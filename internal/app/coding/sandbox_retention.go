package coding

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Task locks serialize collection with feedback/continue actions. Active rounds
// retain compute, including long-running model and delivery operations.
func (a *App) reapSandboxes(ctx context.Context) error {
	if a.Workspaces == nil {
		return nil
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(
		ctx,
		`SELECT id FROM coding_tasks WHERE sandbox_active AND active_run IS NULL AND (control='Human' OR status IN ('Completed','Canceled','Undelivered') OR updated_at < now()-interval '30 minutes') ORDER BY updated_at LIMIT 100 FOR UPDATE SKIP LOCKED`,
	)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.Workspaces.Stop(ctx, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE coding_tasks SET sandbox_active=false WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
