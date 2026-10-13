package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

type sandboxScope struct{ TenantID, ProjectID, ActorID, TaskID, RoundID string }

func (a *App) sandboxIdentity(r *http.Request) (table.Task, taskRunRequest, error) {
	var scope sandboxScope
	if err := json.Unmarshal([]byte(r.Header.Get("X-Agentic-Scope")), &scope); err != nil {
		return table.Task{}, taskRunRequest{}, problem(400, "INVALID_SCOPE", "Missing execution scope")
	}
	runtime := r.Header.Get("X-Agentic-Run-ID")
	attempt, err := strconv.ParseInt(r.Header.Get("X-Agentic-Epoch"), 10, 64)
	if err != nil || attempt < 1 || runtime == "" || scope.RoundID == "" || scope.TaskID == "" {
		return table.Task{}, taskRunRequest{}, problem(400, "INVALID_SCOPE", "Missing execution identity")
	}
	var epoch int64
	var taskID string
	if err = a.DB.QueryRow(r.Context(), `SELECT task_id,epoch FROM coding_runs WHERE id=$1 AND runtime_id=$2 AND status='Running'`, scope.RoundID, runtime).
		Scan(&taskID, &epoch); err != nil {
		return table.Task{}, taskRunRequest{}, problem(
			409,
			"STALE_EXECUTION",
			"Business round is no longer active",
		)
	}
	if taskID != scope.TaskID {
		return table.Task{}, taskRunRequest{}, problem(403, "SCOPE_MISMATCH", "Execution task mismatch")
	}
	b := taskRunRequest{RunID: scope.RoundID, Epoch: epoch, Worker: runtime, RuntimeEpoch: attempt}
	// The bound gateway takes over the role of the old tool's start callback.
	if _, err = a.DB.Exec(
		r.Context(),
		`UPDATE coding_runs SET claimed_by=$2 WHERE id=$1 AND runtime_id=$2 AND status='Running'`,
		scope.RoundID,
		runtime,
	); err != nil {
		return table.Task{}, b, err
	}
	task, err := a.checkGate(r.Context(), taskID, b)
	if err != nil {
		return task, b, err
	}
	if task.TenantID != scope.TenantID || task.ProjectID != scope.ProjectID {
		return task, b, problem(403, "SCOPE_MISMATCH", "Execution project mismatch")
	}
	return task, b, nil
}

func (a *App) roundRepository(ctx context.Context, t table.Task) (workspace.Repository, error) {
	project, err := a.project(ctx, t.TenantID, t.ProjectID)
	if err != nil {
		return workspace.Repository{}, err
	}
	if !a.Config.EnabledProjects[strconv.FormatInt(project.GitLabID, 10)] {
		return workspace.Repository{}, problem(
			409,
			"PROJECT_EXECUTOR_UNAVAILABLE",
			"Project is not enabled for Coding execution",
		)
	}
	token, err := a.GitLab.Credential()
	if err != nil {
		return workspace.Repository{}, err
	}
	return workspace.Repository{
		URL:         strings.TrimRight(a.Config.GitLabURL, "/") + "/" + project.Path + ".git",
		Head:        t.Head,
		Branch:      t.Branch,
		Token:       token,
		PermitToken: a.Config.ServiceToken,
	}, nil
}

func (a *App) prepareSandbox(ctx context.Context, t table.Task, round string) (workspace.Endpoint, error) {
	if a.Workspaces == nil {
		return workspace.Endpoint{}, errors.New("AIO workspace provider is not configured")
	}
	repository, err := a.roundRepository(ctx, t)
	if err != nil {
		return workspace.Endpoint{}, err
	}
	return a.Workspaces.Ensure(ctx, t.ID, round, repository)
}

func (a *App) sandboxProxy(gc *gin.Context) {
	w, r := gc.Writer, gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	if r.PathValue("run") != b.RunID {
		fail(gc, problem(403, "SCOPE_MISMATCH", "Sandbox round mismatch"))
		return
	}
	apiPath := r.PathValue("path")
	if apiPath != "/v1/sandbox" && !strings.HasPrefix(apiPath, "/v1/file/") &&
		!strings.HasPrefix(apiPath, "/v1/bash/") {
		fail(gc, problem(403, "TOOL_NOT_ALLOWED", "Sandbox capability is not enabled"))
		return
	}
	if a.Workspaces == nil {
		fail(gc, problem(503, "SANDBOX_UNAVAILABLE", "AIO is not configured"))
		return
	}
	endpoint, err := a.prepareSandbox(r.Context(), task, b.RunID)
	if err != nil {
		fail(gc, problem(503, "SANDBOX_PREPARING", err.Error()))
		return
	}
	if err = a.Workspaces.Fence(r.Context(), task.ID, b.RunID, b.RuntimeEpoch); err != nil {
		fail(gc, problem(503, "SANDBOX_FENCING", err.Error()))
		return
	}

	target, err := url.Parse(endpoint.URL)
	if err != nil {
		fail(gc, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path = apiPath
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+endpoint.Token)
		req.Header.Del("X-Agentic-Scope")
		req.Header.Del("X-Agentic-Run-ID")
		req.Header.Del("X-Agentic-Epoch")
	}
	proxy.Transport = a.HTTP.Transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		fail(gc, problem(503, "SANDBOX_UNAVAILABLE", "AIO request outcome requires inspection"))
	}
	proxy.ServeHTTP(w, r)
}

func (a *App) submitFinish(gc *gin.Context) {
	r := gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	var body struct {
		Summary  string `json:"summary"`
		Judgment struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"judgment"`
	}
	if err = readJSON(gc, &body); err != nil {
		fail(gc, err)
		return
	}
	if strings.TrimSpace(body.Summary) == "" || strings.TrimSpace(body.Judgment.Summary) == "" ||
		(body.Judgment.Status != "Met" && body.Judgment.Status != "Unmet" && body.Judgment.Status != "NeedsHuman") {
		fail(gc, problem(422, "INVALID_TOOL_INPUT", "summary and valid judgment are required"))
		return
	}
	operation := r.Header.Get("Idempotency-Key")
	if !strings.HasPrefix(operation, b.Worker+":") {
		fail(gc, problem(400, "INVALID_OPERATION", "Missing stable tool operation identity"))
		return
	}
	request, _ := json.Marshal(body)
	result, err := a.DB.Exec(
		r.Context(),
		`UPDATE coding_runs SET output=output || jsonb_build_object('finishRequest',$2::jsonb,'finishOperation',$3::text) WHERE id=$1 AND status='Running' AND (NOT output ? 'finishOperation' OR (output->>'finishOperation'=$3 AND output->'finishRequest'=$2::jsonb))`,
		b.RunID,
		request,
		operation,
	)
	if err != nil {
		fail(gc, err)
		return
	}
	if result.RowsAffected() != 1 {
		fail(gc, problem(409, "FINISH_CONFLICT", "Different delivery request already exists"))
		return
	}
	gc.JSON(200, Object{"accepted": true, "roundID": b.RunID, "taskID": task.ID})
}

func (a *App) sandboxChecks(gc *gin.Context) {
	r := gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	var raw []byte
	if err = a.DB.QueryRow(r.Context(), `SELECT config_snapshot FROM coding_runs WHERE id=$1`, b.RunID).
		Scan(&raw); err != nil {
		fail(gc, err)
		return
	}
	var cfg CodingSettings
	if err = json.Unmarshal(raw, &cfg); err != nil {
		fail(gc, err)
		return
	}
	counted, err := a.DB.Exec(
		r.Context(),
		`UPDATE coding_runs SET output=jsonb_set(output,'{checkAttempts}',to_jsonb(coalesce((output->>'checkAttempts')::int,0)+1)) WHERE id=$1 AND status='Running' AND coalesce((output->>'checkAttempts')::int,0)<$2`,
		b.RunID,
		cfg.RepairLimit+1,
	)
	if err != nil {
		fail(gc, err)
		return
	}
	if counted.RowsAffected() != 1 {
		gc.JSON(
			200,
			Object{
				"error": Object{
					"code":    "REPAIR_LIMIT_REACHED",
					"message": "Project check budget exhausted. Submit finish with the remaining failures.",
				},
			},
		)
		return
	}

	endpoint, err := a.Workspaces.Access(r.Context(), task.ID)
	if err != nil {
		fail(gc, err)
		return
	}
	checks, err := runAIOChecks(r.Context(), endpoint, cfg.Checks)
	if err != nil {
		fail(gc, problem(503, "CHECK_OUTCOME_UNKNOWN", fmt.Sprint(err)))
		return
	}
	gc.JSON(200, Object{"checks": checks})
}
