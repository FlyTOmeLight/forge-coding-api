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

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// CodingTask keeps code ownership separate from task responsibility and review.
type CodingTask struct {
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

const taskColumns = `id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,status,control,version,epoch,round,checks,checked_head,approval_head,reviewer_ids,active_run,stop_requested_at,stop_confirmed,discussions,synthetic,updated_at,base_branch,reproduce,constraints,reference,agent_judgment`

func scanTask(row pgx.Row) (CodingTask, error) {
	var t CodingTask
	e := row.Scan(&t.ID, &t.TenantID, &t.ProjectID, &t.OwnerID, &t.Title, &t.Description, &t.Criteria, &t.Branch, &t.TargetBranch, &t.MRIID, &t.Head, &t.Status, &t.Control, &t.Version, &t.Epoch, &t.Round, &t.Checks, &t.CheckedHead, &t.ApprovalHead, &t.ReviewerIDs, &t.ActiveRun, &t.StopRequestedAt, &t.StopConfirmed, &t.Discussions, &t.Synthetic, &t.UpdatedAt, &t.BaseBranch, &t.Reproduce, &t.Constraints, &t.Reference, &t.AgentJudgment)
	return t, e
}
func (a *App) taskRoutes(m *gin.Engine) {
	route(m, "GET", "/api/v1/tenants/:tid/projects/:pid/tasks", a.authorized(a.listTasks))
	route(m, "POST", "/api/v1/tenants/:tid/projects/:pid/tasks", a.authorized(a.createTask))
	route(m, "POST", "/api/v1/tenants/:tid/projects/:pid/tasks/new", a.authorized(a.createProblemTask))
	route(m, "GET", "/api/v1/tenants/:tid/projects/:pid/coding-settings", a.authorized(a.getCodingSettings))
	route(m, "PUT", "/api/v1/tenants/:tid/projects/:pid/coding-settings", a.authorized(a.saveCodingSettings))
	route(m, "POST", "/api/v1/tenants/:tid/projects/:pid/coding-settings/check", a.authorized(a.checkCodingSettings))
	route(m, "GET", "/api/v1/platform/connections", a.authorized(a.codingConnections))
	route(m, "GET", "/api/v1/tenants/:tid/projects/:pid/tasks/:task", a.authorized(a.getTask))
	route(m, "POST", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/actions", a.authorized(a.taskAction))
	route(m, "POST", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/sync", a.authorized(a.syncTask))
	route(m, "POST", "/internal/v1/tasks/:task/gate", a.service(a.taskGate))
	route(m, "POST", "/internal/v1/tasks/:task/finish", a.service(a.finishTask))
}
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
func (a *App) listTasks(w http.ResponseWriter, r *http.Request, u User) {
	if _, e := a.taskRead(r, u); e != nil {
		fail(w, e)
		return
	}
	rows, e := a.DB.Query(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE tenant_id=$1 AND project_id=$2 ORDER BY updated_at DESC LIMIT 100`, r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(w, e)
		return
	}
	defer rows.Close()
	items := []CodingTask{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			fail(w, e)
			return
		}
		items = append(items, t)
	}
	if e = rows.Err(); e != nil {
		fail(w, e)
		return
	}
	JSON(w, 200, Object{"items": items})
}
func (a *App) getTask(w http.ResponseWriter, r *http.Request, u User) {
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(w, e)
		return
	}
	t, e := scanTask(a.DB.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid")))
	if errors.Is(e, pgx.ErrNoRows) {
		e = problem(404, "NOT_FOUND", "任务不存在")
	}
	if e != nil {
		fail(w, e)
		return
	}
	events, e := collect(r.Context(), a.DB, `SELECT id,actor_id,type,detail,created_at FROM coding_events WHERE task_id=$1 ORDER BY id DESC LIMIT 100`, t.ID)
	if e != nil {
		fail(w, e)
		return
	}
	runs, e := collect(r.Context(), a.DB, `SELECT id,runtime_id,epoch,head,status,requirements,output,config_snapshot,created_at,finished_at FROM coding_runs WHERE task_id=$1 ORDER BY created_at DESC LIMIT 30`, t.ID)
	if e != nil {
		fail(w, e)
		return
	}
	feedback, e := collect(r.Context(), a.DB, `SELECT id,submitted_by,head,requirements,status,created_at FROM coding_feedback WHERE task_id=$1 ORDER BY created_at LIMIT 100`, t.ID)
	if e != nil {
		fail(w, e)
		return
	}
	project, e := a.project(r.Context(), t.TenantID, t.ProjectID)
	projectPath := project.Path
	if e != nil {
		fail(w, e)
		return
	}
	gitlabURL := strings.TrimRight(a.Config.GitLabPublicURL, "/") + "/" + projectPath + "/-/tree/" + url.PathEscape(t.Branch)
	if t.MRIID > 0 {
		gitlabURL = strings.TrimRight(a.Config.GitLabPublicURL, "/") + "/" + projectPath + "/-/merge_requests/" + strconv.FormatInt(t.MRIID, 10)
	}
	JSON(w, 200, Object{"feedback": feedback, "gitlabUrl": gitlabURL, "task": t, "events": events, "runs": runs, "permissions": Object{"write": level >= 30, "manage": level >= 40 || (level >= 30 && t.OwnerID == u.ID), "review": level >= 20 && slices.Contains(t.ReviewerIDs, u.ID)}})
}
func (a *App) taskProject(ctx context.Context, tid, pid string) (int64, error) {
	p, e := a.project(ctx, tid, pid)
	return p.GitLabID, e
}
func taskEvent(ctx context.Context, tx pgx.Tx, id, actor, kind string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO coding_events(task_id,actor_id,type,detail) VALUES($1,$2,$3,$4)`, id, actor, kind, b)
	return e
}
func (a *App) createTask(w http.ResponseWriter, r *http.Request, u User) {
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
		Title       string `json:"title"`
		Description string `json:"description"`
		Criteria    string `json:"criteria"`
		MRIID       int64  `json:"mrIid"`
		Synthetic   bool   `json:"synthetic"`
	}
	if e = readJSON(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Criteria = strings.TrimSpace(b.Criteria)
	if !validName(b.Title) || b.Description == "" || b.Criteria == "" || b.MRIID <= 0 {
		fail(w, problem(400, "INVALID_TASK", "请填写任务、复现和验收条件，并选择待处理 MR"))
		return
	}
	remoteID, e := a.taskProject(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(w, e)
		return
	}
	mr, e := a.GitLab.MergeRequest(r.Context(), remoteID, b.MRIID)
	if e != nil {
		fail(w, e)
		return
	}
	if mr.State != "opened" || mr.Head == "" {
		fail(w, problem(409, "MR_NOT_OPEN", "仅可关联尚未合入的开放 MR"))
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e := scanTask(tx.QueryRow(r.Context(), `INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,reviewer_ids,synthetic) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(project_id,mr_iid) WHERE mr_iid>0 DO UPDATE SET id=coding_tasks.id RETURNING `+taskColumns, ID("task"), r.PathValue("tid"), r.PathValue("pid"), u.ID, b.Title, b.Description, b.Criteria, mr.Source, mr.Target, mr.IID, mr.Head, []string{u.ID}, b.Synthetic))
	if e != nil {
		fail(w, e)
		return
	}
	if e = taskEvent(r.Context(), tx, t.ID, u.ID, "TaskAssociated", Object{"head": t.Head, "mrIid": t.MRIID}); e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	JSON(w, 201, t)
}

// taskAction serializes control transitions. Versioned writes reject stale clients.
func (a *App) taskAction(w http.ResponseWriter, r *http.Request, u User) {
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(w, e)
		return
	}
	if level < 20 {
		fail(w, problem(403, "READ_REQUIRED", "需要项目读取权限"))
		return
	}
	var b struct {
		Action     string `json:"action"`
		Version    int64  `json:"version"`
		Head       string `json:"head"`
		Text       string `json:"text"`
		OwnerID    string `json:"ownerId"`
		FeedbackID string `json:"feedbackId"`
	}
	if e = readJSON(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e := scanTask(tx.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3 FOR UPDATE`, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid")))
	if e != nil {
		fail(w, problem(404, "NOT_FOUND", "任务不存在"))
		return
	}
	if t.Version != b.Version {
		fail(w, problem(412, "STALE_VERSION", "任务已变化，请刷新后操作"))
		return
	}
	if level < 30 && b.Action != "Approve" && b.Action != "Feedback" && b.Action != "ReconfirmFeedback" {
		fail(w, problem(403, "WRITE_REQUIRED", "需要项目代码写权限"))
		return
	}
	manage := t.OwnerID == u.ID || level >= 40
	review := slices.Contains(t.ReviewerIDs, u.ID)
	terminal := t.Status == "Completed" || t.Status == "Canceled" || t.Status == "Undelivered"
	if terminal && b.Action != "Transfer" && b.Action != "Continue" {
		fail(w, problem(409, "TASK_ENDED", "任务已结束"))
		return
	}
	detail := Object{"head": t.Head, "text": strings.TrimSpace(b.Text)}
	switch b.Action {
	case "Intervene", "Cancel":
		if b.Action == "Cancel" && !manage {
			fail(w, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		t.Epoch++
		t.ApprovalHead = ""
		t.StopConfirmed = t.ActiveRun == nil
		if t.ActiveRun != nil {
			var pending string
			if e = tx.QueryRow(r.Context(), `SELECT status FROM coding_runs WHERE id=$1 FOR UPDATE`, *t.ActiveRun).Scan(&pending); e != nil {
				fail(w, e)
				return
			}
			if pending == "Queued" {
				if _, e = tx.Exec(r.Context(), `UPDATE coding_runs SET status='Stopped',finished_at=now() WHERE id=$1`, *t.ActiveRun); e != nil {
					fail(w, e)
					return
				}
				t.ActiveRun = nil
				t.StopConfirmed = true
			}
		}
		t.Control = "Human"
		if t.ActiveRun != nil {
			t.Control = "Stopping"
			now := time.Now().UTC()
			t.StopRequestedAt = &now
			_, e = tx.Exec(r.Context(), `UPDATE coding_runs SET status='Stopping' WHERE id=$1 AND status IN ('Queued','Running')`, *t.ActiveRun)
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
			fail(w, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		n, err := a.taskLevel(r.Context(), b.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			fail(w, err)
			return
		}
		if n < 30 {
			fail(w, problem(400, "INVALID_OWNER", "新负责人需要有效项目写权限"))
			return
		}
		t.OwnerID = b.OwnerID
		detail["ownerId"] = b.OwnerID
	case "Approve":
		if !review {
			fail(w, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可提交审查结果"))
			return
		}
		if b.Head != t.Head {
			fail(w, problem(409, "STALE_COMMIT", "审查认可的代码版本已失效"))
			return
		}
		t.ApprovalHead = t.Head
	case "ReconfirmFeedback":
		if !review {
			fail(w, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可核对新版本"))
			return
		}
		if b.Head != t.Head {
			fail(w, problem(409, "STALE_COMMIT", "代码版本已变化"))
			return
		}
		tag, err := tx.Exec(r.Context(), `UPDATE coding_feedback SET head=$3,status='Pending' WHERE id=$1 AND task_id=$2 AND status IN ('Pending','NeedsReconfirmation')`, b.FeedbackID, t.ID, t.Head)
		if err != nil {
			fail(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			fail(w, problem(404, "NOT_FOUND", "修改要求不存在或已经处理"))
			return
		}
	case "Resume", "Feedback", "Continue", "NextFeedback":
		if b.Action == "Feedback" && !review {
			fail(w, problem(403, "REVIEWER_REQUIRED", "仅指定审查者可正式提交反馈"))
			return
		}
		if b.Action != "Feedback" && !manage {
			fail(w, problem(403, "OWNER_REQUIRED", "需要负责人或项目管理员"))
			return
		}
		if b.Head != t.Head {
			fail(w, problem(409, "STALE_COMMIT", "当前代码版本已变化，请重新核对"))
			return
		}
		if b.Action == "Feedback" && (t.ActiveRun != nil || t.Control != "Agent") {
			requirements := strings.TrimSpace(b.Text)
			if requirements == "" {
				fail(w, problem(400, "REQUIREMENTS_REQUIRED", "请填写已整合的修改要求"))
				return
			}
			id := ID("feedback")
			if _, e = tx.Exec(r.Context(), `INSERT INTO coding_feedback(id,task_id,submitted_by,head,requirements) VALUES($1,$2,$3,$4,$5)`, id, t.ID, u.ID, t.Head, requirements); e != nil {
				fail(w, e)
				return
			}
			detail["feedbackId"] = id
			detail["queued"] = true
			break
		}
		if t.ActiveRun != nil || !t.StopConfirmed {
			fail(w, problem(409, "STOP_UNCONFIRMED", "执行停止尚未确认，不能开始新一轮"))
			return
		}
		if b.Action == "Continue" {
			if t.Status == "Completed" || !terminal {
				fail(w, problem(409, "INVALID_CONTINUE", "仅可继续取消或未交付任务"))
				return
			}
			t.Status = "Review"
			t.Control = "Human"
			break
		}
		remoteID, err := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			fail(w, err)
			return
		}
		mr, err := a.currentTaskMR(r.Context(), remoteID, t)
		if err != nil {
			fail(w, err)
			return
		}
		if mr.State != "opened" || mr.Head != t.Head {
			fail(w, problem(409, "MR_CHANGED", "MR 状态或代码已变化，请先同步"))
			return
		}
		config, err := a.readyTaskSettings(r.Context(), t)
		if err != nil {
			fail(w, err)
			return
		}
		configRaw, _ := json.Marshal(config)
		requirements := strings.TrimSpace(b.Text)
		if b.Action == "NextFeedback" {
			if t.Control != "Agent" {
				fail(w, problem(409, "HUMAN_CONTROL", "请先由负责人显式恢复 Agent"))
				return
			}
			var feedbackID, feedbackHead, feedbackState string
			if e = tx.QueryRow(r.Context(), `SELECT id,head,status,requirements FROM coding_feedback WHERE task_id=$1 AND status IN ('Pending','NeedsReconfirmation') ORDER BY created_at LIMIT 1 FOR UPDATE`, t.ID).Scan(&feedbackID, &feedbackHead, &feedbackState, &requirements); e != nil {
				fail(w, problem(409, "NO_FEEDBACK", "没有待处理要求"))
				return
			}
			if feedbackHead != t.Head || feedbackState != "Pending" {
				fail(w, problem(409, "FEEDBACK_RECONFIRM_REQUIRED", "请先由审查者核对当前 commit"))
				return
			}
			if _, e = tx.Exec(r.Context(), `UPDATE coding_feedback SET status='Consumed' WHERE id=$1`, feedbackID); e != nil {
				fail(w, e)
				return
			}
			detail["feedbackId"] = feedbackID
		}
		if requirements == "" {
			fail(w, problem(400, "REQUIREMENTS_REQUIRED", "请填写本轮已整合的修改要求"))
			return
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
		_, e = tx.Exec(r.Context(), `INSERT INTO coding_runs(id,task_id,epoch,head,requirements,config_snapshot) VALUES($1,$2,$3,$4,$5,$6)`, run, t.ID, t.Epoch, t.Head, requirements, configRaw)
		detail["runId"] = run
	default:
		fail(w, problem(400, "INVALID_ACTION", "不支持的任务操作"))
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	if e = writeTask(r.Context(), tx, t); e != nil {
		fail(w, e)
		return
	}
	if e = taskEvent(r.Context(), tx, t.ID, u.ID, b.Action, detail); e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	t.Version++
	JSON(w, 200, t)
}
func writeTask(ctx context.Context, tx pgx.Tx, t CodingTask) error {
	_, e := tx.Exec(ctx, `UPDATE coding_tasks SET owner_id=$2,head=$3,status=$4,control=$5,epoch=$6,round=$7,checks=$8,checked_head=$9,approval_head=$10,active_run=$11,stop_requested_at=$12,stop_confirmed=$13,discussions=$14,mr_iid=$15,agent_judgment=$16,version=version+1,updated_at=now() WHERE id=$1`, t.ID, t.OwnerID, t.Head, t.Status, t.Control, t.Epoch, t.Round, t.Checks, t.CheckedHead, t.ApprovalHead, t.ActiveRun, t.StopRequestedAt, t.StopConfirmed, t.Discussions, t.MRIID, t.AgentJudgment)
	return e
}

// syncTask imports remote facts without treating comments or MR reopen as execution commands.
func (a *App) syncTask(w http.ResponseWriter, r *http.Request, u User) {
	if _, e := a.taskRead(r, u); e != nil {
		fail(w, e)
		return
	}
	t, e := scanTask(a.DB.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid")))
	if e != nil {
		fail(w, problem(404, "NOT_FOUND", "任务不存在"))
		return
	}
	remoteID, e := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
	if e != nil {
		fail(w, e)
		return
	}
	mr, e := a.currentTaskMR(r.Context(), remoteID, t)
	if e != nil {
		fail(w, e)
		return
	}
	notes := []map[string]any{}
	if t.MRIID > 0 {
		notes, e = a.GitLab.MRDiscussions(r.Context(), remoteID, t.MRIID)
		if e != nil {
			fail(w, e)
			return
		}
	}
	snapshotVersion := t.Version
	raw, e := json.Marshal(notes)
	if e != nil {
		fail(w, e)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e = scanTask(tx.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, t.ID))
	if e != nil {
		fail(w, e)
		return
	}
	if t.Version != snapshotVersion {
		fail(w, problem(409, "STALE_SYNC", "同步期间任务已变化，请重试同步"))
		return
	}
	if mr.IID > 0 {
		t.MRIID = mr.IID
	}
	changed := t.Head != mr.Head
	t.Discussions = raw
	expectedPush := false
	if changed && t.ActiveRun != nil && t.Control == "Agent" {
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM coding_runs WHERE id=$1 AND epoch=$2 AND status='Running' AND output->>'expectedHead'=$3)`, *t.ActiveRun, t.Epoch, mr.Head).Scan(&expectedPush); e != nil {
			fail(w, e)
			return
		}
	}
	if changed {
		t.Head = mr.Head
		if _, e = tx.Exec(r.Context(), `UPDATE coding_feedback SET status='NeedsReconfirmation' WHERE task_id=$1 AND status='Pending' AND head<>$2`, t.ID, t.Head); e != nil {
			fail(w, e)
			return
		}
		t.AgentJudgment = json.RawMessage(`{"status":"Unknown"}`)
		t.Checks = "NotRun"
		t.CheckedHead = ""
		t.ApprovalHead = ""
		if !expectedPush {
			t.Epoch++
			t.Control = "Human"
			if t.ActiveRun != nil {
				t.Control = "Stopping"
				t.StopConfirmed = false
				now := time.Now().UTC()
				t.StopRequestedAt = &now
				_, e = tx.Exec(r.Context(), `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, *t.ActiveRun)
			}
		}
	}
	terminal := t.Status == "Canceled" || t.Status == "Undelivered" || t.Status == "Completed"
	if mr.State == "merged" && !terminal {
		t.Status = "Completed"
		t.Control = "Human"
		t.Epoch++
	}
	if mr.State == "closed" && !terminal {
		t.Status = "Undelivered"
		t.Control = "Human"
		t.Epoch++
	}
	if (mr.State == "merged" || mr.State == "closed") && t.ActiveRun != nil {
		t.StopConfirmed = false
		t.Control = "Stopping"
		now := time.Now().UTC()
		t.StopRequestedAt = &now
		_, e = tx.Exec(r.Context(), `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, *t.ActiveRun)
	}
	if e != nil {
		fail(w, e)
		return
	}
	if e = writeTask(r.Context(), tx, t); e != nil {
		fail(w, e)
		return
	}
	if e = taskEvent(r.Context(), tx, t.ID, u.ID, "GitLabSync", Object{"head": mr.Head, "mrState": mr.State, "codeChanged": changed}); e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	t.Version++
	JSON(w, 200, t)
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
func (a *App) finishTask(w http.ResponseWriter, r *http.Request) {
	var b taskRunRequest
	if e := readJSONLimit(w, r, &b, 4<<20); e != nil {
		fail(w, e)
		return
	}
	if b.Status != "Stopped" && b.Status != "Passed" && b.Status != "Failed" {
		fail(w, problem(400, "INVALID_RESULT", "执行结果无效"))
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	t, e := scanTask(tx.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE`, r.PathValue("task")))
	if e != nil {
		fail(w, problem(404, "NOT_FOUND", "任务不存在"))
		return
	}
	var worker, status string
	var epoch int64
	e = tx.QueryRow(r.Context(), `SELECT claimed_by,status,epoch FROM coding_runs WHERE id=$1 AND task_id=$2 FOR UPDATE`, b.RunID, t.ID).Scan(&worker, &status, &epoch)
	if e != nil || worker != b.Worker || epoch != b.Epoch || t.ActiveRun == nil || *t.ActiveRun != b.RunID {
		fail(w, problem(409, "STALE_EXECUTION", "执行已结束或不是当前执行器"))
		return
	}
	if b.Status != "Stopped" && (t.Control != "Agent" || t.Epoch != epoch || status != "Running") {
		fail(w, problem(409, "LATE_DELIVERY", "执行控制权已变化，拒绝迟到交付"))
		return
	}
	if b.Status != "Stopped" {
		if e = a.runtimeLease(r.Context(), b.Worker, b.RuntimeEpoch); e != nil {
			fail(w, e)
			return
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
	} else {
		level, err := a.taskLevel(r.Context(), t.OwnerID, t.TenantID, t.ProjectID)
		if err != nil {
			fail(w, err)
			return
		}
		if level < 30 {
			fail(w, problem(409, "EXECUTION_REVOKED", "负责人授权已撤销"))
			return
		}
		remoteID, err := a.taskProject(r.Context(), t.TenantID, t.ProjectID)
		if err != nil {
			fail(w, err)
			return
		}
		candidate := t
		if t.MRIID == 0 && b.MRIID > 0 {
			candidate.MRIID = b.MRIID
		}
		mr, err := a.currentTaskMR(r.Context(), remoteID, candidate)
		if err != nil {
			fail(w, err)
			return
		}
		if mr.State != "opened" || mr.Head != b.Head || mr.Source != t.Branch || mr.Target != t.TargetBranch {
			fail(w, problem(409, "REMOTE_COMMIT_MISMATCH", "交付 commit 与 GitLab 当前版本不一致"))
			return
		}
		t.MRIID = candidate.MRIID
		t.Head = b.Head
		if _, e = tx.Exec(r.Context(), `UPDATE coding_feedback SET status='NeedsReconfirmation' WHERE task_id=$1 AND status='Pending' AND head<>$2`, t.ID, t.Head); e != nil {
			fail(w, e)
			return
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
	_, e = tx.Exec(r.Context(), `UPDATE coding_runs SET status=$2,output=$3,finished_at=now(),lease_until=NULL WHERE id=$1`, b.RunID, b.Status, b.Output)
	if e != nil {
		fail(w, e)
		return
	}
	if e = writeTask(r.Context(), tx, t); e != nil {
		fail(w, e)
		return
	}
	if e = taskEvent(r.Context(), tx, t.ID, "executor", "Execution"+b.Status, Object{"runId": b.RunID, "head": t.Head}); e != nil {
		fail(w, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	t.Version++
	JSON(w, 200, t)
}
