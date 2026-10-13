// Package workspace owns task-scoped AIO environments and per-round delivery resources. It uses Kubernetes' native
// resources and AIO's public protocol, without another model execution loop.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const Image = "ghcr.io/agent-infra/sandbox@sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7"

type Manager struct {
	kube        kubernetes.Interface
	namespace   string
	taskImage   string
	config      *rest.Config
	execCommand func(context.Context, string, []string) error
}

type Repository struct{ URL, Head, Branch, Token, PermitToken string }

type Endpoint struct{ URL, Token string }

func New(namespace, taskImage string) (*Manager, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	config.Timeout = 15 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Manager{kube: kube, namespace: namespace, taskImage: taskImage, config: config}, nil
}

func name(round string) string { return "aio-" + strings.ReplaceAll(round, "_", "-") }

func pointer[T any](v T) *T { return &v }

func labels(round string) map[string]string { return map[string]string{"forge-round": round} }

func taskLabels(task, round string) map[string]string {
	return map[string]string{"app": "coding-aio", "forge-task": task, "forge-round": round}
}

func (m *Manager) Ensure(ctx context.Context, task, round string, repository Repository) (Endpoint, error) {
	n := name(task)
	ns := m.namespace
	secret, err := m.kube.CoreV1().Secrets(ns).Get(ctx, n, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		var raw [32]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return Endpoint{}, err
		}
		secret, err = m.kube.CoreV1().
			Secrets(ns).
			Create(ctx, &core.Secret{ObjectMeta: meta.ObjectMeta{Name: n, Labels: taskLabels(task, round)}, Data: map[string][]byte{"aio-token": []byte(hex.EncodeToString(raw[:])), "git-token": []byte(repository.Token), "coding-token": []byte(repository.PermitToken)}}, meta.CreateOptions{})
	}
	if err != nil {
		return Endpoint{}, err
	}
	pvc := &core.PersistentVolumeClaim{
		ObjectMeta: meta.ObjectMeta{Name: n, Labels: taskLabels(task, round)},
		Spec: core.PersistentVolumeClaimSpec{
			AccessModes: []core.PersistentVolumeAccessMode{core.ReadWriteOnce},
			Resources: core.VolumeResourceRequirements{
				Requests: core.ResourceList{core.ResourceStorage: resource.MustParse("2Gi")},
			},
		},
	}
	pvc, err = m.kube.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, meta.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		pvc, err = m.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, n, meta.GetOptions{})
	}
	if err != nil {
		return Endpoint{}, err
	}
	service := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: n, Labels: taskLabels(task, round)},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "coding-aio", "forge-task": task},
			Ports:    []core.ServicePort{{Port: 8080}},
		},
	}
	if _, err = m.kube.CoreV1().
		Services(ns).
		Create(ctx, service, meta.CreateOptions{}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		return Endpoint{}, err
	}
	pod, err := m.kube.CoreV1().Pods(ns).Get(ctx, n, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		env := []core.EnvVar{
			{Name: "WORKSPACE", Value: "/workspace"},
			{
				Name: "SANDBOX_API_KEY",
				ValueFrom: &core.EnvVarSource{
					SecretKeyRef: &core.SecretKeySelector{
						LocalObjectReference: core.LocalObjectReference{Name: n},
						Key:                  "aio-token",
					},
				},
			},
		}
		prepare := core.Container{
			Name:    "prepare",
			Image:   Image,
			Command: []string{"sh", "-eu", "-c", prepareScript},
			Env: []core.EnvVar{
				{
					Name: "REUSE_WORKTREE",
					Value: strconv.FormatBool(
						pvc.Annotations["forge-prepared-head"] == repository.Head,
					),
				},
				{Name: "REPOSITORY_URL", Value: repository.URL},
				{Name: "BASE_HEAD", Value: repository.Head},
				{Name: "WORK_BRANCH", Value: repository.Branch},
				{
					Name: "GITLAB_TOKEN",
					ValueFrom: &core.EnvVarSource{
						SecretKeyRef: &core.SecretKeySelector{
							LocalObjectReference: core.LocalObjectReference{Name: n},
							Key:                  "git-token",
						},
					},
				},
			},
			VolumeMounts: []core.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
			SecurityContext: &core.SecurityContext{
				RunAsUser:  pointer(int64(1000)),
				RunAsGroup: pointer(int64(1000)),
			},
		}
		pod = &core.Pod{
			ObjectMeta: meta.ObjectMeta{
				Name:        n,
				Labels:      taskLabels(task, round),
				Annotations: map[string]string{"forge-workspace-head": repository.Head},
			},
			Spec: core.PodSpec{
				AutomountServiceAccountToken: pointer(false),
				RestartPolicy:                core.RestartPolicyNever,
				SecurityContext:              &core.PodSecurityContext{FSGroup: pointer(int64(1000))},
				InitContainers:               []core.Container{prepare},
				Containers: []core.Container{
					{
						Name:  "aio",
						Image: Image,
						Env:   env,
						Ports: []core.ContainerPort{{ContainerPort: 8080}},
						ReadinessProbe: &core.Probe{
							ProbeHandler: core.ProbeHandler{
								TCPSocket: &core.TCPSocketAction{
									Port: intstr.FromInt32(8080),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       2,
						},
						Resources: core.ResourceRequirements{
							Requests: core.ResourceList{
								core.ResourceCPU:    resource.MustParse("200m"),
								core.ResourceMemory: resource.MustParse("512Mi"),
							},
							Limits: core.ResourceList{
								core.ResourceCPU:    resource.MustParse("2"),
								core.ResourceMemory: resource.MustParse("3Gi"),
							},
						},
						VolumeMounts: []core.VolumeMount{
							{Name: "workspace", MountPath: "/workspace"},
							{Name: "shm", MountPath: "/dev/shm"},
						},
					},
				},
				Volumes: []core.Volume{
					{
						Name: "workspace",
						VolumeSource: core.VolumeSource{
							PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
								ClaimName: n,
							},
						},
					},
					{
						Name: "shm",
						VolumeSource: core.VolumeSource{
							EmptyDir: &core.EmptyDirVolumeSource{
								Medium:    core.StorageMediumMemory,
								SizeLimit: pointer(resource.MustParse("512Mi")),
							},
						},
					},
				},
			},
		}
		pod, err = m.kube.CoreV1().Pods(ns).Create(ctx, pod, meta.CreateOptions{})
	}
	if err != nil {
		return Endpoint{}, err
	}
	if pod.Annotations["forge-snapshot-round"] != "" {
		return Endpoint{}, ErrPreparing
	}
	if pod.Annotations["forge-workspace-head"] != repository.Head {
		// A human moved the branch, or compute expired before publication was recorded.
		// Refresh repository contents on recreation; dependency caches on the PVC survive.
		if err = m.Stop(ctx, task); err != nil {
			return Endpoint{}, err
		}
		return Endpoint{}, ErrPreparing
	}
	if pod.Labels["forge-round"] != round {
		pod.Labels["forge-round"] = round
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["forge-runtime-epoch"] = "0"
		pod, err = m.kube.CoreV1().Pods(ns).Update(ctx, pod, meta.UpdateOptions{})
		if err != nil {
			return Endpoint{}, err
		}
	}
	if pod.Status.Phase == core.PodFailed || pod.Status.Phase == core.PodSucceeded {
		return Endpoint{}, errors.New(
			"AIO instance is not running; inspect stop and workspace before recreating",
		)
	}
	if pod.DeletionTimestamp != nil {
		return Endpoint{}, ErrPreparing
	}
	ready := false
	for _, container := range pod.Status.ContainerStatuses {
		if container.Name == "aio" && container.Ready {
			ready = true
		}
	}
	if !ready {
		return Endpoint{}, ErrPreparing
	}
	if pvc.Annotations["forge-prepared-head"] != repository.Head {
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		pvc.Annotations["forge-prepared-head"] = repository.Head
		if _, err = m.kube.CoreV1().
			PersistentVolumeClaims(ns).
			Update(ctx, pvc, meta.UpdateOptions{}); err != nil {
			return Endpoint{}, err
		}
	}
	return Endpoint{URL: "http://" + n + "." + ns + ".svc:8080", Token: string(secret.Data["aio-token"])}, nil
}

func (m *Manager) Access(ctx context.Context, round string) (Endpoint, error) {
	secret, err := m.kube.CoreV1().Secrets(m.namespace).Get(ctx, name(round), meta.GetOptions{})
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		URL:   "http://" + name(round) + "." + m.namespace + ".svc:8080",
		Token: string(secret.Data["aio-token"]),
	}, nil
}

func (m *Manager) Stop(ctx context.Context, round string) error {
	n := name(round)
	err := m.kube.CoreV1().
		Pods(m.namespace).
		Delete(ctx, n, meta.DeleteOptions{GracePeriodSeconds: pointer(int64(10))})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		_, err = m.kube.CoreV1().Pods(m.namespace).Get(ctx, n, meta.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("AIO stop is not confirmed")
		case <-tick.C:
		}
	}
}

// Fence stops a compute instance before changing its execution owner. The PVC
// persists. A new instance may start only after the old one is absent.
func (m *Manager) Fence(ctx context.Context, task, round string, epoch int64) error {
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, name(task), meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrPreparing
	}
	if err != nil {
		return err
	}
	if pod.Labels["forge-round"] != round {
		return errors.New("stale AIO round")
	}
	old, _ := strconv.ParseInt(pod.Annotations["forge-runtime-epoch"], 10, 64)
	if old == epoch {
		return nil
	}
	if old > epoch {
		return errors.New("stale AIO owner epoch")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["forge-runtime-epoch"] = strconv.FormatInt(epoch, 10)
	// Update uses resourceVersion so two claimants cannot overwrite each other.
	updated, err := m.kube.CoreV1().Pods(m.namespace).Update(ctx, pod, meta.UpdateOptions{})
	if err != nil {
		return err
	}
	if old > 0 {
		err = m.kube.CoreV1().
			Pods(m.namespace).
			Delete(ctx, pod.Name, meta.DeleteOptions{GracePeriodSeconds: pointer(int64(10)), Preconditions: &meta.Preconditions{UID: &updated.UID}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return ErrPreparing
	}
	return nil
}

const prepareScript = `
if test "$REUSE_WORKTREE" = true; then exit 0; fi
rm -rf /workspace/repo
mkdir -p /workspace/repo
cat >/tmp/askpass <<'ASKPASS'
#!/bin/sh
case "$1" in *Username*) printf '%s\n' oauth2;; *) printf '%s\n' "$GITLAB_TOKEN";; esac
ASKPASS
chmod 700 /tmp/askpass
export GIT_ASKPASS=/tmp/askpass GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null
git -c credential.helper= clone --no-checkout "$REPOSITORY_URL" /workspace/repo
git -C /workspace/repo fetch origin "$BASE_HEAD"
git -C /workspace/repo checkout -B "$WORK_BRANCH" "$BASE_HEAD"
rm /tmp/askpass
`

// StopAll confirms every active compute process owned by a round has stopped.
// Finished artifact Pods remain available for result reconciliation.
func (m *Manager) StopAll(ctx context.Context, task, round string) error {
	if err := m.Stop(ctx, task); err != nil {
		return err
	}
	pods, err := m.kube.CoreV1().
		Pods(m.namespace).
		List(ctx, meta.ListOptions{LabelSelector: "forge-round=" + round})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == core.PodSucceeded || pod.Status.Phase == core.PodFailed {
			continue
		}
		if err = m.kube.CoreV1().
			Pods(m.namespace).
			Delete(ctx, pod.Name, meta.DeleteOptions{GracePeriodSeconds: pointer(int64(10))}); err != nil &&
			!apierrors.IsNotFound(err) {
			return err
		}
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		pods, err = m.kube.CoreV1().
			Pods(m.namespace).
			List(ctx, meta.ListOptions{LabelSelector: "forge-round=" + round})
		if err != nil {
			return err
		}
		active := false
		for _, pod := range pods.Items {
			if pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
				active = true
			}
		}
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("round stop is not confirmed")
		case <-tick.C:
		}
	}
}
