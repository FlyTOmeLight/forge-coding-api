package task

import (
	"context"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

// Task locks serialize collection with feedback/continue actions. Active rounds
// retain compute, including long-running model and delivery operations.
func (a *Service) reapSandboxes(ctx context.Context) error {
	if a.Workspaces == nil {
		return nil
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ids, err := table.LockIdleSandboxIDs(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.Workspaces.Stop(ctx, id); err != nil {
			return err
		}
		if _, err = table.DeactivateTaskSandbox(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
