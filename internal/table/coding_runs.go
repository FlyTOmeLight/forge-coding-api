package table

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func LockRunStatus(ctx context.Context, db DB, runID string) (status string, err error) {
	err = db.QueryRow(ctx, `SELECT status FROM coding_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&status)
	return
}

func StopRun(ctx context.Context, db DB, runID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_runs SET status='Stopped',finished_at=now() WHERE id=$1`, runID)
}

func RequestRunStop(ctx context.Context, db DB, runID string) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET status='Stopping' WHERE id=$1 AND status IN ('Queued','Running','Delivering')`,
		runID,
	)
}

func CreateRun(
	ctx context.Context,
	db DB,
	runID, taskID string,
	epoch int64,
	head, requirements string,
	config []byte,
) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`INSERT INTO coding_runs(id,task_id,epoch,head,requirements,config_snapshot) VALUES($1,$2,$3,$4,$5,$6)`,
		runID,
		taskID,
		epoch,
		head,
		requirements,
		config,
	)
}

func LockDeliveryRun(ctx context.Context, db DB, runID, taskID string) (RunClaim, error) {
	var row RunClaim
	err := db.QueryRow(ctx, `SELECT claimed_by,status,epoch FROM coding_runs WHERE id=$1 AND task_id=$2 FOR UPDATE`, runID, taskID).
		Scan(&row.Worker, &row.Status, &row.Epoch)
	return row, err
}

func RunDeliveryReady(ctx context.Context, db DB, runID, runtimeID string) (ready bool, err error) {
	err = db.QueryRow(ctx, `SELECT status='Delivering' AND output ? 'finishRequest' FROM coding_runs WHERE id=$1 AND runtime_id=$2`, runID, runtimeID).
		Scan(&ready)
	return
}

func FinishRun(ctx context.Context, db DB, runID, status string, output []byte) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET status=$2,output=CASE WHEN $2='Stopped' THEN output || $3::jsonb ELSE $3::jsonb END,finished_at=now(),lease_until=NULL WHERE id=$1`,
		runID,
		status,
		output,
	)
}

func TaskFrozenSettings(ctx context.Context, db DB, taskID string) (settings []byte, err error) {
	err = db.QueryRow(ctx, `SELECT config_snapshot FROM coding_runs WHERE task_id=$1 AND coalesce(config_snapshot->>'agentID','')<>'' ORDER BY created_at,id LIMIT 1`, taskID).
		Scan(&settings)
	return
}

func ListActiveRunIDs(ctx context.Context, db DB) ([]string, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id FROM coding_runs WHERE status IN ('Queued','Running','Stopping','Delivering') AND (status='Queued' OR runtime_id IS NOT NULL OR execution_snapshot IS NOT NULL OR (status='Stopping' AND claimed_by='')) ORDER BY created_at,id LIMIT 100`,
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

func RunTaskID(ctx context.Context, db DB, runID string) (taskID string, err error) {
	err = db.QueryRow(ctx, `SELECT task_id FROM coding_runs WHERE id=$1`, runID).Scan(&taskID)
	return
}

func LockRunExecution(ctx context.Context, db DB, runID string) (RunExecution, error) {
	var row RunExecution
	err := db.QueryRow(ctx, `SELECT runtime_id,status,requirements,config_snapshot,execution_snapshot FROM coding_runs WHERE id=$1 FOR UPDATE`, runID).
		Scan(&row.RuntimeID, &row.Status, &row.Requirements, &row.ConfigSnapshot, &row.ExecutionSnapshot)
	return row, err
}

func MarkRunStopping(ctx context.Context, db DB, runID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, runID)
}

func RunOutput(ctx context.Context, db DB, runID string) (output []byte, err error) {
	err = db.QueryRow(ctx, `SELECT output FROM coding_runs WHERE id=$1`, runID).Scan(&output)
	return
}

func SaveRunBaseline(ctx context.Context, db DB, runID string, baseline []byte) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET output=output || jsonb_build_object('baseline',$2::jsonb) WHERE id=$1`,
		runID,
		baseline,
	)
}

func SaveExecutionSnapshot(ctx context.Context, db DB, runID string, snapshot []byte) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_runs SET execution_snapshot=$2 WHERE id=$1`, runID, snapshot)
}

func StartRun(ctx context.Context, db DB, runID, runtimeID string) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET runtime_id=$2,claimed_by=$2,status='Running' WHERE id=$1`,
		runID,
		runtimeID,
	)
}

func RunEpoch(ctx context.Context, db DB, runID string) (epoch int64, err error) {
	err = db.QueryRow(ctx, `SELECT epoch FROM coding_runs WHERE id=$1`, runID).Scan(&epoch)
	return
}

func RunHasFinish(ctx context.Context, db DB, runID string) (hasFinish bool, err error) {
	err = db.QueryRow(ctx, `SELECT output ? 'finishRequest' FROM coding_runs WHERE id=$1`, runID).Scan(&hasFinish)
	return
}

func MarkRunDelivering(ctx context.Context, db DB, runID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_runs SET status='Delivering' WHERE id=$1`, runID)
}

func PublicationIdentity(ctx context.Context, db DB, runID string) (RunIdentity, error) {
	var row RunIdentity
	err := db.QueryRow(ctx, `SELECT task_id,epoch FROM coding_runs WHERE id=$1 AND status='Delivering'`, runID).
		Scan(&row.TaskID, &row.Epoch)
	return row, err
}

func RunRuntimeIdentity(ctx context.Context, db DB, runID string) (RuntimeIdentity, error) {
	var row RuntimeIdentity
	err := db.QueryRow(ctx, `SELECT task_id,runtime_id,epoch FROM coding_runs WHERE id=$1`, runID).
		Scan(&row.TaskID, &row.RuntimeID, &row.Epoch)
	return row, err
}

func DeliveryRun(ctx context.Context, db DB, runID string) (DeliveryRecord, error) {
	var row DeliveryRecord
	err := db.QueryRow(ctx, `SELECT task_id,runtime_id,epoch,config_snapshot,output,created_at FROM coding_runs WHERE id=$1 AND status='Delivering'`, runID).
		Scan(&row.TaskID, &row.RuntimeID, &row.Epoch, &row.ConfigSnapshot, &row.Output, &row.CreatedAt)
	return row, err
}

func SaveDeliveryEvidence(ctx context.Context, db DB, runID, key string, evidence []byte) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET output=output || jsonb_build_object($2::text,$3::jsonb) WHERE id=$1 AND status='Delivering'`,
		runID,
		key,
		evidence,
	)
}

func RunningIdentity(ctx context.Context, db DB, runID, runtimeID string) (RunIdentity, error) {
	var row RunIdentity
	err := db.QueryRow(ctx, `SELECT task_id,epoch FROM coding_runs WHERE id=$1 AND runtime_id=$2 AND status='Running'`, runID, runtimeID).
		Scan(&row.TaskID, &row.Epoch)
	return row, err
}

func ClaimRunningRun(ctx context.Context, db DB, runID, runtimeID string) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET claimed_by=$2 WHERE id=$1 AND runtime_id=$2 AND status='Running'`,
		runID,
		runtimeID,
	)
}

func SubmitRunFinish(
	ctx context.Context,
	db DB,
	runID string,
	request []byte,
	operation string,
) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET output=output || jsonb_build_object('finishRequest',$2::jsonb,'finishOperation',$3::text) WHERE id=$1 AND status='Running' AND (NOT output ? 'finishOperation' OR (output->>'finishOperation'=$3 AND output->'finishRequest'=$2::jsonb))`,
		runID,
		request,
		operation,
	)
}

func RunSettings(ctx context.Context, db DB, runID string) (settings []byte, err error) {
	err = db.QueryRow(ctx, `SELECT config_snapshot FROM coding_runs WHERE id=$1`, runID).Scan(&settings)
	return
}

func CountCheckAttempt(ctx context.Context, db DB, runID string, limit int) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_runs SET output=jsonb_set(output,'{checkAttempts}',to_jsonb(coalesce((output->>'checkAttempts')::int,0)+1)) WHERE id=$1 AND status='Running' AND coalesce((output->>'checkAttempts')::int,0)<$2`,
		runID,
		limit,
	)
}

func IsExpectedPush(ctx context.Context, db DB, runID string, epoch int64, head string) (expected bool, err error) {
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coding_runs WHERE id=$1 AND epoch=$2 AND status IN ('Running','Delivering') AND output->>'expectedHead'=$3)`, runID, epoch, head).
		Scan(&expected)
	return
}

func RunLeaseMatches(
	ctx context.Context,
	db DB,
	runID, taskID, runtimeID string,
	epoch int64,
) (matches bool, err error) {
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coding_runs WHERE id=$1 AND task_id=$2 AND runtime_id=$3 AND claimed_by=$3 AND status='Running' AND epoch=$4)`, runID, taskID, runtimeID, epoch).
		Scan(&matches)
	return
}

func ScopedRuntimeID(
	ctx context.Context,
	db DB,
	runID, taskID, tenantID, projectID string,
) (runtimeID *string, err error) {
	err = db.QueryRow(ctx, `SELECT r.runtime_id FROM coding_runs r JOIN coding_tasks t ON t.id=r.task_id WHERE r.id=$1 AND t.id=$2 AND t.tenant_id=$3 AND t.project_id=$4`, runID, taskID, tenantID, projectID).
		Scan(&runtimeID)
	return
}

type RunClaim struct {
	Worker string
	Status string
	Epoch  int64
}

type RunExecution struct {
	RuntimeID         *string
	Status            string
	Requirements      string
	ConfigSnapshot    []byte
	ExecutionSnapshot []byte
}

type RunIdentity struct {
	TaskID string
	Epoch  int64
}

type RuntimeIdentity struct {
	TaskID    string
	RuntimeID string
	Epoch     int64
}

type DeliveryRecord struct {
	TaskID         string
	RuntimeID      string
	Epoch          int64
	ConfigSnapshot []byte
	Output         []byte
	CreatedAt      time.Time
}

func TaskRuns(ctx context.Context, db DB, taskID string) ([]map[string]any, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id,runtime_id,epoch,head,status,requirements,output,config_snapshot,created_at,finished_at FROM coding_runs WHERE task_id=$1 ORDER BY created_at DESC,id DESC LIMIT 30`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil {
		items = []map[string]any{}
	}
	return items, err
}
