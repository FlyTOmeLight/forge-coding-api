package coding

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

// GitLab facts are imported independently of execution lease reconciliation.
func (a *App) runGitLabSync(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if err := a.reconcileGitLabTasks(ctx); err != nil && ctx.Err() == nil {
			logger.FromContext(ctx).Errorf(ctx, "GitLab task scan failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) reconcileGitLabTasks(ctx context.Context) error {
	rows, err := a.DB.Query(
		ctx,
		`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE mr_iid>0 AND status NOT IN ('Completed','Canceled','Undelivered') ORDER BY id`,
	)
	if err != nil {
		return err
	}
	tasks, err := pgx.CollectRows(
		rows,
		func(row pgx.CollectableRow) (table.Task, error) { return table.ScanTask(row) },
	)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := a.syncTaskFacts(ctx, task, "gitlab-sync"); err != nil && ctx.Err() == nil {
			logger.FromContext(ctx).Errorf(ctx, "GitLab task sync failed: task=%s: %v", task.ID, err)
		}
	}
	return nil
}

func (a *App) syncTaskFacts(ctx context.Context, t table.Task, actor string) (table.Task, error) {
	remoteID, e := a.taskProject(ctx, t.TenantID, t.ProjectID)
	if e != nil {
		return table.Task{}, e
	}
	mr, e := a.currentTaskMR(ctx, remoteID, t)
	if e != nil {
		return table.Task{}, e
	}
	notes := []map[string]any{}
	if t.MRIID > 0 {
		notes, e = a.GitLab.MRDiscussions(ctx, remoteID, t.MRIID)
		if e != nil {
			return table.Task{}, e
		}
	}
	snapshotVersion := t.Version
	raw, e := json.Marshal(notes)
	if e != nil {
		return table.Task{}, e
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return table.Task{}, e
	}
	defer tx.Rollback(ctx)
	t, e = table.ScanTask(
		tx.QueryRow(ctx, `SELECT `+table.TaskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, t.ID),
	)
	if e != nil {
		return table.Task{}, e
	}
	if t.Version != snapshotVersion {
		return table.Task{}, problem(409, "STALE_SYNC", "同步期间任务已变化，请重试同步")
	}
	terminal := t.Status == "Canceled" || t.Status == "Undelivered" || t.Status == "Completed"
	stateChanged := !terminal && (mr.State == "merged" || mr.State == "closed")
	needsStop := (mr.State == "merged" || mr.State == "closed") && t.ActiveRun != nil && t.Control != "Stopping"
	var storedNotes []map[string]any
	if e = json.Unmarshal(t.Discussions, &storedNotes); e != nil {
		return table.Task{}, e
	}
	if t.Head == mr.Head && reflect.DeepEqual(storedNotes, notes) && !stateChanged && !needsStop {
		return t, nil
	}
	if mr.IID > 0 {
		t.MRIID = mr.IID
	}
	changed := t.Head != mr.Head
	t.Discussions = raw
	expectedPush := false
	if changed && t.ActiveRun != nil && t.Control == "Agent" {
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coding_runs WHERE id=$1 AND epoch=$2 AND status IN ('Running','Delivering') AND output->>'expectedHead'=$3)`, *t.ActiveRun, t.Epoch, mr.Head).
			Scan(&expectedPush); e != nil {
			return table.Task{}, e
		}
	}
	if changed {
		t.Head = mr.Head
		if _, e = tx.Exec(
			ctx,
			`UPDATE coding_feedback SET status='NeedsReconfirmation' WHERE task_id=$1 AND status='Pending' AND head<>$2`,
			t.ID,
			t.Head,
		); e != nil {
			return table.Task{}, e
		}
		t.AgentJudgment = json.RawMessage(`{"status":"Unknown"}`)
		t.Checks = "NotRun"
		t.CheckedHead = ""
		t.ApprovalHead = ""
		if !expectedPush {
			t.Epoch++
			t.Control = "Human"
			if t.ActiveRun != nil {
				t.Control = "Stopping"
				t.StopConfirmed = false
				now := time.Now().UTC()
				t.StopRequestedAt = &now
				_, e = tx.Exec(
					ctx,
					`UPDATE coding_runs SET status='Stopping' WHERE id=$1`,
					*t.ActiveRun,
				)
			}
		}
	}
	if mr.State == "merged" && !terminal {
		t.Status = "Completed"
		t.Control = "Human"
		t.Epoch++
	}
	if mr.State == "closed" && !terminal {
		t.Status = "Undelivered"
		t.Control = "Human"
		t.Epoch++
	}
	if (mr.State == "merged" || mr.State == "closed") && t.ActiveRun != nil {
		t.StopConfirmed = false
		t.Control = "Stopping"
		now := time.Now().UTC()
		t.StopRequestedAt = &now
		_, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, *t.ActiveRun)
	}
	if e != nil {
		return table.Task{}, e
	}
	if e = table.WriteTask(ctx, tx, t); e != nil {
		return table.Task{}, e
	}
	if e = table.TaskEvent(
		ctx,
		tx,
		t.ID,
		actor,
		"GitLabSync",
		Object{"head": mr.Head, "mrState": mr.State, "codeChanged": changed},
	); e != nil {
		return table.Task{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return table.Task{}, e
	}
	t.Version++
	return t, nil
}
