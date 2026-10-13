package coding

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

	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func (a *App) taskLevel(ctx context.Context, uid, tid, pid string) (int, error) {
	v, e := a.access(ctx, uid, tid, pid)
	if e != nil {
		return 0, e
	}
	n, _ := v["level"].(int)
	return n, nil
}

func (a *App) taskRead(r *http.Request, u User) (int, error) {
	level, e := a.taskLevel(r.Context(), u.ID, r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return 0, e
	}
	if level < 20 {
		return 0, problem(404, "NOT_FOUND", "任务不存在或无代码访问权限")
	}
	return level, nil
}

func (a *App) listTasks(gc *core.Context, u User) {
	r := gc.Request
	if _, e := a.taskRead(r, u); e != nil {
		fail(gc, e)
		return
	}
	rows, e := a.DB.Query(
		r.Context(),
		`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE tenant_id=$1 AND project_id=$2 ORDER BY updated_at DESC LIMIT 100`,
		r.PathValue("tid"),
		r.PathValue("pid"),
	)
	if e != nil {
		fail(gc, e)
		return
	}
	defer rows.Close()
	items := []table.Task{}
	for rows.Next() {
		t, e := table.ScanTask(rows)
		if e != nil {
			fail(gc, e)
			return
		}
		items = append(items, t)
	}
	if e = rows.Err(); e != nil {
		fail(gc, e)
		return
	}
	gc.JSON(200, Object{"items": items})
}

func (a *App) getTask(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	t, e := table.ScanTask(
		a.DB.QueryRow(
			r.Context(),
			`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`,
			r.PathValue("task"),
			r.PathValue("tid"),
			r.PathValue("pid"),
		),
	)
	if errors.Is(e, pgx.ErrNoRows) {
		e = problem(404, "NOT_FOUND", "任务不存在")
	}
	if e != nil {
		fail(gc, e)
		return
	}
	events, e := table.QueryMaps(
		r.Context(),
		a.DB,
		`SELECT id,actor_id,type,detail,created_at FROM coding_events WHERE task_id=$1 ORDER BY id DESC LIMIT 100`,
		t.ID,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	runs, e := table.QueryMaps(
		r.Context(),
		a.DB,
		`SELECT id,runtime_id,epoch,head,status,requirements,output,config_snapshot,created_at,finished_at FROM coding_runs WHERE task_id=$1 ORDER BY created_at DESC LIMIT 30`,
		t.ID,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	feedback, e := table.QueryMaps(
		r.Context(),
		a.DB,
		`SELECT id,submitted_by,head,requirements,status,created_at FROM coding_feedback WHERE task_id=$1 ORDER BY created_at LIMIT 100`,
		t.ID,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	project, e := a.project(r.Context(), t.TenantID, t.ProjectID)
	projectPath := project.Path
	if e != nil {
		fail(gc, e)
		return
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
			fail(gc, problem(502, "GITLAB_BRANCH_UNAVAILABLE", "无法确认 GitLab 工作分支，请稍后重试"))
			return
		}
	}
	gc.JSON(
		200,
		Object{
			"feedback":     feedback,
			"gitlabUrl":    gitlabURL,
			"gitlabTarget": gitlabTarget,
			"task":         t,
			"events":       events,
			"runs":         runs,
			"permissions": Object{
				"write":  level >= 30,
				"manage": level >= 40 || (level >= 30 && t.OwnerID == u.ID),
				"review": level >= 20 && slices.Contains(t.ReviewerIDs, u.ID),
			},
		},
	)
}

func (a *App) taskProject(ctx context.Context, tid, pid string) (int64, error) {
	p, e := a.project(ctx, tid, pid)
	return p.GitLabID, e
}

func (a *App) createTask(gc *core.Context, u User) {
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
	var b codingv1.AssociateTaskRequest
	if e = readJSON(gc, &b); e != nil {
		fail(gc, e)
		return
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.MRIID <= 0 {
		fail(gc, problem(400, "INVALID_TASK", "请填写任务、复现和验收条件，并选择待处理 MR"))
		return
	}
	remoteID, e := a.taskProject(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(gc, e)
		return
	}
	mr, e := a.GitLab.MergeRequest(r.Context(), remoteID, b.MRIID)
	if e != nil {
		fail(gc, e)
		return
	}
	if mr.State != "opened" || mr.Head == "" {
		fail(gc, problem(409, "MR_NOT_OPEN", "仅可关联尚未合入的开放 MR"))
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(gc, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e := table.ScanTask(
		tx.QueryRow(
			r.Context(),
			`INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,reviewer_ids,synthetic) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(project_id,mr_iid) WHERE mr_iid>0 DO UPDATE SET id=coding_tasks.id RETURNING `+table.TaskColumns,
			ID("task"),
			r.PathValue("tid"),
			r.PathValue("pid"),
			u.ID,
			b.Title,
			b.Description,
			b.Criteria,
			mr.Source,
			mr.Target,
			mr.IID,
			mr.Head,
			[]string{u.ID},
			b.Synthetic,
		),
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
		"TaskAssociated",
		Object{"head": t.Head, "mrIid": t.MRIID},
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

// taskAction serializes control transitions. Versioned writes reject stale clients.
func (a *App) taskAction(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	if level < 20 {
		fail(gc, problem(403, "READ_REQUIRED", "需要项目读取权限"))
		return
	}
	var b codingv1.TaskActionRequest
	if e = readJSON(gc, &b); e != nil {
		fail(gc, e)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(gc, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e := table.ScanTask(
		tx.QueryRow(
			r.Context(),
			`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3 FOR UPDATE`,
			r.PathValue("task"),
			r.PathValue("tid"),
			r.PathValue("pid"),
		),
	)
	if e != nil {
		fail(gc, problem(404, "NOT_FOUND", "任务不存在"))
		return
	}
	if t.Version != b.Version {
		fail(gc, problem(412, "STALE_VERSION", "任务已变化，请刷新后操作"))
		return
	}
	if level < 30 && b.Action != "Approve" && b.Action != "Feedback" && b.Action != "ReconfirmFeedback" {
		fail(gc, problem(403, "WRITE_REQUIRED", "需要项目代码写权限"))
		return
	}
	manage := t.OwnerID == u.ID || level >= 40
	review := slices.Contains(t.ReviewerIDs, u.ID)
	terminal := t.Status == "Completed" || t.Status == "Canceled" || t.Status == "Undelivered"
	if terminal && b.Action != "Transfer" && b.Action != "Continue" {
		fail(gc, problem(409, "TASK_ENDED", "任务已结束"))
		return
	}
	detail := Object{"head": t.Head, "text": strings.TrimSpace(b.Text)}
	switch b.Action {
	case "Intervene", "Cancel":
		if b.Action == "Cancel" && !manage {
			fail(gc, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		t.Epoch++
		t.ApprovalHead = ""
		t.StopConfirmed = t.ActiveRun == nil
		if t.ActiveRun != nil {
			var pending string
			if e = tx.QueryRow(r.Context(), `SELECT status FROM coding_runs WHERE id=$1 FOR UPDATE`, *t.ActiveRun).
				Scan(&pending); e != nil {
				fail(gc, e)
				return
			}
			if pending == "Queued" {
				if e = a.Workspaces.StopAll(r.Context(), t.ID, *t.ActiveRun); e != nil {
					fail(gc, e)
					return
				}
				if _, e = tx.Exec(
					r.Context(),
					`UPDATE coding_runs SET status='Stopped',finished_at=now() WHERE id=$1`,
					*t.ActiveRun,
				); e != nil {
					fail(gc, e)
					return
				}
				t.ActiveRun = nil
				t.StopConfirmed = true
			}
		}
		if t.ActiveRun == nil && a.Workspaces != nil {
			if e = a.Workspaces.Stop(r.Context(), t.ID); e != nil {
				fail(gc, e)
				return
			}
			if _, e = tx.Exec(
				r.Context(),
				`UPDATE coding_tasks SET sandbox_active=false WHERE id=$1`,
				t.ID,
			); e != nil {
				fail(gc, e)
				return
			}
		}
		t.Control = "Human"
		if t.ActiveRun != nil {
			t.Control = "Stopping"
			now := time.Now().UTC()
			t.StopRequestedAt = &now
			_, e = tx.Exec(
				r.Context(),
				`UPDATE coding_runs SET status='Stopping' WHERE id=$1 AND status IN ('Queued','Running','Delivering')`,
				*t.ActiveRun,
			)
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
			fail(gc, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		n, err := a.taskLevel(r.Context(), b.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			fail(gc, err)
			return
		}
		if n < 30 {
			fail(gc, problem(400, "INVALID_OWNER", "新负责人需要有效项目写权限"))
			return
		}
		t.OwnerID = b.OwnerID
		detail["ownerId"] = b.OwnerID
	case "Approve":
		if !review {
			fail(gc, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可提交审查结果"))
			return
		}
		if b.Head != t.Head {
			fail(gc, problem(409, "STALE_COMMIT", "审查认可的代码版本已失效"))
			return
		}
		if t.ActiveRun != nil || !t.StopConfirmed {
			fail(gc, problem(409, "STOP_UNCONFIRMED", "请等待当前执行结束后再认可代码版本"))
			return
		}
		if t.MRIID == 0 {
			fail(gc, problem(409, "MR_REQUIRED", "尚未生成 MR，不能认可交付版本"))
			return
		}
		remoteID, err := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			fail(gc, err)
			return
		}
		mr, err := a.GitLab.MergeRequest(r.Context(), remoteID, t.MRIID)
		if err != nil {
			fail(gc, problem(502, "GITLAB_MR_UNAVAILABLE", "无法确认 GitLab MR 状态，请稍后重试"))
			return
		}
		if err = a.setTaskMRDraft(r.Context(), remoteID, t, mr, false); err != nil {
			fail(gc, err)
			return
		}
		t.ApprovalHead = t.Head
		detail["gitlabDraft"] = false
	case "ReconfirmFeedback":
		if !review {
			fail(gc, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可核对新版本"))
			return
		}
		if b.Head != t.Head {
			fail(gc, problem(409, "STALE_COMMIT", "代码版本已变化"))
			return
		}
		tag, err := tx.Exec(
			r.Context(),
			`UPDATE coding_feedback SET head=$3,status='Pending' WHERE id=$1 AND task_id=$2 AND status IN ('Pending','NeedsReconfirmation')`,
			b.FeedbackID,
			t.ID,
			t.Head,
		)
		if err != nil {
			fail(gc, err)
			return
		}
		if tag.RowsAffected() == 0 {
			fail(gc, problem(404, "NOT_FOUND", "修改要求不存在或已经处理"))
			return
		}
	case "Resume", "Feedback", "Continue", "NextFeedback":
		if b.Action == "Feedback" && !review {
			fail(gc, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可正式提交反馈"))
			return
		}
		if b.Action != "Feedback" && !manage {
			fail(gc, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		if b.Head != t.Head {
			fail(gc, problem(409, "STALE_COMMIT", "当前代码版本已变化，请重新核对"))
			return
		}
		if b.Action == "Feedback" && (t.ActiveRun != nil || t.Control != "Agent") {
			requirements := strings.TrimSpace(b.Text)
			if requirements == "" {
				fail(gc, problem(400, "REQUIREMENTS_REQUIRED", "请填写已整合的修改要求"))
				return
			}
			id := ID("feedback")
			if _, e = tx.Exec(
				r.Context(),
				`INSERT INTO coding_feedback(id,task_id,submitted_by,head,requirements) VALUES($1,$2,$3,$4,$5)`,
				id,
				t.ID,
				u.ID,
				t.Head,
				requirements,
			); e != nil {
				fail(gc, e)
				return
			}
			detail["feedbackId"] = id
			detail["queued"] = true
			break
		}
		if t.ActiveRun != nil || !t.StopConfirmed {
			fail(gc, problem(409, "STOP_UNCONFIRMED", "执行停止尚未确认，不能开始新一轮"))
			return
		}
		if b.Action == "Continue" {
			if t.Status == "Completed" || !terminal {
				fail(gc, problem(409, "INVALID_CONTINUE", "仅可继续取消或未交付任务"))
				return
			}
			t.Status = "Review"
			t.Control = "Human"
			break
		}
		remoteID, err := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			fail(gc, err)
			return
		}
		mr, err := a.currentTaskMR(r.Context(), remoteID, t)
		if err != nil {
			fail(gc, err)
			return
		}
		if mr.State != "opened" || mr.Head != t.Head {
			fail(gc, problem(409, "MR_CHANGED", "MR 状态或代码已变化，请先同步"))
			return
		}
		config, err := a.readyTaskSettings(r.Context(), t)
		if err != nil {
			fail(gc, err)
			return
		}
		configRaw, _ := json.Marshal(config)
		requirements := strings.TrimSpace(b.Text)
		if b.Action == "NextFeedback" || (b.Action == "Resume" && b.FeedbackID != "") {
			if b.Action == "NextFeedback" && t.Control != "Agent" {
				fail(gc, problem(409, "HUMAN_CONTROL", "请先由负责人显式恢复 Agent"))
				return
			}
			var feedbackID, feedbackHead, feedbackState string
			if e = tx.QueryRow(r.Context(), `SELECT id,head,status,requirements FROM coding_feedback WHERE task_id=$1 AND status IN ('Pending','NeedsReconfirmation') AND ($2='' OR id=$2) ORDER BY created_at LIMIT 1 FOR UPDATE`, t.ID, b.FeedbackID).
				Scan(&feedbackID, &feedbackHead, &feedbackState, &requirements); e != nil {
				fail(gc, problem(409, "NO_FEEDBACK", "没有待处理要求"))
				return
			}
			if feedbackHead != t.Head || feedbackState != "Pending" {
				fail(gc, problem(409, "FEEDBACK_RECONFIRM_REQUIRED", "请先由审查者核对当前 commit"))
				return
			}
			if _, e = tx.Exec(
				r.Context(),
				`UPDATE coding_feedback SET status='Consumed' WHERE id=$1`,
				feedbackID,
			); e != nil {
				fail(gc, e)
				return
			}
			detail["feedbackId"] = feedbackID
		}
		if requirements == "" {
			fail(gc, problem(400, "REQUIREMENTS_REQUIRED", "请填写本轮已整合的修改要求"))
			return
		}
		if t.MRIID > 0 {
			if err = a.setTaskMRDraft(r.Context(), remoteID, t, mr, true); err != nil {
				fail(gc, err)
				return
			}
		}
		run := ID("run")
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
		detail["runId"] = run
	default:
		fail(gc, problem(400, "INVALID_ACTION", "不支持的任务操作"))
		return
	}
	if e != nil {
		fail(gc, e)
		return
	}
	if e = table.WriteTask(r.Context(), tx, t); e != nil {
		fail(gc, e)
		return
	}
	if e = table.TaskEvent(r.Context(), tx, t.ID, u.ID, b.Action, detail); e != nil {
		fail(gc, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(gc, e)
		return
	}
	t.Version++
	gc.JSON(200, t)
}

// syncTask imports remote facts without treating comments or MR reopen as execution commands.
func (a *App) syncTask(gc *core.Context, u User) {
	r := gc.Request
	if _, e := a.taskRead(r, u); e != nil {
		fail(gc, e)
		return
	}
	t, e := table.ScanTask(
		a.DB.QueryRow(
			r.Context(),
			`SELECT `+table.TaskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`,
			r.PathValue("task"),
			r.PathValue("tid"),
			r.PathValue("pid"),
		),
	)
	if e != nil {
		fail(gc, problem(404, "NOT_FOUND", "任务不存在"))
		return
	}
	t, e = a.syncTaskFacts(r.Context(), t, u.ID)
	if e != nil {
		fail(gc, e)
		return
	}
	gc.JSON(200, t)
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
func (a *App) recordDelivery(ctx context.Context, taskID string, b taskRunRequest) (table.Task, error) {
	if b.Status != "Stopped" && b.Status != "Passed" && b.Status != "Failed" {
		return table.Task{}, problem(400, "INVALID_RESULT", "执行结果无效")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return table.Task{}, e
	}
	defer tx.Rollback(ctx)
	t, e := table.ScanTask(
		tx.QueryRow(ctx, `SELECT `+table.TaskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, taskID),
	)
	if e != nil {
		return table.Task{}, problem(404, "NOT_FOUND", "任务不存在")
	}
	var worker, status string
	var epoch int64
	e = tx.QueryRow(ctx, `SELECT claimed_by,status,epoch FROM coding_runs WHERE id=$1 AND task_id=$2 FOR UPDATE`, b.RunID, t.ID).
		Scan(&worker, &status, &epoch)
	if e != nil || worker != b.Worker || epoch != b.Epoch || t.ActiveRun == nil || *t.ActiveRun != b.RunID {
		return table.Task{}, problem(409, "STALE_EXECUTION", "执行已结束或不是当前执行器")
	}
	if b.Status != "Stopped" &&
		(t.Control != "Agent" || t.Epoch != epoch || status != "Running" && status != "Delivering") {
		return table.Task{}, problem(409, "LATE_DELIVERY", "执行控制权已变化，拒绝迟到交付")
	}
	if b.Status != "Stopped" {
		var ready bool
		if e = tx.QueryRow(ctx, `SELECT status='Delivering' AND output ? 'finishRequest' FROM coding_runs WHERE id=$1 AND runtime_id=$2`, b.RunID, b.Worker).
			Scan(&ready); e != nil ||
			!ready {
			return table.Task{}, problem(409, "STALE_DELIVERY", "No active delivery request")
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
		level, err := a.taskLevel(ctx, t.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			return table.Task{}, err
		}
		if level < 30 {
			return table.Task{}, problem(409, "EXECUTION_REVOKED", "负责人授权已撤销")
		}
		remoteID, err := a.taskProject(ctx, t.TenantID, t.ProjectID)
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
			return table.Task{}, problem(409, "REMOTE_COMMIT_MISMATCH", "交付 commit 与 GitLab 当前版本不一致")
		}
		t.MRIID = candidate.MRIID
		t.Head = b.Head
		if _, e = tx.Exec(
			ctx,
			`UPDATE coding_feedback SET status='NeedsReconfirmation' WHERE task_id=$1 AND status='Pending' AND head<>$2`,
			t.ID,
			t.Head,
		); e != nil {
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
	_, e = tx.Exec(
		ctx,
		`UPDATE coding_runs SET status=$2,output=CASE WHEN $2='Stopped' THEN output || $3::jsonb ELSE $3::jsonb END,finished_at=now(),lease_until=NULL WHERE id=$1`,
		b.RunID,
		b.Status,
		b.Output,
	)
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
		Object{"runId": b.RunID, "head": t.Head},
	); e != nil {
		return table.Task{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return table.Task{}, e
	}
	t.Version++
	return t, nil
}
