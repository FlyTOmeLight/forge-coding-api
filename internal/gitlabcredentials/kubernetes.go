package gitlabcredentials

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (r *runner) distribute(ctx context.Context, saved state) error {
	for _, namespace := range r.config.Namespaces {
		secrets, err := r.kube.CoreV1().Secrets(namespace).List(ctx, meta.ListOptions{})
		if err != nil {
			return err
		}
		for _, secret := range secrets.Items {
			changed := false
			for _, key := range []string{"GITLAB_TOKEN", "git-token"} {
				if string(secret.Data[key]) == saved.Previous.Value {
					secret.Data[key] = []byte(saved.Next.Value)
					changed = true
				}
			}
			if changed {
				if _, err = r.kube.CoreV1().Secrets(namespace).Update(ctx, &secret, meta.UpdateOptions{}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (r *runner) probe(ctx context.Context, target Probe, pod *core.Pod) (token, error) {
	// Fixed command emits only metadata; it cannot return credential plaintext.
	req := r.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(target.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&core.PodExecOptions{Container: target.Container, Command: []string{"/forge", "gitlab-credential-status"}, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(r.rest, http.MethodPost, req.URL())
	if err != nil {
		return token{}, fmt.Errorf("credential probe transport unavailable")
	}
	var output, diagnostic bytes.Buffer
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &output, Stderr: &diagnostic}); err != nil {
		return token{}, fmt.Errorf("credential probe failed for %s/%s", target.Namespace, pod.Name)
	}
	var result token
	if err = json.Unmarshal(output.Bytes(), &result); err != nil {
		return result, fmt.Errorf("invalid credential probe metadata")
	}
	return result, nil
}
func (r *runner) verifyOnce(ctx context.Context, id int64) error {
	for _, target := range r.config.Targets {
		pods, err := r.kube.CoreV1().Pods(target.Namespace).List(ctx, meta.ListOptions{LabelSelector: "app=" + target.Deployment})
		if err != nil {
			return err
		}
		found := false
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				return errNotLoaded
			}
			if pod.Status.Phase != core.PodRunning {
				return errNotLoaded
			}
			ready := false
			for _, container := range pod.Status.ContainerStatuses {
				if container.Name == target.Container && container.Ready {
					ready = true
				}
			}
			if !ready {
				return errNotLoaded
			}
			found = true
			status, err := r.execute(ctx, target, &pod)
			if err != nil {
				return errNotLoaded
			}
			if status.ID != id {
				return errNotLoaded
			}
		}
		if !found {
			return errNotLoaded
		}
	}
	return nil
}
func (r *runner) verify(ctx context.Context, id int64) error {
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		if err := r.verifyOnce(ctx, id); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errNotLoaded
		case <-tick.C:
		}
	}
}
func (r *runner) drain(ctx context.Context) error {
	for _, namespace := range r.config.Namespaces {
		pods, err := r.kube.CoreV1().Pods(namespace).List(ctx, meta.ListOptions{})
		if err != nil {
			return err
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == core.PodSucceeded || pod.Status.Phase == core.PodFailed {
				continue
			}
			if pod.Labels["app"] == "coding-artifact" {
				return fmt.Errorf("old credential retained for active artifact operation")
			}
			if pod.Labels["app"] == "coding-aio" {
				prepared := false
				for _, container := range pod.Status.InitContainerStatuses {
					if container.Name == "prepare" && container.State.Terminated != nil {
						prepared = container.State.Terminated.ExitCode == 0
					}
				}
				if !prepared {
					return fmt.Errorf("old credential retained for repository preparation")
				}
			}
			// Other applications are verified above through hot-loaded credentials.
			if strings.HasPrefix(pod.Name, "aio-") && pod.DeletionTimestamp != nil {
				return fmt.Errorf("old sandbox operation is stopping")
			}
		}
	}
	return nil
}
