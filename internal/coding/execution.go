package coding

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (a *App) startRound(w http.ResponseWriter, r *http.Request) {
	var b taskRunRequest
	if e := readJSON(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	if e := a.runtimeLease(r.Context(), b.Worker, b.RuntimeEpoch); e != nil {
		fail(w, e)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var taskID, requirements string
	var epoch int64
	var snapshot json.RawMessage
	e = tx.QueryRow(r.Context(), `SELECT task_id,epoch,requirements,config_snapshot FROM coding_runs WHERE id=$1 AND runtime_id=$2 AND status='Running'`, r.PathValue("run"), b.Worker).Scan(&taskID, &epoch, &requirements, &snapshot)
	if e != nil {
		fail(w, problem(409, "STALE_EXECUTION", "本轮执行已变化"))
		return
	}
	t, e := scanTask(tx.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, taskID))
	if e != nil {
		fail(w, e)
		return
	}
	level, e := a.taskLevel(r.Context(), t.OwnerID, t.TenantID, t.ProjectID)
	if e != nil {
		fail(w, e)
		return
	}
	if level < 30 || t.Control != "Agent" || t.Epoch != epoch || t.ActiveRun == nil || *t.ActiveRun != r.PathValue("run") {
		fail(w, problem(409, "EXECUTION_REVOKED", "本轮自动写入已被禁止"))
		return
	}
	if _, e = tx.Exec(r.Context(), `UPDATE coding_runs SET claimed_by=$2 WHERE id=$1 AND runtime_id=$2`, r.PathValue("run"), b.Worker); e != nil {
		fail(w, e)
		return
	}
	project, e := a.project(r.Context(), t.TenantID, t.ProjectID)
	if e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	JSON(w, 200, Object{"id": r.PathValue("run"), "epoch": epoch, "task": t, "requirements": requirements, "config": snapshot, "gitlabProjectId": project.GitLabID, "projectPath": project.Path})
}
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
func (a *App) taskGate(w http.ResponseWriter, r *http.Request) {
	var b taskRunRequest
	if e := readJSON(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	t, e := a.checkGate(r.Context(), r.PathValue("task"), b)
	if e != nil {
		fail(w, e)
		return
	}
	if b.Head != "" {
		if len(b.Head) != 40 && len(b.Head) != 64 {
			fail(w, problem(400, "INVALID_COMMIT", "交付版本格式无效"))
			return
		}
		tag, e := a.DB.Exec(r.Context(), `UPDATE coding_runs r SET output=jsonb_build_object('expectedHead',$5::text) FROM coding_tasks t WHERE r.id=$1 AND r.task_id=$2 AND r.epoch=$3 AND r.claimed_by=$4 AND r.status='Running' AND t.id=r.task_id AND t.control='Agent' AND t.epoch=r.epoch`, b.RunID, t.ID, b.Epoch, b.Worker, b.Head)
		if e != nil {
			fail(w, e)
			return
		}
		if tag.RowsAffected() != 1 {
			fail(w, problem(409, "EXECUTION_REVOKED", "自动写入已禁止"))
			return
		}
	}
	JSON(w, 200, Object{"allowed": true, "head": t.Head, "checkedAt": time.Now().UTC()})
}

func (a *App) toolEndpoint(ctx context.Context, tid, pid string) (string, error) {
	p, e := a.project(ctx, tid, pid)
	if e != nil {
		return "", e
	}
	endpoint := a.Config.ToolProjects[strconv.FormatInt(p.GitLabID, 10)]
	if endpoint == "" {
		return "", problem(409, "PROJECT_EXECUTOR_UNAVAILABLE", "此项目尚未配置独立的 Coding 工具环境")
	}
	return endpoint, nil
}
