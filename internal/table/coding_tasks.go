package table

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Task is the persisted coding_tasks row. HTTP contracts live in the SDK.
type Task struct {
	AgentJudgment   json.RawMessage `json:"agentJudgment"`
	BaseBranch      string          `json:"baseBranch"`
	Reproduce       string          `json:"reproduce"`
	Constraints     string          `json:"constraints"`
	Reference       string          `json:"reference"`
	ID              string          `json:"id"`
	TenantID        string          `json:"tenantId"`
	ProjectID       string          `json:"projectId"`
	OwnerID         string          `json:"ownerId"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Criteria        string          `json:"criteria"`
	Branch          string          `json:"branch"`
	TargetBranch    string          `json:"targetBranch"`
	MRIID           int64           `json:"mrIid"`
	Head            string          `json:"head"`
	Status          string          `json:"status"`
	Control         string          `json:"control"`
	Version         int64           `json:"version"`
	Epoch           int64           `json:"epoch"`
	Round           int             `json:"round"`
	Checks          string          `json:"checks"`
	CheckedHead     string          `json:"checkedHead"`
	ApprovalHead    string          `json:"approvalHead"`
	ReviewerIDs     []string        `json:"reviewerIds"`
	ActiveRun       *string         `json:"activeRun"`
	StopRequestedAt *time.Time      `json:"stopRequestedAt"`
	StopConfirmed   bool            `json:"stopConfirmed"`
	Discussions     json.RawMessage `json:"discussions"`
	Synthetic       bool            `json:"synthetic"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

const TaskColumns = `id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,status,control,version,epoch,round,checks,checked_head,approval_head,reviewer_ids,active_run,stop_requested_at,stop_confirmed,discussions,synthetic,updated_at,base_branch,reproduce,constraints,reference,agent_judgment`

func ScanTask(row pgx.Row) (Task, error) {
	var t Task
	e := row.Scan(
		&t.ID,
		&t.TenantID,
		&t.ProjectID,
		&t.OwnerID,
		&t.Title,
		&t.Description,
		&t.Criteria,
		&t.Branch,
		&t.TargetBranch,
		&t.MRIID,
		&t.Head,
		&t.Status,
		&t.Control,
		&t.Version,
		&t.Epoch,
		&t.Round,
		&t.Checks,
		&t.CheckedHead,
		&t.ApprovalHead,
		&t.ReviewerIDs,
		&t.ActiveRun,
		&t.StopRequestedAt,
		&t.StopConfirmed,
		&t.Discussions,
		&t.Synthetic,
		&t.UpdatedAt,
		&t.BaseBranch,
		&t.Reproduce,
		&t.Constraints,
		&t.Reference,
		&t.AgentJudgment,
	)
	return t, e
}

func WriteTask(ctx context.Context, tx pgx.Tx, t Task) error {
	_, e := tx.Exec(
		ctx,
		`UPDATE coding_tasks SET owner_id=$2,head=$3,status=$4,control=$5,epoch=$6,round=$7,checks=$8,checked_head=$9,approval_head=$10,active_run=$11,stop_requested_at=$12,stop_confirmed=$13,discussions=$14,mr_iid=$15,agent_judgment=$16,version=version+1,updated_at=now() WHERE id=$1`,
		t.ID,
		t.OwnerID,
		t.Head,
		t.Status,
		t.Control,
		t.Epoch,
		t.Round,
		t.Checks,
		t.CheckedHead,
		t.ApprovalHead,
		t.ActiveRun,
		t.StopRequestedAt,
		t.StopConfirmed,
		t.Discussions,
		t.MRIID,
		t.AgentJudgment,
	)
	return e
}

func ListTasks(ctx context.Context, db DB, tenantID, projectID string) ([]Task, error) {
	rows, err := db.Query(
		ctx,
		`SELECT `+TaskColumns+` FROM coding_tasks WHERE tenant_id=$1 AND project_id=$2 ORDER BY updated_at DESC,id DESC LIMIT 100`,
		tenantID,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Task, error) { return ScanTask(row) })
	if items == nil {
		items = []Task{}
	}
	return items, err
}

func FindScopedTask(ctx context.Context, db DB, taskID, tenantID, projectID string) (Task, error) {
	return ScanTask(
		db.QueryRow(
			ctx,
			`SELECT `+TaskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`,
			taskID,
			tenantID,
			projectID,
		),
	)
}

func AssociateTask(ctx context.Context, db DB, t Task) (Task, error) {
	return ScanTask(
		db.QueryRow(
			ctx,
			`INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,reviewer_ids,synthetic) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(project_id,mr_iid) WHERE mr_iid>0 DO UPDATE SET id=coding_tasks.id RETURNING `+TaskColumns,
			t.ID,
			t.TenantID,
			t.ProjectID,
			t.OwnerID,
			t.Title,
			t.Description,
			t.Criteria,
			t.Branch,
			t.TargetBranch,
			t.MRIID,
			t.Head,
			t.ReviewerIDs,
			t.Synthetic,
		),
	)
}

func LockScopedTask(ctx context.Context, db DB, taskID, tenantID, projectID string) (Task, error) {
	return ScanTask(
		db.QueryRow(
			ctx,
			`SELECT `+TaskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3 FOR UPDATE`,
			taskID,
			tenantID,
			projectID,
		),
	)
}

func DeactivateTaskSandbox(ctx context.Context, db DB, taskID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_tasks SET sandbox_active=false WHERE id=$1`, taskID)
}

func LockTask(ctx context.Context, db DB, taskID string) (Task, error) {
	return ScanTask(db.QueryRow(ctx, `SELECT `+TaskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, taskID))
}

func LockTaskRequest(ctx context.Context, db DB, requestKey string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, requestKey)
}

func TaskRequestHash(ctx context.Context, db DB, requestKey string) (requestHash string, err error) {
	err = db.QueryRow(ctx, `SELECT request_hash FROM coding_tasks WHERE request_key=$1`, requestKey).
		Scan(&requestHash)
	return
}

func FindTaskByRequest(ctx context.Context, db DB, requestKey string) (Task, error) {
	return ScanTask(db.QueryRow(ctx, `SELECT `+TaskColumns+` FROM coding_tasks WHERE request_key=$1`, requestKey))
}

func CreateProblemTask(ctx context.Context, db DB, t Task, runID, requestKey, requestHash string) (Task, error) {
	return ScanTask(
		db.QueryRow(
			ctx,
			`INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,reproduce,criteria,constraints,reference,base_branch,branch,target_branch,mr_iid,head,reviewer_ids,synthetic,status,control,round,active_run,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$11,0,$13,$14,$15,'Queued','Agent',1,$16,$17,$18) RETURNING `+TaskColumns,
			t.ID,
			t.TenantID,
			t.ProjectID,
			t.OwnerID,
			t.Title,
			t.Description,
			t.Reproduce,
			t.Criteria,
			t.Constraints,
			t.Reference,
			t.BaseBranch,
			t.Branch,
			t.Head,
			t.ReviewerIDs,
			t.Synthetic,
			runID,
			requestKey,
			requestHash,
		),
	)
}

func TryLockTask(ctx context.Context, db DB, taskID string) (Task, error) {
	return ScanTask(
		db.QueryRow(ctx, `SELECT `+TaskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE SKIP LOCKED`, taskID),
	)
}

func ActivateTaskSandbox(ctx context.Context, db DB, taskID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_tasks SET sandbox_active=true WHERE id=$1`, taskID)
}

func FindTask(ctx context.Context, db DB, taskID string) (Task, error) {
	return ScanTask(db.QueryRow(ctx, `SELECT `+TaskColumns+` FROM coding_tasks WHERE id=$1`, taskID))
}

func ListSyncTasks(ctx context.Context, db DB) ([]Task, error) {
	rows, err := db.Query(
		ctx,
		`SELECT `+TaskColumns+` FROM coding_tasks WHERE mr_iid>0 AND status NOT IN ('Completed','Canceled','Undelivered') ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Task, error) { return ScanTask(row) })
	if items == nil {
		items = []Task{}
	}
	return items, err
}

func LockIdleSandboxIDs(ctx context.Context, db DB) ([]string, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id FROM coding_tasks WHERE sandbox_active AND active_run IS NULL AND (control='Human' OR status IN ('Completed','Canceled','Undelivered') OR updated_at < now()-interval '30 minutes') ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED`,
	)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if items == nil {
		items = []string{}
	}
	return items, err
}
