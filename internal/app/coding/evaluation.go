package coding

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

type evaluationScope = codingv1.EvaluationScope

type evaluationScenario = codingv1.EvaluationScenario

type evaluationRequest = codingv1.EvaluationEnvironmentRequest

type evaluationEvidence struct {
	Status   string   `json:"status"`
	BaseHead string   `json:"baseHead"`
	Patch    string   `json:"patch,omitempty"`
	Changed  []string `json:"changed,omitempty"`
	Checks   []Object `json:"checks,omitempty"`
	Error    string   `json:"error,omitempty"`
	Stopped  bool     `json:"stopped"`
}

func evaluationNames(id string) (string, string) { return "eval-" + id, "eval-" + id + "-result" }

func (a *App) evaluationRepository(ctx context.Context, req evaluationRequest) (workspace.Repository, error) {
	level, err := a.taskLevel(ctx, req.Scope.ActorID, req.Scope.TenantID, req.Scope.ProjectID)
	if err != nil {
		return workspace.Repository{}, err
	}
	if level < 30 {
		return workspace.Repository{}, problem(403, "EXECUTION_REVOKED", "项目执行权限已撤销")
	}
	return a.roundRepository(
		ctx,
		table.Task{
			TenantID:  req.Scope.TenantID,
			ProjectID: req.Scope.ProjectID,
			Head:      req.Scenario.BaseHead,
			Branch:    "evaluation",
		},
	)
}

func (a *App) evaluationEnvironment(gc *gin.Context) {
	w, r := gc.Writer, gc.Request
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(190 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var req evaluationRequest
	if err := readJSON(gc, &req); err != nil {
		fail(gc, err)
		return
	}
	id := r.PathValue("trial")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(req.Scenario.BaseHead) ||
		req.Scope.TenantID == "" ||
		req.Scope.ProjectID == "" ||
		req.Scope.ActorID == "" ||
		(req.Phase != "prepare" && req.Phase != "verify" && req.Phase != "stop") {
		fail(gc, problem(400, "INVALID_EVALUATION", "评测环境配置无效"))
		return
	}
	cfg := CodingSettings{
		AgentID:        "evaluation",
		AgentVersion:   1,
		BaseBranch:     "evaluation",
		AllowedPaths:   req.Scenario.AllowedPaths,
		Checks:         req.Scenario.Checks,
		RepairLimit:    1,
		TimeoutMinutes: 30,
	}
	if err := cfg.validate(); err != nil {
		fail(gc, err)
		return
	}
	if a.Workspaces == nil {
		fail(gc, problem(503, "WORKSPACE_UNAVAILABLE", "独立代码环境未配置"))
		return
	}
	phase := req.Phase
	req.Phase = ""
	raw := mustJSON(req)
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(gc, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize duplicate reconciliation requests. A separate stop request waits for
	// the bounded check operation, then confirms every process has stopped.
	if _, err = tx.Exec(
		r.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"evaluation:"+id,
	); err != nil {
		fail(gc, err)
		return
	}
	evidence := evaluationEvidence{Status: "Preparing", BaseHead: req.Scenario.BaseHead}
	if _, err = a.DB.Exec(
		r.Context(),
		`INSERT INTO coding_evaluation_environments(id,request,evidence) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`,
		id,
		raw,
		mustJSON(evidence),
	); err != nil {
		fail(gc, err)
		return
	}
	var stored, state, artifactRaw []byte
	if err = tx.QueryRow(r.Context(), `SELECT request,evidence,artifact FROM coding_evaluation_environments WHERE id=$1`, id).
		Scan(&stored, &state, &artifactRaw); err != nil {
		fail(gc, err)
		return
	}
	var frozen evaluationRequest
	if err = json.Unmarshal(stored, &frozen); err != nil {
		fail(gc, err)
		return
	}
	if string(mustJSON(frozen)) != string(raw) {
		fail(gc, problem(409, "EVALUATION_CONFLICT", "Trial 已绑定另一份评测输入"))
		return
	}
	if err = json.Unmarshal(state, &evidence); err != nil {
		fail(gc, err)
		return
	}
	task, round := evaluationNames(id)
	committed := false
	save := func() {
		if committed {
			if _, err = a.DB.Exec(
				r.Context(),
				`UPDATE coding_evaluation_environments SET evidence=$2,updated_at=now() WHERE id=$1`,
				id,
				mustJSON(evidence),
			); err != nil {
				fail(gc, err)
				return
			}
			gc.JSON(200, evidence)
			return
		}
		if _, err = tx.Exec(
			r.Context(),
			`UPDATE coding_evaluation_environments SET evidence=$2,updated_at=now() WHERE id=$1`,
			id,
			mustJSON(evidence),
		); err != nil {
			fail(gc, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			fail(gc, err)
			return
		}
		gc.JSON(200, evidence)
	}
	stop := func() bool {
		// Commit the result before deleting its source artifacts. Recovery can repeat
		// cleanup without rerunning verification or losing an already measured result.
		if _, err = tx.Exec(
			r.Context(),
			`UPDATE coding_evaluation_environments SET evidence=$2,updated_at=now() WHERE id=$1`,
			id,
			mustJSON(evidence),
		); err != nil {
			fail(gc, err)
			return false
		}
		if err = tx.Commit(r.Context()); err != nil {
			fail(gc, err)
			return false
		}
		committed = true
		if err = a.Workspaces.CleanupEvaluation(r.Context(), task, round); err != nil {
			fail(gc, err)
			return false
		}
		evidence.Stopped = true
		return true
	}
	if evidence.Stopped {
		save()
		return
	}
	if evidence.Status == "Completed" || evidence.Status == "Failed" || evidence.Status == "Stopped" {
		if stop() {
			save()
		}
		return
	}
	if phase == "stop" {
		evidence.Status = "Stopped"
		if stop() {
			save()
		}
		return
	}
	repository, err := a.evaluationRepository(r.Context(), req)
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Repository or project execution permission unavailable"
		if stop() {
			save()
		}
		return
	}
	if phase == "prepare" {
		if evidence.Status == "Ready" {
			save()
			return
		}
		endpoint, prepareErr := a.Workspaces.Ensure(r.Context(), task, round, repository)
		err = prepareErr
		if errors.Is(err, workspace.ErrPreparing) {
			save()
			return
		}
		if err != nil {
			evidence.Status = "Failed"
			evidence.Error = "Repository environment preparation failed"
			if stop() {
				save()
			}
			return
		}
		if err = client.Request(
			r.Context(),
			a.HTTP,
			"GET",
			endpoint.URL+"/v1/sandbox",
			endpoint.Token,
			nil,
			nil,
		); err != nil {
			var unavailable *serrors.Error
			if errors.As(err, &unavailable) && unavailable.Status == 503 {
				save()
				return
			}
			evidence.Status = "Failed"
			evidence.Error = "Repository environment service rejected access"
			if stop() {
				save()
			}
			return
		}
		evidence.Status = "Ready"
		save()
		return
	}
	if evidence.Status != "Ready" && evidence.Status != "Verifying" {
		fail(gc, problem(409, "EVALUATION_NOT_READY", "代码环境尚未就绪"))
		return
	}
	evidence.Status = "Verifying"
	if len(artifactRaw) == 0 {
		if err = a.Workspaces.Pause(r.Context(), task, round); err != nil {
			fail(gc, err)
			return
		}
		env := map[string]string{
			"REPOSITORY_URL": repository.URL,
			"BASE_HEAD":      req.Scenario.BaseHead,
			"WORK_BRANCH":    "evaluation",
			"ALLOWED_PATHS":  string(mustJSON(req.Scenario.AllowedPaths)),
			"SUMMARY":        "Coding evaluation candidate",
			"COMMIT_DATE":    "2000-01-01T00:00:00Z",
		}
		artifactRaw, err = a.Workspaces.RunJob(r.Context(), task, round, "snapshot", env)
		if errors.Is(err, workspace.ErrPreparing) {
			save()
			return
		}
		if err != nil {
			evidence.Status = "Failed"
			evidence.Error = "Candidate snapshot failed; check allowed paths and repository changes"
			if stop() {
				save()
			}
			return
		}
		if _, err = tx.Exec(
			r.Context(),
			`UPDATE coding_evaluation_environments SET artifact=$2 WHERE id=$1`,
			id,
			artifactRaw,
		); err != nil {
			fail(gc, err)
			return
		}
	}
	var artifact workspace.Artifact
	if err = json.Unmarshal(artifactRaw, &artifact); err != nil {
		fail(gc, err)
		return
	}
	evidence.Patch = artifact.Patch
	evidence.Changed = artifact.Changed
	endpoint, err := a.Workspaces.Verifier(r.Context(), task, round)
	if errors.Is(err, workspace.ErrPreparing) {
		save()
		return
	}
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Independent verification environment failed"
		if stop() {
			save()
		}
		return
	}
	// Container readiness can precede Service endpoint propagation. Probe the
	// control API before starting any command so this retry has no side effects.
	if err = client.Request(
		r.Context(),
		a.HTTP,
		"GET",
		endpoint.URL+"/v1/sandbox",
		endpoint.Token,
		nil,
		nil,
	); err != nil {
		var unavailable *serrors.Error
		if errors.As(err, &unavailable) && unavailable.Status == 503 {
			save()
			return
		}
		evidence.Status = "Failed"
		evidence.Error = "Independent verification service rejected access"
		if stop() {
			save()
		}
		return
	}
	checkCtx, checkCancel := context.WithTimeout(r.Context(), 120*time.Second)
	checks, err := runAIOChecks(checkCtx, endpoint, req.Scenario.Checks)
	checkCancel()
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Independent verification did not return exit codes"
		slog.Error("coding evaluation verification failed", "trial", id, "error", err)
	} else {
		evidence.Checks = checks
		evidence.Status = "Completed"
	}
	if stop() {
		save()
	}
}

func (a *App) evaluationIdentity(r *http.Request) (string, evaluationRequest, error) {
	var scope sandboxScope
	if err := json.Unmarshal([]byte(r.Header.Get("X-Agentic-Scope")), &scope); err != nil {
		return "", evaluationRequest{}, problem(403, "INVALID_SCOPE", "评测身份无效")
	}
	id := strings.TrimPrefix(scope.TaskID, "eval:")
	if id == scope.TaskID || scope.RoundID != "1" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		return "", evaluationRequest{}, problem(403, "INVALID_SCOPE", "评测身份无效")
	}
	var raw, state []byte
	if err := a.DB.QueryRow(r.Context(), `SELECT request,evidence FROM coding_evaluation_environments WHERE id=$1`, id).
		Scan(&raw, &state); err != nil {
		return "", evaluationRequest{}, problem(403, "INVALID_SCOPE", "评测环境不存在")
	}
	var req evaluationRequest
	var evidence evaluationEvidence
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", req, err
	}
	if err := json.Unmarshal(state, &evidence); err != nil {
		return "", req, err
	}
	if evidence.Status != "Ready" || evidence.Stopped || scope.TenantID != req.Scope.TenantID ||
		scope.ProjectID != req.Scope.ProjectID ||
		scope.ActorID != req.Scope.ActorID {
		return "", req, problem(409, "EXECUTION_REVOKED", "评测环境已停止或身份不匹配")
	}
	epoch, err := strconv.ParseInt(r.Header.Get("X-Agentic-Epoch"), 10, 64)
	if err != nil || epoch < 1 {
		return "", req, problem(403, "INVALID_EPOCH", "执行轮次无效")
	}
	runtime := r.Header.Get("X-Agentic-Run-ID")
	var lease struct {
		Allowed bool `json:"allowed"`
	}
	err = client.Request(
		r.Context(),
		a.HTTP,
		"GET",
		a.Config.RuntimeURL+"/internal/v1/evaluation-trials/"+id+"/lease?runID="+url.QueryEscape(
			runtime,
		)+"&epoch="+strconv.FormatInt(
			epoch,
			10,
		),
		a.Config.RuntimeToken,
		nil,
		&lease,
	)
	if err != nil {
		return "", req, err
	}
	if !lease.Allowed {
		return "", req, problem(409, "EXECUTION_REVOKED", "评测执行权已失效")
	}
	repository, err := a.evaluationRepository(r.Context(), req)
	if err != nil {
		return "", req, err
	}
	task, round := evaluationNames(id)
	if _, err = a.Workspaces.Ensure(r.Context(), task, round, repository); err != nil {
		if errors.Is(err, workspace.ErrPreparing) {
			return "", req, problem(503, "SANDBOX_PREPARING", "代码环境正在恢复")
		}
		return "", req, err
	}
	if err = a.Workspaces.Fence(r.Context(), task, round, epoch); err != nil {
		if errors.Is(err, workspace.ErrPreparing) {
			return "", req, problem(503, "SANDBOX_FENCING", "等待旧执行环境停止")
		}
		return "", req, err
	}
	return id, req, nil
}

func (a *App) evaluationSandbox(gc *gin.Context) {
	w, r := gc.Writer, gc.Request
	id, _, err := a.evaluationIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	if id != r.PathValue("trial") {
		fail(gc, problem(403, "INVALID_SCOPE", "环境不匹配"))
		return
	}
	p := r.PathValue("path")
	if p != "/v1/sandbox" && !strings.HasPrefix(p, "/v1/file/") && !strings.HasPrefix(p, "/v1/bash/") {
		fail(gc, problem(404, "NOT_FOUND", "接口不存在"))
		return
	}
	task, _ := evaluationNames(id)
	endpoint, err := a.Workspaces.Access(r.Context(), task)
	if err != nil {
		fail(gc, err)
		return
	}
	target, err := url.Parse(endpoint.URL)
	if err != nil {
		fail(gc, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if r.Method == "GET" && p == "/v1/sandbox" {
			fail(gc, problem(503, "SANDBOX_PREPARING", "等待代码环境服务就绪"))
			return
		}
		fail(gc, problem(502, "SANDBOX_UNAVAILABLE", "代码工具连接中断"))
	}

	original := proxy.Director
	proxy.Director = func(out *http.Request) {
		original(out)
		out.URL.Path = p
		out.URL.RawPath = ""
		out.Header.Set("Authorization", "Bearer "+endpoint.Token)
		out.Header.Del("X-Agentic-Scope")
		out.Header.Del("X-Agentic-Run-ID")
		out.Header.Del("X-Agentic-Epoch")
	}
	proxy.ServeHTTP(w, r)
}

func (a *App) evaluationFinish(gc *gin.Context) {
	r := gc.Request
	_, _, err := a.evaluationIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	var finish codingv1.EvaluationFinishRequest
	if err = readJSON(gc, &finish); err != nil {
		fail(gc, err)
		return
	}
	if strings.TrimSpace(finish.Summary) == "" {
		fail(gc, problem(400, "INVALID_FINISH", "需要结果摘要"))
		return
	}
	gc.JSON(200, Object{"accepted": true, "summary": finish.Summary, "verification": "pending independent checks"})
}

func (a *App) evaluationChecks(gc *gin.Context) {
	r := gc.Request
	id, req, err := a.evaluationIdentity(r)
	if err != nil {
		fail(gc, err)
		return
	}
	task, _ := evaluationNames(id)
	endpoint, err := a.Workspaces.Access(r.Context(), task)
	if err != nil {
		fail(gc, err)
		return
	}
	checkCtx, checkCancel := context.WithTimeout(r.Context(), 120*time.Second)
	checks, err := runAIOChecks(checkCtx, endpoint, req.Scenario.Checks)
	checkCancel()
	if err != nil {
		fail(gc, err)
		return
	}
	gc.JSON(200, Object{"checks": checks})
}

// Reclaim orphan environments if the evaluation API cannot finish reconciliation.
func (a *App) reapEvaluationEnvironments(ctx context.Context) error {
	if a.Workspaces == nil {
		return nil
	}
	rows, err := a.DB.Query(
		ctx,
		`SELECT id FROM coding_evaluation_environments WHERE NOT (evidence->>'stopped')::boolean AND created_at < now()-interval '25 hours' LIMIT 100`,
	)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		task, round := evaluationNames(id)
		stopCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err = a.Workspaces.CleanupEvaluation(stopCtx, task, round)
		cancel()
		if err != nil {
			return err
		}
		_, err = a.DB.Exec(
			ctx,
			`UPDATE coding_evaluation_environments SET evidence=evidence || '{"status":"Failed","stopped":true,"error":"Environment expired"}'::jsonb,updated_at=now() WHERE id=$1`,
			id,
		)
		if err != nil {
			return err
		}
	}
	return nil
}
