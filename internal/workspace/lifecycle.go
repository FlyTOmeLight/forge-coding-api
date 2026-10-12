package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// Pause preserves process memory while the platform copies the repository.
// Its annotation prevents a new execution from accessing frozen compute.
func (m *Manager) Pause(ctx context.Context, task, round string) error {
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, name(task), meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if pod.Labels["forge-round"] != round {
		return errors.New("stale snapshot owner")
	}
	if owner := pod.Annotations["forge-snapshot-round"]; owner != "" && owner != round {
		return errors.New("snapshot already owned by another round")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["forge-snapshot-round"] = round
	if _, err = m.kube.CoreV1().Pods(m.namespace).Update(ctx, pod, meta.UpdateOptions{}); err != nil {
		return err
	}
	if err = m.command(ctx, pod.Name, []string{"/usr/bin/python3", "-I", "-c", pauseScript}); err != nil {
		return &OperationFailed{Phase: "snapshot", Detail: err.Error()}
	}
	return nil
}
func (m *Manager) Resume(ctx context.Context, task, round string) error {
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, name(task), meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if pod.Annotations["forge-snapshot-round"] == "" {
		return nil
	}
	if pod.Annotations["forge-snapshot-round"] != round {
		return errors.New("stale snapshot owner")
	}
	if err = m.command(ctx, pod.Name, []string{"/usr/bin/python3", "-I", "-c", resumeScript}); err != nil {
		return err
	}
	delete(pod.Annotations, "forge-snapshot-round")
	_, err = m.kube.CoreV1().Pods(m.namespace).Update(ctx, pod, meta.UpdateOptions{})
	return err
}

// Published identifies the revision represented by the retained working tree.
func (m *Manager) Published(ctx context.Context, task, head string) error {
	if _, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, name(task), meta.GetOptions{}); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	pvc, err := m.kube.CoreV1().PersistentVolumeClaims(m.namespace).Get(ctx, name(task), meta.GetOptions{})
	if err != nil {
		return err
	}
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	pvc.Annotations["forge-prepared-head"] = head
	if _, err = m.kube.CoreV1().PersistentVolumeClaims(m.namespace).Update(ctx, pvc, meta.UpdateOptions{}); err != nil {
		return err
	}
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, name(task), meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["forge-workspace-head"] = head
	_, err = m.kube.CoreV1().Pods(m.namespace).Update(ctx, pod, meta.UpdateOptions{})
	return err
}
func (m *Manager) command(ctx context.Context, pod string, command []string) error {
	if m.execCommand != nil {
		return m.execCommand(ctx, pod, command)
	}
	req := m.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(m.namespace).Name(pod).SubResource("exec").VersionedParams(&core.PodExecOptions{Container: "aio", Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(m.config, "POST", req.URL())
	if err != nil {
		return err
	}
	var output, diagnostic bytes.Buffer
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &output, Stderr: &diagnostic}); err != nil {
		return fmt.Errorf("sandbox process control failed: %w: %s", err, output.String()+diagnostic.String())
	}
	return nil
}

// Keep PID 1 and exec ancestors alive so Kubernetes can run Resume. Enumerate
// until all other living processes are stopped, including background writers.
const pauseScript = `
import os, signal, time
excluded = {1}
pid = os.getpid()
while pid > 1:
    excluded.add(pid)
    raw = open('/proc/%d/stat' % pid).read().rsplit(')', 1)[1].split()
    pid = int(raw[1])
deadline = time.monotonic() + 5
stable = 0
while stable < 2:
    pending = False
    for entry in os.listdir('/proc'):
        if not entry.isdigit() or int(entry) in excluded: continue
        try:
            raw = open('/proc/%s/stat' % entry).read().rsplit(')', 1)[1].split()
            if raw[0] not in ('T', 't', 'Z', 'X'):
                os.kill(int(entry), signal.SIGSTOP)
                pending = True
        except ProcessLookupError: pass
        except FileNotFoundError: pass
    stable = 0 if pending else stable + 1
    if time.monotonic() > deadline: raise RuntimeError('sandbox processes did not quiesce')
    time.sleep(0.05)
`
const resumeScript = `
import os, signal
for entry in os.listdir('/proc'):
    if not entry.isdigit() or int(entry) == 1: continue
    try: os.kill(int(entry), signal.SIGCONT)
    except ProcessLookupError: pass
`
