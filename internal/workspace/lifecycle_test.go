package workspace

import (
	"context"
	"errors"
	"testing"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func readyTask(t *testing.T) (*Manager, context.Context, Repository) {
	t.Helper()
	ctx := context.Background()
	m := &Manager{kube: fake.NewClientset(), namespace: "test", taskImage: "platform", execCommand: func(context.Context, string, []string) error { return nil }}
	repository := Repository{URL: "https://git/repo", Head: "initial", Branch: "work"}
	if _, err := m.Ensure(ctx, "task", "round-1", repository); !errors.Is(err, ErrPreparing) {
		t.Fatal(err)
	}
	pod, err := m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = types.UID("retained-compute")
	pod.Spec.NodeName = "node-a"
	pod.Status.Phase = core.PodRunning
	pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: "aio", Ready: true}}
	if _, err = m.kube.CoreV1().Pods("test").Update(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Ensure(ctx, "task", "round-1", repository); err != nil {
		t.Fatal(err)
	}
	return m, ctx, repository
}
func TestTaskReuseAndExecutionRecovery(t *testing.T) {
	m, ctx, repository := readyTask(t)
	if err := m.Fence(ctx, "task", "round-1", 1); err != nil {
		t.Fatal(err)
	}
	repository.Head = "published"
	if err := m.Published(ctx, "task", repository.Head); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(ctx, "task", "round-2", repository); err != nil {
		t.Fatal(err)
	}
	if err := m.Fence(ctx, "task", "round-2", 1); err != nil {
		t.Fatal(err)
	}
	pod, _ := m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{})
	if pod.UID != "retained-compute" || pod.Labels["forge-round"] != "round-2" {
		t.Fatal("feedback recreated compute", pod)
	}
	if err := m.Fence(ctx, "task", "round-1", 1); err == nil {
		t.Fatal("old round retained access")
	}
	if err := m.Fence(ctx, "task", "round-2", 2); !errors.Is(err, ErrPreparing) {
		t.Fatal(err)
	}
	if _, err := m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("old owner compute survived", err)
	}
	if _, err := m.Ensure(ctx, "task", "round-2", repository); !errors.Is(err, ErrPreparing) {
		t.Fatal(err)
	}
	pod, _ = m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{})
	found := false
	for _, env := range pod.Spec.InitContainers[0].Env {
		if env.Name == "REUSE_WORKTREE" {
			found = env.Value == "true"
		}
	}
	if !found {
		t.Fatal("recovery discarded retained code and dependencies")
	}
}
func TestSnapshotPausesWithoutDeletingCompute(t *testing.T) {
	m, ctx, repository := readyTask(t)
	calls := 0
	m.execCommand = func(context.Context, string, []string) error { calls++; return nil }
	if err := m.Pause(ctx, "task", "round-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(ctx, "task", "round-2", repository); !errors.Is(err, ErrPreparing) {
		t.Fatal("new round entered frozen compute", err)
	}
	if err := m.Resume(ctx, "task", "round-2"); err == nil {
		t.Fatal("wrong round resumed snapshot")
	}
	if err := m.Resume(ctx, "task", "round-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume(ctx, "task", "round-1"); err != nil {
		t.Fatal(err)
	}
	pod, _ := m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{})
	if pod.UID != "retained-compute" || calls != 2 || pod.Annotations["forge-snapshot-round"] != "" {
		t.Fatal(pod, calls)
	}
}
func TestHumanBranchMoveRefreshesRepository(t *testing.T) {
	m, ctx, repository := readyTask(t)
	repository.Head = "human-change"
	if _, err := m.Ensure(ctx, "task", "round-2", repository); !errors.Is(err, ErrPreparing) {
		t.Fatal(err)
	}
	if _, err := m.Ensure(ctx, "task", "round-2", repository); !errors.Is(err, ErrPreparing) {
		t.Fatal(err)
	}
	pod, _ := m.kube.CoreV1().Pods("test").Get(ctx, name("task"), meta.GetOptions{})
	for _, env := range pod.Spec.InitContainers[0].Env {
		if env.Name == "REUSE_WORKTREE" && env.Value != "false" {
			t.Fatal("human revision ignored")
		}
	}
	if _, err := m.kube.CoreV1().PersistentVolumeClaims("test").Get(ctx, name("task"), meta.GetOptions{}); err != nil {
		t.Fatal("task storage lost", err)
	}
}
func TestDeliveryVolumesAreIsolatedAndStayOnTaskNode(t *testing.T) {
	m, ctx, _ := readyTask(t)
	for _, round := range []string{"round-1", "round-2"} {
		if _, err := m.RunJob(ctx, "task", round, "snapshot", nil); !errors.Is(err, ErrPreparing) {
			t.Fatal(err)
		}
		snapshotBefore, _ := m.kube.CoreV1().Pods("test").Get(ctx, name(round)+"-snapshot", meta.GetOptions{})
		snapshotBefore.Spec.NodeName = "node-a"
		if _, err := m.kube.CoreV1().Pods("test").Update(ctx, snapshotBefore, meta.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Verifier(ctx, "task", round); !errors.Is(err, ErrPreparing) {
			t.Fatal(err)
		}
		snapshot, _ := m.kube.CoreV1().Pods("test").Get(ctx, name(round)+"-snapshot", meta.GetOptions{})
		verifier, _ := m.kube.CoreV1().Pods("test").Get(ctx, name(round)+"-verify", meta.GetOptions{})
		if snapshot.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0] != "node-a" || verifier.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0] != "node-a" {
			t.Fatal("RWO locality lost")
		}
		for _, v := range snapshot.Spec.Volumes {
			if v.Name == "source" && (v.PersistentVolumeClaim.ClaimName != name("task") || !v.PersistentVolumeClaim.ReadOnly) {
				t.Fatal("source isn't read-only")
			}
			if v.Name == "workspace" && v.PersistentVolumeClaim.ClaimName != name(round) {
				t.Fatal("snapshot is exposed to mutable task workspace")
			}
		}
		for _, v := range verifier.Spec.Volumes {
			if v.Name == "input" && v.PersistentVolumeClaim.ClaimName != name(round) {
				t.Fatal("verifier reads mutable task")
			}
		}
	}
}
