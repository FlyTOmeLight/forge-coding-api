package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/codingid"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func (a *Service) readyTaskSettings(ctx context.Context, t table.Task) (project.Settings, error) {
	if e := a.Projects.RequireExecutor(ctx, t.TenantID, t.ProjectID); e != nil {
		return project.Settings{}, e
	}
	var c project.Settings
	var frozen []byte
	frozen, e := table.TaskFrozenSettings(ctx, a.DB, t.ID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return c, e
	}
	if len(frozen) > 0 {
		if e = json.Unmarshal(frozen, &c); e != nil {
			return c, e
		}
	} else {
		c, e = a.Projects.Settings(ctx, t.TenantID, t.ProjectID)
		if e != nil {
			return c, e
		}
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	if c.CheckedAt == nil {
		return c, serrors.NewError(409, "PROJECT_NOT_READY", "请先由项目管理员检查仓库与 Agent 接入")
	}
	if e = a.Projects.CheckAgentBinding(ctx, t.TenantID, t.ProjectID, &c); e != nil {
		return c, e
	}
	if t.BaseBranch != "" {
		c.BaseBranch = t.BaseBranch
	}
	return c, nil
}

// A task has a branch from creation, and gains an MR only after code changes exist.
func (a *Service) currentTaskMR(ctx context.Context, project int64, t table.Task) (gitlab.MergeRequest, error) {
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

func (a *Service) createProblemTask(
	gc *core.Context,
	u access.User,
	b codingv1.CreateTaskRequest,
) (table.Task, bool, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return table.Task{}, false, e
	}
	if level < 30 {
		return table.Task{}, false, serrors.NewError(403, "WRITE_REQUIRED", "需要项目代码写权限")
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	b.Reproduce = strings.TrimSpace(b.Reproduce)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.Reproduce == "" {
		return table.Task{}, false, serrors.NewError(422, "INVALID_TASK", "请填写问题、复现步骤和验收条件")
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 100 {
		return table.Task{}, false, serrors.NewError(400, "IDEMPOTENCY_REQUIRED", "创建任务需要唯一请求标识")
	}
	requestKey := codingid.Hash(u.ID + ":" + r.PathValue("tid") + ":" + r.PathValue("pid") + ":" + key)
	raw, _ := json.Marshal(b)
	requestHash := codingid.Hash(string(raw))
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return table.Task{}, false, e
	}
	defer tx.Rollback(r.Context())
	_, e = table.LockTaskRequest(r.Context(), tx, requestKey)
	if e != nil {
		return table.Task{}, false, e
	}
	var storedHash string
	storedHash, e = table.TaskRequestHash(r.Context(), tx, requestKey)
	if e == nil {
		if storedHash != requestHash {
			return table.Task{}, false, serrors.NewError(409, "IDEMPOTENCY_CONFLICT", "请求标识已用于其他内容")
		}
		t, err := table.FindTaskByRequest(r.Context(), tx, requestKey)
		if err != nil {
			return table.Task{}, false, err
		}
		return t, false, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return table.Task{}, false, e
	}
	t := table.Task{
		ID:        codingid.ID("task"),
		TenantID:  r.PathValue("tid"),
		ProjectID: r.PathValue("pid"),
		OwnerID:   u.ID,
	}
	c, e := a.readyTaskSettings(r.Context(), t)
	if e != nil {
		return table.Task{}, false, e
	}
	if b.BaseBranch != "" {
		c.BaseBranch = b.BaseBranch
	}
	remoteID, e := a.Projects.ProjectID(r.Context(), t.TenantID, t.ProjectID)
	if e != nil {
		return table.Task{}, false, e
	}
	branch, e := a.GitLab.Branch(r.Context(), remoteID, c.BaseBranch)
	if e != nil {
		return table.Task{}, false, e
	}
	if branch.Commit.ID == "" {
		return table.Task{}, false, serrors.NewError(409, "EMPTY_BASE", "基准分支没有可用提交")
	}
	if len(b.ReviewerIDs) == 0 {
		b.ReviewerIDs = []string{u.ID}
	}
	if len(b.ReviewerIDs) > 10 {
		return table.Task{}, false, serrors.NewError(422, "INVALID_REVIEWERS", "最多选择 10 位指定审查者")
	}
	for _, id := range b.ReviewerIDs {
		n, err := a.Permissions.Level(r.Context(), id, t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, false, err
		}
		if n < 20 {
			return table.Task{}, false, serrors.NewError(422, "INVALID_REVIEWER", "指定审查者需要有效项目读取权限")
		}
	}
	configRaw, _ := json.Marshal(c)
	run := codingid.ID("run")
	t.Branch = "agent/" + strings.TrimPrefix(t.ID, "task_")
	t, e = table.CreateProblemTask(
		r.Context(),
		tx,
		table.Task{
			ID:          t.ID,
			TenantID:    t.TenantID,
			ProjectID:   t.ProjectID,
			OwnerID:     u.ID,
			Title:       b.Title,
			Description: b.Description,
			Reproduce:   b.Reproduce,
			Criteria:    b.Criteria,
			Constraints: b.Constraints,
			Reference:   b.Reference,
			BaseBranch:  c.BaseBranch,
			Branch:      t.Branch,
			Head:        branch.Commit.ID,
			ReviewerIDs: b.ReviewerIDs,
			Synthetic:   b.Synthetic,
		},
		run,
		requestKey,
		requestHash,
	)
	if e != nil {
		return table.Task{}, false, e
	}
	requirements := b.Description + "\n复现：" + b.Reproduce + "\n验收：" + b.Criteria + "\n约束：" + b.Constraints
	_, e = table.CreateRun(r.Context(), tx, run, t.ID, t.Epoch, t.Head, requirements, configRaw)
	if e != nil {
		return table.Task{}, false, e
	}
	if e = table.TaskEvent(
		r.Context(),
		tx,
		t.ID,
		u.ID,
		"TaskCreated",
		map[string]any{"head": t.Head, "branch": t.Branch, "configVersion": c.Version},
	); e != nil {
		return table.Task{}, false, e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return table.Task{}, false, e
	}
	return t, true, nil
}

// Readiness follows the reviewed task revision; a new execution must return to draft.
func (a *Service) setTaskMRDraft(
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
		return serrors.NewError(409, "MR_CHANGED", "GitLab MR 状态或代码已变化，请先同步后重新审查")
	}
	if mr.Draft == draft {
		return nil
	}
	updated, err := a.GitLab.SetMergeRequestDraft(ctx, project, mr, draft)
	if err != nil {
		return serrors.NewError(502, "GITLAB_DRAFT_UPDATE_FAILED", "GitLab 草稿状态更新失败，请稍后重试")
	}
	if !matches(updated) {
		return serrors.NewError(409, "MR_CHANGED", "GitLab MR 状态或代码已变化，请先同步后重新审查")
	}
	if updated.Draft != draft {
		return serrors.NewError(502, "GITLAB_DRAFT_UPDATE_FAILED", "GitLab 未确认草稿状态更新，请稍后重试")
	}
	return nil
}
