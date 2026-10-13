package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/codingid"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func (a *Service) listTasks(gc *core.Context, u access.User) ([]table.Task, error) {
	r := gc.Request
	if _, e := a.Permissions.Read(r, u); e != nil {
		return nil, e
	}
	items, e := table.ListTasks(r.Context(), a.DB, r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return nil, e
	}
	return items, nil
}

func (a *Service) getTask(gc *core.Context, u access.User) (map[string]any, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return nil, e
	}
	t, e := table.FindScopedTask(r.Context(), a.DB, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid"))
	if errors.Is(e, pgx.ErrNoRows) {
		e = serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	if e != nil {
		return nil, e
	}
	events, e := table.TaskEvents(r.Context(), a.DB, t.ID)
	if e != nil {
		return nil, e
	}
	runs, e := table.TaskRuns(r.Context(), a.DB, t.ID)
	if e != nil {
		return nil, e
	}
	feedback, e := table.TaskFeedback(r.Context(), a.DB, t.ID)
	if e != nil {
		return nil, e
	}
	project, e := a.Permissions.Resolve(r.Context(), t.TenantID, t.ProjectID)
	projectPath := project.Path
	if e != nil {
		return nil, e
	}
	gitlabURL := strings.TrimRight(a.Config.GitLabPublicURL, "/") + "/" + projectPath
	gitlabTarget := "project"
	if t.MRIID > 0 {
		gitlabURL += "/-/merge_requests/" + strconv.FormatInt(t.MRIID, 10)
		gitlabTarget = "mergeRequest"
	} else {
		// A task reserves a branch name before the executor has pushed any code.
		// Link to the branch only after GitLab confirms it exists.
		_, err := a.GitLab.Branch(r.Context(), project.GitLabID, t.Branch)
		if err == nil {
			gitlabURL += "/-/tree/" + url.PathEscape(t.Branch)
			gitlabTarget = "branch"
		} else if !gitlab.IsStatus(err, http.StatusNotFound) {
			return nil, serrors.NewError(502, "GITLAB_BRANCH_UNAVAILABLE", "无法确认 GitLab 工作分支，请稍后重试")
		}
	}
	return map[string]any{
		"feedback":     feedback,
		"gitlabUrl":    gitlabURL,
		"gitlabTarget": gitlabTarget,
		"task":         t,
		"events":       events,
		"runs":         runs,
		"permissions": map[string]any{
			"write":  level >= 30,
			"manage": level >= 40 || (level >= 30 && t.OwnerID == u.ID),
			"review": level >= 20 && slices.Contains(t.ReviewerIDs, u.ID),
		},
	}, nil
}

func (a *Service) createTask(gc *core.Context, u access.User, b codingv1.AssociateTaskRequest) (table.Task, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return table.Task{}, e
	}
	if level < 30 {
		return table.Task{}, serrors.NewError(403, "WRITE_REQUIRED", "需要项目代码写权限")
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.MRIID <= 0 {
		return table.Task{}, serrors.NewError(400, "INVALID_TASK", "请填写任务、复现和验收条件，并选择待处理 MR")
	}
	remoteID, e := a.Projects.ProjectID(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return table.Task{}, e
	}
	mr, e := a.GitLab.MergeRequest(r.Context(), remoteID, b.MRIID)
	if e != nil {
		return table.Task{}, e
	}
	if mr.State != "opened" || mr.Head == "" {
		return table.Task{}, serrors.NewError(409, "MR_NOT_OPEN", "仅可关联尚未合入的开放 MR")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return table.Task{}, e
	}
	defer tx.Rollback(r.Context())
	t, e := table.AssociateTask(
		r.Context(),
		tx,
		table.Task{
			ID:           codingid.ID("task"),
			TenantID:     r.PathValue("tid"),
			ProjectID:    r.PathValue("pid"),
			OwnerID:      u.ID,
			Title:        b.Title,
			Description:  b.Description,
			Criteria:     b.Criteria,
			Branch:       mr.Source,
			TargetBranch: mr.Target,
			MRIID:        mr.IID,
			Head:         mr.Head,
			ReviewerIDs:  []string{u.ID},
			Synthetic:    b.Synthetic,
		},
	)
	if e != nil {
		return table.Task{}, e
	}
	if e = table.TaskEvent(
		r.Context(),
		tx,
		t.ID,
		u.ID,
		"TaskAssociated",
		map[string]any{"head": t.Head, "mrIid": t.MRIID},
	); e != nil {
		return table.Task{}, e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return table.Task{}, e
	}
	return t, nil
}

// taskAction serializes control transitions. Versioned writes reject stale clients.
func (a *Service) taskAction(gc *core.Context, u access.User, b codingv1.TaskActionRequest) (table.Task, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return table.Task{}, e
	}
	if level < 20 {
		return table.Task{}, serrors.NewError(403, "READ_REQUIRED", "需要项目读取权限")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return table.Task{}, e
	}
	defer tx.Rollback(r.Context())
	t, e := table.LockScopedTask(r.Context(), tx, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return table.Task{}, serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	if t.Version != b.Version {
		return table.Task{}, serrors.NewError(412, "STALE_VERSION", "任务已变化，请刷新后操作")
	}
	if level < 30 && b.Action != "Approve" && b.Action != "Feedback" && b.Action != "ReconfirmFeedback" {
		return table.Task{}, serrors.NewError(403, "WRITE_REQUIRED", "需要项目代码写权限")
	}
	manage := t.OwnerID == u.ID || level >= 40
	review := slices.Contains(t.ReviewerIDs, u.ID)
	terminal := t.Status == "Completed" || t.Status == "Canceled" || t.Status == "Undelivered"
	if terminal && b.Action != "Transfer" && b.Action != "Continue" {
		return table.Task{}, serrors.NewError(409, "TASK_ENDED", "任务已结束")
	}
	detail := map[string]any{"head": t.Head, "text": strings.TrimSpace(b.Text)}
	switch b.Action {
	case "Intervene", "Cancel":
		if b.Action == "Cancel" && !manage {
			return table.Task{}, serrors.NewError(403, "OWNER_REQUIRED", "需要负责人或项目管理员")
		}
		t.Epoch++
		t.ApprovalHead = ""
		t.StopConfirmed = t.ActiveRun == nil
		if t.ActiveRun != nil {
			var pending string
			if pending, e = table.LockRunStatus(r.Context(), tx, *t.ActiveRun); e != nil {
				return table.Task{}, e
			}
			if pending == "Queued" {
				if e = a.Workspaces.StopAll(r.Context(), t.ID, *t.ActiveRun); e != nil {
					return table.Task{}, e
				}
				if _, e = table.StopRun(r.Context(), tx, *t.ActiveRun); e != nil {
					return table.Task{}, e
				}
				t.ActiveRun = nil
				t.StopConfirmed = true
			}
		}
		if t.ActiveRun == nil && a.Workspaces != nil {
			if e = a.Workspaces.Stop(r.Context(), t.ID); e != nil {
				return table.Task{}, e
			}
			if _, e = table.DeactivateTaskSandbox(r.Context(), tx, t.ID); e != nil {
				return table.Task{}, e
			}
		}
		t.Control = "Human"
		if t.ActiveRun != nil {
			t.Control = "Stopping"
			now := time.Now().UTC()
			t.StopRequestedAt = &now
			_, e = table.RequestRunStop(r.Context(), tx, *t.ActiveRun)
		}
		if b.Action == "Intervene" && t.ActiveRun == nil && t.Status == "Queued" {
			t.Status = "Review"
		}
		if b.Action == "Cancel" {
			t.Status = "Canceled"
			if !t.StopConfirmed {
				t.Status = "Canceling"
			}
		}
	case "Transfer":
		if !manage {
			return table.Task{}, serrors.NewError(403, "OWNER_REQUIRED", "需要负责人或项目管理员")
		}
		n, err := a.Permissions.Level(r.Context(), b.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		if n < 30 {
			return table.Task{}, serrors.NewError(400, "INVALID_OWNER", "新负责人需要有效项目写权限")
		}
		t.OwnerID = b.OwnerID
		detail["ownerId"] = b.OwnerID
	case "Approve":
		if !review {
			return table.Task{}, serrors.NewError(403, "REVIEWER_REQUIRED", "仅指定审查者可提交审查结果")
		}
		if b.Head != t.Head {
			return table.Task{}, serrors.NewError(409, "STALE_COMMIT", "审查认可的代码版本已失效")
		}
		if t.ActiveRun != nil || !t.StopConfirmed {
			return table.Task{}, serrors.NewError(409, "STOP_UNCONFIRMED", "请等待当前执行结束后再认可代码版本")
		}
		if t.MRIID == 0 {
			return table.Task{}, serrors.NewError(409, "MR_REQUIRED", "尚未生成 MR，不能认可交付版本")
		}
		remoteID, err := a.Projects.ProjectID(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		mr, err := a.GitLab.MergeRequest(r.Context(), remoteID, t.MRIID)
		if err != nil {
			return table.Task{}, serrors.NewError(502, "GITLAB_MR_UNAVAILABLE", "无法确认 GitLab MR 状态，请稍后重试")
		}
		if err = a.setTaskMRDraft(r.Context(), remoteID, t, mr, false); err != nil {
			return table.Task{}, err
		}
		t.ApprovalHead = t.Head
		detail["gitlabDraft"] = false
	case "ReconfirmFeedback":
		if !review {
			return table.Task{}, serrors.NewError(403, "REVIEWER_REQUIRED", "仅指定审查者可核对新版本")
		}
		if b.Head != t.Head {
			return table.Task{}, serrors.NewError(409, "STALE_COMMIT", "代码版本已变化")
		}
		tag, err := table.ReconfirmFeedback(r.Context(), tx, b.FeedbackID, t.ID, t.Head)
		if err != nil {
			return table.Task{}, err
		}
		if tag.RowsAffected() == 0 {
			return table.Task{}, serrors.NewError(404, "NOT_FOUND", "修改要求不存在或已经处理")
		}
	case "Resume", "Feedback", "Continue", "NextFeedback":
		if b.Action == "Feedback" && !review {
			return table.Task{}, serrors.NewError(403, "REVIEWER_REQUIRED", "仅指定审查者可正式提交反馈")
		}
		if b.Action != "Feedback" && !manage {
			return table.Task{}, serrors.NewError(403, "OWNER_REQUIRED", "需要负责人或项目管理员")
		}
		if b.Head != t.Head {
			return table.Task{}, serrors.NewError(409, "STALE_COMMIT", "当前代码版本已变化，请重新核对")
		}
		if b.Action == "Feedback" && (t.ActiveRun != nil || t.Control != "Agent") {
			requirements := strings.TrimSpace(b.Text)
			if requirements == "" {
				return table.Task{}, serrors.NewError(400, "REQUIREMENTS_REQUIRED", "请填写已整合的修改要求")
			}
			id := codingid.ID("feedback")
			if _, e = table.CreateFeedback(
				r.Context(),
				tx,
				id,
				t.ID,
				u.ID,
				t.Head,
				requirements,
			); e != nil {
				return table.Task{}, e
			}
			detail["feedbackId"] = id
			detail["queued"] = true
			break
		}
		if t.ActiveRun != nil || !t.StopConfirmed {
			return table.Task{}, serrors.NewError(409, "STOP_UNCONFIRMED", "执行停止尚未确认，不能开始新一轮")
		}
		if b.Action == "Continue" {
			if t.Status == "Completed" || !terminal {
				return table.Task{}, serrors.NewError(409, "INVALID_CONTINUE", "仅可继续取消或未交付任务")
			}
			t.Status = "Review"
			t.Control = "Human"
			break
		}
		remoteID, err := a.Projects.ProjectID(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		mr, err := a.currentTaskMR(r.Context(), remoteID, t)
		if err != nil {
			return table.Task{}, err
		}
		if mr.State != "opened" || mr.Head != t.Head {
			return table.Task{}, serrors.NewError(409, "MR_CHANGED", "MR 状态或代码已变化，请先同步")
		}
		config, err := a.readyTaskSettings(r.Context(), t)
		if err != nil {
			return table.Task{}, err
		}
		configRaw, _ := json.Marshal(config)
		requirements := strings.TrimSpace(b.Text)
		if b.Action == "NextFeedback" || (b.Action == "Resume" && b.FeedbackID != "") {
			if b.Action == "NextFeedback" && t.Control != "Agent" {
				return table.Task{}, serrors.NewError(409, "HUMAN_CONTROL", "请先由负责人显式恢复 Agent")
			}
			feedback, e := table.LockNextFeedback(r.Context(), tx, t.ID, b.FeedbackID)
			if e != nil {
				return table.Task{}, serrors.NewError(409, "NO_FEEDBACK", "没有待处理要求")
			}
			requirements = feedback.Requirements
			if feedback.Head != t.Head || feedback.Status != "Pending" {
				return table.Task{}, serrors.NewError(
					409,
					"FEEDBACK_RECONFIRM_REQUIRED",
					"请先由审查者核对当前 commit",
				)
			}
			if _, e = table.ConsumeFeedback(r.Context(), tx, feedback.ID); e != nil {
				return table.Task{}, e
			}
			detail["feedbackId"] = feedback.ID
		}
		if requirements == "" {
			return table.Task{}, serrors.NewError(400, "REQUIREMENTS_REQUIRED", "请填写本轮已整合的修改要求")
		}
		if t.MRIID > 0 {
			if err = a.setTaskMRDraft(r.Context(), remoteID, t, mr, true); err != nil {
				return table.Task{}, err
			}
		}
		run := codingid.ID("run")
		t.Epoch++
		t.Round++
		t.Control = "Agent"
		t.Status = "Queued"
		t.AgentJudgment = json.RawMessage(`{"status":"Unknown"}`)
		t.Checks = "NotRun"
		t.CheckedHead = ""
		t.ApprovalHead = ""
		t.ActiveRun = &run
		t.StopRequestedAt = nil
		_, e = table.CreateRun(r.Context(), tx, run, t.ID, t.Epoch, t.Head, requirements, configRaw)
		detail["runId"] = run
	default:
		return table.Task{}, serrors.NewError(400, "INVALID_ACTION", "不支持的任务操作")
	}
	if e != nil {
		return table.Task{}, e
	}
	if e = table.WriteTask(r.Context(), tx, t); e != nil {
		return table.Task{}, e
	}
	if e = table.TaskEvent(r.Context(), tx, t.ID, u.ID, b.Action, detail); e != nil {
		return table.Task{}, e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return table.Task{}, e
	}
	t.Version++
	return t, nil
}

// syncTask imports remote facts without treating comments or MR reopen as execution commands.
func (a *Service) syncTask(gc *core.Context, u access.User) (table.Task, error) {
	r := gc.Request
	if _, e := a.Permissions.Read(r, u); e != nil {
		return table.Task{}, e
	}
	t, e := table.FindScopedTask(r.Context(), a.DB, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return table.Task{}, serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	t, e = a.syncTaskFacts(r.Context(), t, u.ID)
	if e != nil {
		return table.Task{}, e
	}
	return t, nil
}

type taskRunRequest struct {
	RuntimeEpoch int64 `json:"runtimeEpoch"`

	MRIID           int64           `json:"mrIid"`
	GitLabProjectID int64           `json:"gitlabProjectId"`
	RunID           string          `json:"runId"`
	Epoch           int64           `json:"epoch"`
	Worker          string          `json:"worker"`
	Status          string          `json:"status"`
	Head            string          `json:"head"`
	Output          json.RawMessage `json:"output"`
}

// finishTask accepts a stopped acknowledgement for the old epoch, but rejects late delivery.
func (a *Service) recordDelivery(ctx context.Context, taskID string, b taskRunRequest) (table.Task, error) {
	if b.Status != "Stopped" && b.Status != "Passed" && b.Status != "Failed" {
		return table.Task{}, serrors.NewError(400, "INVALID_RESULT", "执行结果无效")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return table.Task{}, e
	}
	defer tx.Rollback(ctx)
	t, e := table.LockTask(ctx, tx, taskID)
	if e != nil {
		return table.Task{}, serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	claim, e := table.LockDeliveryRun(ctx, tx, b.RunID, t.ID)
	if e != nil || claim.Worker != b.Worker || claim.Epoch != b.Epoch || t.ActiveRun == nil ||
		*t.ActiveRun != b.RunID {
		return table.Task{}, serrors.NewError(409, "STALE_EXECUTION", "执行已结束或不是当前执行器")
	}
	if b.Status != "Stopped" &&
		(t.Control != "Agent" || t.Epoch != claim.Epoch || claim.Status != "Running" && claim.Status != "Delivering") {
		return table.Task{}, serrors.NewError(409, "LATE_DELIVERY", "执行控制权已变化，拒绝迟到交付")
	}
	if b.Status != "Stopped" {
		var ready bool
		if ready, e = table.RunDeliveryReady(ctx, tx, b.RunID, b.Worker); e != nil ||
			!ready {
			return table.Task{}, serrors.NewError(409, "STALE_DELIVERY", "No active delivery request")
		}
	}
	if b.Status == "Stopped" {
		t.Control = "Human"
		t.StopConfirmed = true
		t.ActiveRun = nil
		if t.Status == "Canceling" {
			t.Status = "Canceled"
		} else if t.Status == "Running" || t.Status == "Queued" {
			t.Status = "Review"
		}
		var stopped struct {
			RuntimeStatus string `json:"runtimeStatus"`
		}
		_ = json.Unmarshal(b.Output, &stopped)
		if stopped.RuntimeStatus == "Failed" || stopped.RuntimeStatus == "NeedsReview" {
			if t.Status != "Canceled" && t.Status != "Completed" && t.Status != "Undelivered" {
				t.Status = "Failed"
			}
		}
	} else {
		level, err := a.Permissions.Level(ctx, t.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		if level < 30 {
			return table.Task{}, serrors.NewError(409, "EXECUTION_REVOKED", "负责人授权已撤销")
		}
		remoteID, err := a.Projects.ProjectID(ctx, t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		candidate := t
		if t.MRIID == 0 && b.MRIID > 0 {
			candidate.MRIID = b.MRIID
		}
		mr, err := a.currentTaskMR(ctx, remoteID, candidate)
		if err != nil {
			return table.Task{}, err
		}
		if mr.State != "opened" || mr.Head != b.Head || mr.Source != t.Branch || mr.Target != t.TargetBranch {
			return table.Task{}, serrors.NewError(
				409,
				"REMOTE_COMMIT_MISMATCH",
				"交付 commit 与 GitLab 当前版本不一致",
			)
		}
		t.MRIID = candidate.MRIID
		t.Head = b.Head
		if _, e = table.InvalidateFeedback(ctx, tx, t.ID, t.Head); e != nil {
			return table.Task{}, e
		}
		var evidence struct {
			Judgment json.RawMessage `json:"judgment"`
		}
		_ = json.Unmarshal(b.Output, &evidence)
		if len(evidence.Judgment) > 0 {
			t.AgentJudgment = evidence.Judgment
		}
		t.Checks = b.Status
		t.CheckedHead = b.Head
		t.ActiveRun = nil
		t.Status = "Review"
		if b.Status == "Failed" {
			t.Status = "Failed"
		}
		t.ApprovalHead = ""
	}
	if len(b.Output) == 0 {
		b.Output = json.RawMessage(`{}`)
	}
	_, e = table.FinishRun(ctx, tx, b.RunID, b.Status, b.Output)
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
		"executor",
		"Execution"+b.Status,
		map[string]any{"runId": b.RunID, "head": t.Head},
	); e != nil {
		return table.Task{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return table.Task{}, e
	}
	t.Version++
	return t, nil
}
