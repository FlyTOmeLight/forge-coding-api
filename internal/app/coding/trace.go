package coding

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type RuntimeTrace struct {
	Run *struct {
		RuntimeRun
		Scope *struct {
			TenantID  string `json:"tenantID"`
			ProjectID string `json:"projectID"`
			TaskID    string `json:"taskID"`
			RoundID   string `json:"roundID"`
		} `json:"scope"`
		WaitingStep string `json:"waitingStep,omitempty"`
		EventSeq    int64  `json:"eventSeq"`
		Deadline    string `json:"deadline"`
		Attempt     int    `json:"attempt"`
	} `json:"run"`
	Events      []json.RawMessage `json:"events"`
	LatestEvent json.RawMessage   `json:"latestEvent,omitempty"`
	NextSeq     int64             `json:"nextSeq"`
	HasMore     bool              `json:"hasMore"`
}

func (a *App) getRunTrace(gc *gin.Context, u User) {
	w, r := gc.Writer, gc.Request
	if _, err := a.taskRead(r, u); err != nil {
		fail(gc, err)
		return
	}
	after := int64(0)
	if text := r.URL.Query().Get("after"); text != "" {
		var err error
		after, err = strconv.ParseInt(text, 10, 64)
		if err != nil || after < 0 {
			fail(gc, problem(400, "INVALID_CURSOR", "事件序号无效"))
			return
		}
	}
	var runtimeID *string
	err := a.DB.QueryRow(r.Context(), `SELECT r.runtime_id FROM coding_runs r JOIN coding_tasks t ON t.id=r.task_id WHERE r.id=$1 AND t.id=$2 AND t.tenant_id=$3 AND t.project_id=$4`, r.PathValue("run"), r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid")).
		Scan(&runtimeID)
	if err == pgx.ErrNoRows {
		fail(gc, problem(404, "NOT_FOUND", "执行轮次不存在"))
		return
	}
	if err != nil {
		fail(gc, err)
		return
	}
	trace := RuntimeTrace{Events: []json.RawMessage{}, NextSeq: after}
	if runtimeID != nil {
		endpoint := a.Config.RuntimeURL + "/v1/runs/" + url.PathEscape(
			*runtimeID,
		) + "/trace?after=" + strconv.FormatInt(
			after,
			10,
		)
		if err = client.RequestLimited(
			r.Context(),
			a.HTTP,
			"GET",
			endpoint,
			a.Config.RuntimeToken,
			nil,
			&trace,
			32<<20,
		); err != nil {
			fail(gc, err)
			return
		}
		if trace.Run == nil || trace.Run.ID != *runtimeID || trace.Run.Scope == nil ||
			trace.Run.Scope.TenantID != r.PathValue("tid") ||
			trace.Run.Scope.ProjectID != r.PathValue("pid") ||
			trace.Run.Scope.TaskID != r.PathValue("task") ||
			trace.Run.Scope.RoundID != r.PathValue("run") {
			fail(gc, problem(502, "RUNTIME_SCOPE_MISMATCH", "执行记录与当前任务不匹配"))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	gc.JSON(200, trace)
}
