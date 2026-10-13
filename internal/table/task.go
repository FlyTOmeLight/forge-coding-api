package table

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Task keeps code ownership separate from task responsibility and review.
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

func TaskEvent(ctx context.Context, tx pgx.Tx, id, actor, kind string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		`INSERT INTO coding_events(task_id,actor_id,type,detail) VALUES($1,$2,$3,$4)`,
		id,
		actor,
		kind,
		b,
	)
	return e
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
