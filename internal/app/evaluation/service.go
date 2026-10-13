package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/aio"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type Evidence struct {
	Status   string           `json:"status"`
	BaseHead string           `json:"baseHead"`
	Patch    string           `json:"patch,omitempty"`
	Changed  []string         `json:"changed,omitempty"`
	Checks   []map[string]any `json:"checks,omitempty"`
	Error    string           `json:"error,omitempty"`
	Stopped  bool             `json:"stopped"`
}

type Service struct {
	Config      config.Config
	DB          *pgxpool.Pool
	HTTP        *http.Client
	Workspaces  workspace.Provider
	Projects    *project.Service
	Permissions *access.Service
	Runtime     *agentic.Client
}

func New(
	cfg config.Config,
	db *pgxpool.Pool,
	h *http.Client,
	ws workspace.Provider,
	projects *project.Service,
	permissions *access.Service,
) *Service {
	return &Service{
		Config:      cfg,
		DB:          db,
		HTTP:        h,
		Workspaces:  ws,
		Projects:    projects,
		Permissions: permissions,
		Runtime:     agentic.New(cfg.RuntimeURL, cfg.RuntimeToken, h),
	}
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func evaluationNames(id string) (string, string) { return "eval-" + id, "eval-" + id + "-result" }

func (s *Service) repository(
	ctx context.Context,
	req codingv1.EvaluationEnvironmentRequest,
) (workspace.Repository, error) {
	level, err := s.Permissions.Level(ctx, req.Scope.ActorID, req.Scope.TenantID, req.Scope.ProjectID)
	if err != nil {
		return workspace.Repository{}, err
	}
	if level < 30 {
		return workspace.Repository{}, serrors.NewError(403, "EXECUTION_REVOKED", "项目执行权限已撤销")
	}
	return s.Projects.Repository(ctx, req.Scope.TenantID, req.Scope.ProjectID, req.Scenario.BaseHead, "evaluation")
}

func (s *Service) Environment(
	gc *core.Context,
	id string,
	req codingv1.EvaluationEnvironmentRequest,
) (Evidence, error) {
	ctx := gc.Request.Context()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(req.Scenario.BaseHead) ||
		req.Scope.TenantID == "" ||
		req.Scope.ProjectID == "" ||
		req.Scope.ActorID == "" ||
		(req.Phase != "prepare" && req.Phase != "verify" && req.Phase != "stop") {
		return Evidence{}, serrors.NewError(400, "INVALID_EVALUATION", "评测环境配置无效")
	}
	cfg := project.Settings{
		AgentID:        "evaluation",
		AgentVersion:   1,
		BaseBranch:     "evaluation",
		AllowedPaths:   req.Scenario.AllowedPaths,
		Checks:         req.Scenario.Checks,
		RepairLimit:    1,
		TimeoutMinutes: 30,
	}
	if err := cfg.Validate(); err != nil {
		return Evidence{}, err
	}
	if s.Workspaces == nil {
		return Evidence{}, serrors.NewError(503, "WORKSPACE_UNAVAILABLE", "独立代码环境未配置")
	}
	phase := req.Phase
	req.Phase = ""
	raw := encode(req)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Evidence{}, err
	}
	defer tx.Rollback(ctx)
	if err = table.LockEvaluationEnvironment(ctx, tx, id); err != nil {
		return Evidence{}, err
	}
	evidence := Evidence{Status: "Preparing", BaseHead: req.Scenario.BaseHead}
	if err = table.InsertEvaluationEnvironment(ctx, s.DB, id, raw, encode(evidence)); err != nil {
		return evidence, err
	}
	stored, err := table.GetEvaluationEnvironment(ctx, tx, id)
	if err != nil {
		return evidence, err
	}
	var frozen codingv1.EvaluationEnvironmentRequest
	if err = json.Unmarshal(stored.Request, &frozen); err != nil {
		return evidence, err
	}
	if string(encode(frozen)) != string(raw) {
		return evidence, serrors.NewError(409, "EVALUATION_CONFLICT", "Trial 已绑定另一份评测输入")
	}
	if err = json.Unmarshal(stored.Evidence, &evidence); err != nil {
		return evidence, err
	}
	artifactRaw := []byte(stored.Artifact)
	task, round := evaluationNames(id)
	committed := false
	save := func() (Evidence, error) {
		var db table.DB = tx
		if committed {
			db = s.DB
		}
		if err := table.UpdateEvaluationEvidence(ctx, db, id, encode(evidence)); err != nil {
			return evidence, err
		}
		if !committed {
			if err := tx.Commit(ctx); err != nil {
				return evidence, err
			}
		}
		return evidence, nil
	}
	stop := func() error {
		// Persist evidence before deleting its source artifacts so retries only repeat cleanup.
		if err := table.UpdateEvaluationEvidence(ctx, tx, id, encode(evidence)); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
		if err := s.Workspaces.CleanupEvaluation(ctx, task, round); err != nil {
			return err
		}
		evidence.Stopped = true
		return nil
	}
	if evidence.Stopped {
		return save()
	}
	if evidence.Status == "Completed" || evidence.Status == "Failed" || evidence.Status == "Stopped" {
		if err := stop(); err != nil {
			return evidence, err
		}
		return save()
	}
	if phase == "stop" {
		evidence.Status = "Stopped"
		if err := stop(); err != nil {
			return evidence, err
		}
		return save()
	}
	repository, err := s.repository(ctx, req)
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Repository or project execution permission unavailable"
		if err := stop(); err != nil {
			return evidence, err
		}
		return save()
	}
	if phase == "prepare" {
		if evidence.Status == "Ready" {
			return save()
		}
		endpoint, prepareErr := s.Workspaces.Ensure(ctx, task, round, repository)
		err = prepareErr
		if errors.Is(err, workspace.ErrPreparing) {
			return save()
		}
		if err != nil {
			evidence.Status = "Failed"
			evidence.Error = "Repository environment preparation failed"
			if err := stop(); err != nil {
				return evidence, err
			}
			return save()
		}
		if err = aio.Ready(ctx, s.HTTP, endpoint); err != nil {
			var unavailable *serrors.Error
			if errors.As(err, &unavailable) && unavailable.Status == 503 {
				return save()
			}
			evidence.Status = "Failed"
			evidence.Error = "Repository environment service rejected access"
			if err := stop(); err != nil {
				return evidence, err
			}
			return save()
		}
		evidence.Status = "Ready"
		return save()
	}
	if evidence.Status != "Ready" && evidence.Status != "Verifying" {
		return evidence, serrors.NewError(409, "EVALUATION_NOT_READY", "代码环境尚未就绪")
	}
	evidence.Status = "Verifying"
	if len(artifactRaw) == 0 {
		if err = s.Workspaces.Pause(ctx, task, round); err != nil {
			return evidence, err
		}
		env := map[string]string{
			"REPOSITORY_URL": repository.URL,
			"BASE_HEAD":      req.Scenario.BaseHead,
			"WORK_BRANCH":    "evaluation",
			"ALLOWED_PATHS":  string(encode(req.Scenario.AllowedPaths)),
			"SUMMARY":        "Coding evaluation candidate",
			"COMMIT_DATE":    "2000-01-01T00:00:00Z",
		}
		artifactRaw, err = s.Workspaces.RunJob(ctx, task, round, "snapshot", env)
		if errors.Is(err, workspace.ErrPreparing) {
			return save()
		}
		if err != nil {
			evidence.Status = "Failed"
			evidence.Error = "Candidate snapshot failed; check allowed paths and repository changes"
			if err := stop(); err != nil {
				return evidence, err
			}
			return save()
		}
		if err = table.UpdateEvaluationArtifact(ctx, tx, id, artifactRaw); err != nil {
			return evidence, err
		}
	}
	var artifact workspace.Artifact
	if err = json.Unmarshal(artifactRaw, &artifact); err != nil {
		return evidence, err
	}
	evidence.Patch = artifact.Patch
	evidence.Changed = artifact.Changed
	endpoint, err := s.Workspaces.Verifier(ctx, task, round)
	if errors.Is(err, workspace.ErrPreparing) {
		return save()
	}
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Independent verification environment failed"
		if err := stop(); err != nil {
			return evidence, err
		}
		return save()
	}
	// Container readiness can precede Service endpoint propagation. Probe the
	// control API before starting any command so this retry has no side effects.
	if err = aio.Ready(ctx, s.HTTP, endpoint); err != nil {
		var unavailable *serrors.Error
		if errors.As(err, &unavailable) && unavailable.Status == 503 {
			return save()
		}
		evidence.Status = "Failed"
		evidence.Error = "Independent verification service rejected access"
		if err := stop(); err != nil {
			return evidence, err
		}
		return save()
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 120*time.Second)
	checks, err := aio.RunChecks(checkCtx, endpoint, req.Scenario.Checks)
	checkCancel()
	if err != nil {
		evidence.Status = "Failed"
		evidence.Error = "Independent verification did not return exit codes"
		logger.FromContext(ctx).
			Errorf(ctx, "coding evaluation verification failed: trial=%s: %v", id, err)
	} else {
		evidence.Checks = checks
		evidence.Status = "Completed"
	}
	if err := stop(); err != nil {
		return evidence, err
	}
	return save()
}

func (s *Service) identity(gc *core.Context) (string, codingv1.EvaluationEnvironmentRequest, error) {
	r := gc.Request
	var scope codingv1.ToolScope
	if err := json.Unmarshal([]byte(r.Header.Get("X-Agentic-Scope")), &scope); err != nil {
		return "", codingv1.EvaluationEnvironmentRequest{}, serrors.NewError(403, "INVALID_SCOPE", "评测身份无效")
	}
	id := strings.TrimPrefix(scope.TaskID, "eval:")
	if id == scope.TaskID || scope.RoundID != "1" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		return "", codingv1.EvaluationEnvironmentRequest{}, serrors.NewError(403, "INVALID_SCOPE", "评测身份无效")
	}
	stored, err := table.GetEvaluationEnvironment(r.Context(), s.DB, id)
	if err != nil {
		return "", codingv1.EvaluationEnvironmentRequest{}, serrors.NewError(403, "INVALID_SCOPE", "评测环境不存在")
	}
	var req codingv1.EvaluationEnvironmentRequest
	var evidence Evidence
	if err := json.Unmarshal(stored.Request, &req); err != nil {
		return "", req, err
	}
	if err := json.Unmarshal(stored.Evidence, &evidence); err != nil {
		return "", req, err
	}
	if evidence.Status != "Ready" || evidence.Stopped || scope.TenantID != req.Scope.TenantID ||
		scope.ProjectID != req.Scope.ProjectID ||
		scope.ActorID != req.Scope.ActorID {
		return "", req, serrors.NewError(409, "EXECUTION_REVOKED", "评测环境已停止或身份不匹配")
	}
	epoch, err := strconv.ParseInt(r.Header.Get("X-Agentic-Epoch"), 10, 64)
	if err != nil || epoch < 1 {
		return "", req, serrors.NewError(403, "INVALID_EPOCH", "执行轮次无效")
	}
	runtime := r.Header.Get("X-Agentic-Run-ID")
	allowed, err := s.Runtime.EvaluationLease(r.Context(), id, runtime, epoch)
	if err != nil {
		return "", req, err
	}
	if !allowed {
		return "", req, serrors.NewError(409, "EXECUTION_REVOKED", "评测执行权已失效")
	}
	repository, err := s.repository(r.Context(), req)
	if err != nil {
		return "", req, err
	}
	task, round := evaluationNames(id)
	if _, err = s.Workspaces.Ensure(r.Context(), task, round, repository); err != nil {
		if errors.Is(err, workspace.ErrPreparing) {
			return "", req, serrors.NewError(503, "SANDBOX_PREPARING", "代码环境正在恢复")
		}
		return "", req, err
	}
	if err = s.Workspaces.Fence(r.Context(), task, round, epoch); err != nil {
		if errors.Is(err, workspace.ErrPreparing) {
			return "", req, serrors.NewError(503, "SANDBOX_FENCING", "等待旧执行环境停止")
		}
		return "", req, err
	}
	return id, req, nil
}

func (s *Service) Reap(ctx context.Context) error {
	if s.Workspaces == nil {
		return nil
	}
	ids, err := table.ExpiredEvaluationEnvironments(ctx, s.DB)
	if err != nil {
		return err
	}
	for _, id := range ids {
		task, round := evaluationNames(id)
		stopCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err = s.Workspaces.CleanupEvaluation(stopCtx, task, round)
		cancel()
		if err != nil {
			return err
		}
		err = table.ExpireEvaluationEnvironment(ctx, s.DB, id)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Sandbox(gc *core.Context) (workspace.Endpoint, error) {
	id, _, err := s.identity(gc)
	if err != nil {
		return workspace.Endpoint{}, err
	}
	if id != gc.Request.PathValue("trial") {
		return workspace.Endpoint{}, serrors.NewError(403, "INVALID_SCOPE", "环境不匹配")
	}
	path := gc.Request.PathValue("path")
	if path != "/v1/sandbox" && !strings.HasPrefix(path, "/v1/file/") && !strings.HasPrefix(path, "/v1/bash/") {
		return workspace.Endpoint{}, serrors.NewError(404, "NOT_FOUND", "接口不存在")
	}
	task, _ := evaluationNames(id)
	return s.Workspaces.Access(gc.Request.Context(), task)
}

func (s *Service) Finish(gc *core.Context, finish codingv1.EvaluationFinishRequest) (map[string]any, error) {
	if _, _, err := s.identity(gc); err != nil {
		return nil, err
	}
	if strings.TrimSpace(finish.Summary) == "" {
		return nil, serrors.NewError(400, "INVALID_FINISH", "需要结果摘要")
	}
	return map[string]any{
		"accepted":     true,
		"summary":      finish.Summary,
		"verification": "pending independent checks",
	}, nil
}

func (s *Service) Checks(gc *core.Context) (map[string]any, error) {
	id, req, err := s.identity(gc)
	if err != nil {
		return nil, err
	}
	task, _ := evaluationNames(id)
	endpoint, err := s.Workspaces.Access(gc.Request.Context(), task)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(gc.Request.Context(), 120*time.Second)
	defer cancel()
	checks, err := aio.RunChecks(ctx, endpoint, req.Scenario.Checks)
	if err != nil {
		return nil, err
	}
	return map[string]any{"checks": checks}, nil
}
