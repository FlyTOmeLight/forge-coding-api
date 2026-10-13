package coding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func (a *App) readyTaskSettings(ctx context.Context, t table.Task) (CodingSettings, error) {
	if e := a.requireProjectExecutor(ctx, t.TenantID, t.ProjectID); e != nil {
		return CodingSettings{}, e
	}
	var c CodingSettings
	var frozen []byte
	e := a.DB.QueryRow(ctx, `SELECT config_snapshot FROM coding_runs WHERE task_id=$1 AND coalesce(config_snapshot->>'agentID','')<>'' ORDER BY created_at,id LIMIT 1`, t.ID).
		Scan(&frozen)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return c, e
	}
	if len(frozen) > 0 {
		if e = json.Unmarshal(frozen, &c); e != nil {
			return c, e
		}
	} else {
		c, e = a.codingSettings(ctx, t.TenantID, t.ProjectID)
		if e != nil {
			return c, e
		}
	}
	if e = c.validate(); e != nil {
		return c, e
	}
	if c.CheckedAt == nil {
		return c, problem(409, "PROJECT_NOT_READY", "请先由项目管理员检查仓库与 Agent 接入")
	}
	if e = a.checkAgentBinding(ctx, t.TenantID, t.ProjectID, &c); e != nil {
		return c, e
	}
	if t.BaseBranch != "" {
		c.BaseBranch = t.BaseBranch
	}
	return c, nil
}

// A task has a branch from creation, and gains an MR only after code changes exist.
func (a *App) currentTaskMR(ctx context.Context, project int64, t table.Task) (gitlab.MergeRequest, error) {
	if t.MRIID > 0 {
		mr, err := a.GitLab.MergeRequest(ctx, project, t.MRIID)
		if err != nil || mr.State != "opened" {
			return mr, err
		}
		// MR diff metadata can lag a completed push. Delivery and background sync
		// share the live source ref so sync cannot undo a just-verified delivery.
		branch, err := a.GitLab.Branch(ctx, project, mr.Source)
		if err != nil {
			return gitlab.MergeRequest{}, err
		}
		mr.Head = branch.Commit.ID
		return mr, nil
	}
	b, e := a.GitLab.Branch(ctx, project, t.Branch)
	if gitlab.IsStatus(e, 404) {
		return gitlab.MergeRequest{State: "opened", Head: t.Head, Source: t.Branch, Target: t.TargetBranch}, nil
	}
	if e != nil {
		return gitlab.MergeRequest{}, e
	}
	return gitlab.MergeRequest{State: "opened", Head: b.Commit.ID, Source: t.Branch, Target: t.TargetBranch}, nil
}

func (a *App) createProblemTask(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	if level < 30 {
		fail(gc, problem(403, "WRITE_REQUIRED", "需要项目代码写权限"))
		return
	}
	var b codingv1.CreateTaskRequest
	if e = readJSON(gc, &b); e != nil {
		fail(gc, e)
		return
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	b.Reproduce = strings.TrimSpace(b.Reproduce)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.Reproduce == "" {
		fail(gc, problem(422, "INVALID_TASK", "请填写问题、复现步骤和验收条件"))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 100 {
		fail(gc, problem(400, "IDEMPOTENCY_REQUIRED", "创建任务需要唯一请求标识"))
		return
	}
	requestKey := Hash(u.ID + ":" + r.PathValue("tid") + ":" + r.PathValue("pid") + ":" + key)
	raw, _ := json.Marshal(b)
	requestHash := Hash(string(raw))
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(gc, e)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, requestKey)
	if e != nil {
		fail(gc, e)
		return
	}
	var storedHash string
	e = tx.QueryRow(r.Context(), `SELECT request_hash FROM coding_tasks WHERE request_key=$1`, requestKey).
		Scan(&storedHash)
	if e == nil {
		if storedHash != requestHash {
			fail(gc, problem(409, "IDEMPOTENCY_CONFLICT", "请求标识已用于其他内容"))
			return
		}
		t, err := table.ScanTask(
			tx.QueryRow(
				r.Context(),
				`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE request_key=$1`,
				requestKey,
			),
		)
		if err != nil {
			fail(gc, err)
			return
		}
		gc.JSON(200, t)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		fail(gc, e)
		return
	}
	t := table.Task{ID: ID("task"), TenantID: r.PathValue("tid"), ProjectID: r.PathValue("pid"), OwnerID: u.ID}
	c, e := a.readyTaskSettings(r.Context(), t)
	if e != nil {
		fail(gc, e)
		return
	}
	if b.BaseBranch != "" {
		c.BaseBranch = b.BaseBranch
	}
	remoteID, e := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
	if e != nil {
		fail(gc, e)
		return
	}
	branch, e := a.GitLab.Branch(r.Context(), remoteID, c.BaseBranch)
	if e != nil {
		fail(gc, e)
		return
	}
	if branch.Commit.ID == "" {
		fail(gc, problem(409, "EMPTY_BASE", "基准分支没有可用提交"))
		return
	}
	if len(b.ReviewerIDs) == 0 {
		b.ReviewerIDs = []string{u.ID}
	}
	if len(b.ReviewerIDs) > 10 {
		fail(gc, problem(422, "INVALID_REVIEWERS", "最多选择 10 位指定审查者"))
		return
	}
	for _, id := range b.ReviewerIDs {
		n, err := a.taskLevel(r.Context(), id, t.TenantID, t.ProjectID)
		if err != nil {
			fail(gc, err)
			return
		}
		if n < 20 {
			fail(gc, problem(422, "INVALID_REVIEWER", "指定审查者需要有效项目读取权限"))
			return
		}
	}
	configRaw, _ := json.Marshal(c)
	run := ID("run")
	t.Branch = "agent/" + strings.TrimPrefix(t.ID, "task_")
	t, e = table.ScanTask(
		tx.QueryRow(
			r.Context(),
			`INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,reproduce,criteria,constraints,reference,base_branch,branch,target_branch,mr_iid,head,reviewer_ids,synthetic,status,control,round,active_run,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$11,0,$13,$14,$15,'Queued','Agent',1,$16,$17,$18) RETURNING `+table.TaskColumns,
			t.ID,
			t.TenantID,
			t.ProjectID,
			u.ID,
			b.Title,
			b.Description,
			b.Reproduce,
			b.Criteria,
			b.Constraints,
			b.Reference,
			c.BaseBranch,
			t.Branch,
			branch.Commit.ID,
			b.ReviewerIDs,
			b.Synthetic,
			run,
			requestKey,
			requestHash,
		),
	)
	if e != nil {
		fail(gc, e)
		return
	}
	requirements := b.Description + "\n复现：" + b.Reproduce + "\n验收：" + b.Criteria + "\n约束：" + b.Constraints
	_, e = tx.Exec(
		r.Context(),
		`INSERT INTO coding_runs(id,task_id,epoch,head,requirements,config_snapshot) VALUES($1,$2,$3,$4,$5,$6)`,
		run,
		t.ID,
		t.Epoch,
		t.Head,
		requirements,
		configRaw,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	if e = table.TaskEvent(
		r.Context(),
		tx,
		t.ID,
		u.ID,
		"TaskCreated",
		Object{"head": t.Head, "branch": t.Branch, "configVersion": c.Version},
	); e != nil {
		fail(gc, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(gc, e)
		return
	}
	gc.JSON(201, t)
}

// Readiness follows the reviewed task revision; a new execution must return to draft.
func (a *App) setTaskMRDraft(
	ctx context.Context,
	project int64,
	t table.Task,
	mr gitlab.MergeRequest,
	draft bool,
) error {
	matches := func(m gitlab.MergeRequest) bool {
		return m.IID == t.MRIID && m.State == "opened" && m.Head == t.Head && m.Source == t.Branch &&
			m.Target == t.TargetBranch
	}
	if !matches(mr) {
		return problem(409, "MR_CHANGED", "GitLab MR 状态或代码已变化，请先同步后重新审查")
	}
	if mr.Draft == draft {
		return nil
	}
	updated, err := a.GitLab.SetMergeRequestDraft(ctx, project, mr, draft)
	if err != nil {
		return problem(502, "GITLAB_DRAFT_UPDATE_FAILED", "GitLab 草稿状态更新失败，请稍后重试")
	}
	if !matches(updated) {
		return problem(409, "MR_CHANGED", "GitLab MR 状态或代码已变化，请先同步后重新审查")
	}
	if updated.Draft != draft {
		return problem(502, "GITLAB_DRAFT_UPDATE_FAILED", "GitLab 未确认草稿状态更新，请稍后重试")
	}
	return nil
}
