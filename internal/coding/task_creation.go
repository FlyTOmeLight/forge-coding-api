package coding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/FlyTOmeLight/forge-coding-api/internal/remote"
	"github.com/jackc/pgx/v5"
)

func (a *App) readyTaskSettings(ctx context.Context, t CodingTask) (CodingSettings, error) {
	if _, e := a.toolEndpoint(ctx, t.TenantID, t.ProjectID); e != nil {
		return CodingSettings{}, e
	}
	c, e := a.codingSettings(ctx, t.TenantID, t.ProjectID)
	if e != nil {
		return c, e
	}
	if e = c.validate(); e != nil {
		return c, e
	}
	if c.CheckedAt == nil {
		return c, problem(409, "PROJECT_NOT_READY", "请先由项目管理员检查仓库与模型接入")
	}
	if t.BaseBranch != "" {
		c.BaseBranch = t.BaseBranch
	}
	return c, nil
}

// A task has a branch from creation, and gains an MR only after code changes exist.
func (a *App) currentTaskMR(ctx context.Context, project int64, t CodingTask) (remote.MergeRequest, error) {
	if t.MRIID > 0 {
		return a.GitLab.MergeRequest(ctx, project, t.MRIID)
	}
	b, e := a.GitLab.Branch(ctx, project, t.Branch)
	if remote.IsStatus(e, 404) {
		return remote.MergeRequest{State: "opened", Head: t.Head, Source: t.Branch, Target: t.TargetBranch}, nil
	}
	if e != nil {
		return remote.MergeRequest{}, e
	}
	return remote.MergeRequest{State: "opened", Head: b.Commit.ID, Source: t.Branch, Target: t.TargetBranch}, nil
}
func (a *App) createProblemTask(w http.ResponseWriter, r *http.Request, u User) {
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(w, e)
		return
	}
	if level < 30 {
		fail(w, problem(403, "WRITE_REQUIRED", "需要项目代码写权限"))
		return
	}
	var b struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Reproduce   string   `json:"reproduce"`
		Criteria    string   `json:"criteria"`
		Constraints string   `json:"constraints"`
		Reference   string   `json:"reference"`
		BaseBranch  string   `json:"baseBranch"`
		ReviewerIDs []string `json:"reviewerIds"`
		Synthetic   bool     `json:"synthetic"`
	}
	if e = readJSON(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	b.Reproduce = strings.TrimSpace(b.Reproduce)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.Reproduce == "" {
		fail(w, problem(422, "INVALID_TASK", "请填写问题、复现步骤和验收条件"))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 100 {
		fail(w, problem(400, "IDEMPOTENCY_REQUIRED", "创建任务需要唯一请求标识"))
		return
	}
	requestKey := Hash(u.ID + ":" + r.PathValue("tid") + ":" + r.PathValue("pid") + ":" + key)
	raw, _ := json.Marshal(b)
	requestHash := Hash(string(raw))
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, requestKey)
	if e != nil {
		fail(w, e)
		return
	}
	var storedHash string
	e = tx.QueryRow(r.Context(), `SELECT request_hash FROM coding_tasks WHERE request_key=$1`, requestKey).Scan(&storedHash)
	if e == nil {
		if storedHash != requestHash {
			fail(w, problem(409, "IDEMPOTENCY_CONFLICT", "请求标识已用于其他内容"))
			return
		}
		t, err := scanTask(tx.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE request_key=$1`, requestKey))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, t)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		fail(w, e)
		return
	}
	t := CodingTask{ID: ID("task"), TenantID: r.PathValue("tid"), ProjectID: r.PathValue("pid"), OwnerID: u.ID}
	c, e := a.readyTaskSettings(r.Context(), t)
	if e != nil {
		fail(w, e)
		return
	}
	if b.BaseBranch != "" {
		c.BaseBranch = b.BaseBranch
	}
	remoteID, e := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
	if e != nil {
		fail(w, e)
		return
	}
	branch, e := a.GitLab.Branch(r.Context(), remoteID, c.BaseBranch)
	if e != nil {
		fail(w, e)
		return
	}
	if branch.Commit.ID == "" {
		fail(w, problem(409, "EMPTY_BASE", "基准分支没有可用提交"))
		return
	}
	if len(b.ReviewerIDs) == 0 {
		b.ReviewerIDs = []string{u.ID}
	}
	if len(b.ReviewerIDs) > 10 {
		fail(w, problem(422, "INVALID_REVIEWERS", "最多选择 10 位指定审查者"))
		return
	}
	for _, id := range b.ReviewerIDs {
		n, err := a.taskLevel(r.Context(), id, t.TenantID, t.ProjectID)
		if err != nil {
			fail(w, err)
			return
		}
		if n < 20 {
			fail(w, problem(422, "INVALID_REVIEWER", "指定审查者需要有效项目读取权限"))
			return
		}
	}
	configRaw, _ := json.Marshal(c)
	run := ID("run")
	t.Branch = "agent/" + strings.TrimPrefix(t.ID, "task_")
	t, e = scanTask(tx.QueryRow(r.Context(), `INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,reproduce,criteria,constraints,reference,base_branch,branch,target_branch,mr_iid,head,reviewer_ids,synthetic,status,control,round,active_run,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$11,0,$13,$14,$15,'Queued','Agent',1,$16,$17,$18) RETURNING `+taskColumns, t.ID, t.TenantID, t.ProjectID, u.ID, b.Title, b.Description, b.Reproduce, b.Criteria, b.Constraints, b.Reference, c.BaseBranch, t.Branch, branch.Commit.ID, b.ReviewerIDs, b.Synthetic, run, requestKey, requestHash))
	if e != nil {
		fail(w, e)
		return
	}
	requirements := b.Description + "\n复现：" + b.Reproduce + "\n验收：" + b.Criteria + "\n约束：" + b.Constraints
	_, e = tx.Exec(r.Context(), `INSERT INTO coding_runs(id,task_id,epoch,head,requirements,config_snapshot) VALUES($1,$2,$3,$4,$5,$6)`, run, t.ID, t.Epoch, t.Head, requirements, configRaw)
	if e != nil {
		fail(w, e)
		return
	}
	if e = taskEvent(r.Context(), tx, t.ID, u.ID, "TaskCreated", Object{"head": t.Head, "branch": t.Branch, "configVersion": c.Version}); e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	JSON(w, 201, t)
}
