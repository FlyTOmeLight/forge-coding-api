package workspace

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Evaluation artifacts are persisted in the Coding database before collection.
// Refuse ordinary task names so business worktrees can never enter this path.
func (m *Manager) CleanupEvaluation(ctx context.Context, task, round string) error {
	if !strings.HasPrefix(task, "eval-") || round != task+"-result" {
		return errors.New("invalid evaluation environment")
	}
	if err := m.StopAll(ctx, task, round); err != nil {
		return err
	}
	for _, n := range []string{name(task), name(round), name(round) + "-verify", name(round) + "-snapshot"} {
		for _, err := range []error{
			m.kube.CoreV1().Pods(m.namespace).Delete(ctx, n, meta.DeleteOptions{}),
			m.kube.CoreV1().Services(m.namespace).Delete(ctx, n, meta.DeleteOptions{}),
			m.kube.CoreV1().PersistentVolumeClaims(m.namespace).Delete(ctx, n, meta.DeleteOptions{}),
			m.kube.CoreV1().Secrets(m.namespace).Delete(ctx, n, meta.DeleteOptions{}),
		} {
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
