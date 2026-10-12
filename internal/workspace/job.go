package workspace

import (
	"context"
	"errors"
	"io"
	"strings"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type OperationFailed struct{ Phase, Detail string }

func (e *OperationFailed) Error() string { return "workspace " + e.Phase + " failed: " + e.Detail }

var ErrPreparing = errors.New("workspace operation is preparing")

func (m *Manager) RunJob(ctx context.Context, task, round, phase string, env map[string]string) ([]byte, error) {
	n := name(round) + "-" + phase
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, n, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		pvc := &core.PersistentVolumeClaim{ObjectMeta: meta.ObjectMeta{Name: name(round), Labels: labels(round)}, Spec: core.PersistentVolumeClaimSpec{AccessModes: []core.PersistentVolumeAccessMode{core.ReadWriteOnce}, Resources: core.VolumeResourceRequirements{Requests: core.ResourceList{core.ResourceStorage: resource.MustParse("2Gi")}}}}
		if _, err = m.kube.CoreV1().PersistentVolumeClaims(m.namespace).Create(ctx, pvc, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if m.taskImage == "" {
			return nil, errors.New("workspace task image is required")
		}
		node, err := m.deliveryNode(ctx, task, round)
		if err != nil {
			return nil, err
		}
		variables := []core.EnvVar{{Name: "GITLAB_TOKEN", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: name(task)}, Key: "git-token"}}}, {Name: "CODING_TOKEN", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: name(task)}, Key: "coding-token"}}}}
		for key, value := range env {
			variables = append(variables, core.EnvVar{Name: key, Value: value})
		}
		volumes := []core.Volume{{Name: "workspace", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name(round)}}}, {Name: "scratch", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}}
		mounts := []core.VolumeMount{{Name: "workspace", MountPath: "/volume"}, {Name: "scratch", MountPath: "/scratch"}}
		if phase == "snapshot" || phase == "publish" {
			volumes = append(volumes, core.Volume{Name: "source", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name(task), ReadOnly: phase == "snapshot"}}})
			mounts = append(mounts, core.VolumeMount{Name: "source", MountPath: "/source", ReadOnly: phase == "snapshot"})
		}
		if phase == "publish" {
			volumes = append(volumes, core.Volume{Name: "verified", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name(round) + "-verify", ReadOnly: true}}})
			mounts = append(mounts, core.VolumeMount{Name: "verified", MountPath: "/verified", ReadOnly: true})
		}
		pod = &core.Pod{ObjectMeta: meta.ObjectMeta{Name: n, Labels: map[string]string{"app": "coding-artifact", "forge-round": round, "forge-phase": phase}}, Spec: core.PodSpec{Affinity: deliveryAffinity(node), AutomountServiceAccountToken: pointer(false), RestartPolicy: core.RestartPolicyNever, SecurityContext: &core.PodSecurityContext{RunAsUser: pointer(int64(1000)), RunAsGroup: pointer(int64(1000)), FSGroup: pointer(int64(1000))}, Containers: []core.Container{{Name: "artifact", Image: m.taskImage, Command: []string{"/forge", "workspace-" + phase}, Env: variables, VolumeMounts: mounts, Resources: core.ResourceRequirements{Limits: core.ResourceList{core.ResourceCPU: resource.MustParse("1"), core.ResourceMemory: resource.MustParse("512Mi")}}}}, Volumes: volumes}}
		_, err = m.kube.CoreV1().Pods(m.namespace).Create(ctx, pod, meta.CreateOptions{})
		if err != nil {
			return nil, err
		}
		return nil, ErrPreparing
	}
	if err != nil {
		return nil, err
	}
	if pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
		return nil, ErrPreparing
	}
	stream, err := m.kube.CoreV1().Pods(m.namespace).GetLogs(n, &core.PodLogOptions{Container: "artifact"}).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	raw, err := io.ReadAll(io.LimitReader(stream, 4<<20))
	if err != nil {
		return nil, err
	}
	if pod.Status.Phase == core.PodFailed {
		return nil, &OperationFailed{Phase: phase, Detail: string(raw)}
	}
	return raw, nil
}
func (m *Manager) Verifier(ctx context.Context, task, round string) (Endpoint, error) {
	n := name(round) + "-verify"
	pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, n, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		pvc := &core.PersistentVolumeClaim{ObjectMeta: meta.ObjectMeta{Name: n, Labels: labels(round)}, Spec: core.PersistentVolumeClaimSpec{AccessModes: []core.PersistentVolumeAccessMode{core.ReadWriteOnce}, Resources: core.VolumeResourceRequirements{Requests: core.ResourceList{core.ResourceStorage: resource.MustParse("2Gi")}}}}
		if _, err = m.kube.CoreV1().PersistentVolumeClaims(m.namespace).Create(ctx, pvc, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return Endpoint{}, err
		}
		service := &core.Service{ObjectMeta: meta.ObjectMeta{Name: n}, Spec: core.ServiceSpec{Selector: map[string]string{"app": "coding-verifier", "forge-round": round}, Ports: []core.ServicePort{{Port: 8080}}}}
		if _, err = m.kube.CoreV1().Services(m.namespace).Create(ctx, service, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return Endpoint{}, err
		}
		node, err := m.deliveryNode(ctx, task, round)
		if err != nil {
			return Endpoint{}, err
		}
		mainEnv := []core.EnvVar{{Name: "WORKSPACE", Value: "/workspace"}, {Name: "SANDBOX_API_KEY", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: name(task)}, Key: "aio-token"}}}}
		pod = &core.Pod{ObjectMeta: meta.ObjectMeta{Name: n, Labels: map[string]string{"app": "coding-verifier", "forge-round": round}}, Spec: core.PodSpec{Affinity: deliveryAffinity(node), AutomountServiceAccountToken: pointer(false), RestartPolicy: core.RestartPolicyNever, SecurityContext: &core.PodSecurityContext{FSGroup: pointer(int64(1000))}, InitContainers: []core.Container{{Name: "prepare", Image: Image, Command: []string{"sh", "-eu", "-c", "mkdir -p /workspace/repo; cp -a /input/candidate/. /workspace/repo/; rm -rf /workspace/repo/.git"}, SecurityContext: &core.SecurityContext{RunAsUser: pointer(int64(1000))}, VolumeMounts: []core.VolumeMount{{Name: "input", MountPath: "/input", ReadOnly: true}, {Name: "workspace", MountPath: "/workspace"}}}}, Containers: []core.Container{{Name: "aio", Image: Image, Env: mainEnv, ReadinessProbe: &core.Probe{ProbeHandler: core.ProbeHandler{TCPSocket: &core.TCPSocketAction{Port: intstr.FromInt32(8080)}}, InitialDelaySeconds: 5, PeriodSeconds: 2}, Ports: []core.ContainerPort{{ContainerPort: 8080}}, VolumeMounts: []core.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}, Resources: core.ResourceRequirements{Limits: core.ResourceList{core.ResourceCPU: resource.MustParse("2"), core.ResourceMemory: resource.MustParse("3Gi")}}}}, Volumes: []core.Volume{{Name: "input", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name(round), ReadOnly: true}}}, {Name: "workspace", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: n}}}}}}
		_, err = m.kube.CoreV1().Pods(m.namespace).Create(ctx, pod, meta.CreateOptions{})
		if err != nil {
			return Endpoint{}, err
		}
		return Endpoint{}, ErrPreparing
	}
	if err != nil {
		return Endpoint{}, err
	}
	if pod.Status.Phase == core.PodFailed {
		return Endpoint{}, errors.New("verification environment failed")
	}
	ready := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "aio" && status.Ready {
			ready = true
		}
	}
	if !ready {
		return Endpoint{}, ErrPreparing
	}
	endpoint, err := m.Access(ctx, task)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint.URL = strings.Replace(endpoint.URL, name(task)+".", n+".", 1)
	return endpoint, nil
}

// RWO volumes must stay on one node while their owning sandbox is running.
func (m *Manager) deliveryNode(ctx context.Context, task, round string) (string, error) {
	for _, podName := range []string{name(round) + "-snapshot", name(task)} {
		pod, err := m.kube.CoreV1().Pods(m.namespace).Get(ctx, podName, meta.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return pod.Spec.NodeName, nil
	}
	return "", nil
}

func deliveryAffinity(node string) *core.Affinity {
	if node == "" {
		return nil
	}
	return &core.Affinity{NodeAffinity: &core.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &core.NodeSelector{NodeSelectorTerms: []core.NodeSelectorTerm{{MatchFields: []core.NodeSelectorRequirement{{Key: "metadata.name", Operator: core.NodeSelectorOpIn, Values: []string{node}}}}}}}}
}
