package coding

import (
	"context"
	"strconv"
)

func (a *App) checkGate(ctx context.Context, task string, b taskRunRequest) (CodingTask, error) {
	t, e := scanTask(a.DB.QueryRow(ctx, `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1`, task))
	if e != nil {
		return t, problem(404, "NOT_FOUND", "任务不存在")
	}
	level, e := a.taskLevel(ctx, t.OwnerID, t.TenantID, t.ProjectID)
	if e != nil {
		return t, e
	}
	if level < 30 || t.Control != "Agent" || t.Epoch != b.Epoch || t.ActiveRun == nil || *t.ActiveRun != b.RunID {
		return t, problem(409, "EXECUTION_REVOKED", "人工介入或授权变化，禁止继续写入")
	}
	if e := a.runtimeLease(ctx, b.Worker, b.RuntimeEpoch); e != nil {
		return t, e
	}
	var ok bool
	e = a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coding_runs WHERE id=$1 AND task_id=$2 AND runtime_id=$3 AND claimed_by=$3 AND status='Running' AND epoch=$4)`, b.RunID, t.ID, b.Worker, b.Epoch).Scan(&ok)
	if e != nil {
		return t, e
	}
	if !ok {
		return t, problem(409, "STALE_EXECUTION", "本轮执行身份失效")
	}
	return t, nil
}
func (a *App) requireProjectExecutor(ctx context.Context, tid, pid string) error {
	p, err := a.project(ctx, tid, pid)
	if err != nil {
		return err
	}
	if !a.Config.EnabledProjects[strconv.FormatInt(p.GitLabID, 10)] {
		return problem(409, "PROJECT_EXECUTOR_UNAVAILABLE", "此项目尚未启用 Coding 执行")
	}
	return nil
}
