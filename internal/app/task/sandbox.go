package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/aio"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type sandboxScope = codingv1.ToolScope

func (a *Service) sandboxIdentity(r *http.Request) (table.Task, taskRunRequest, error) {
	var scope sandboxScope
	if err := json.Unmarshal([]byte(r.Header.Get("X-Agentic-Scope")), &scope); err != nil {
		return table.Task{}, taskRunRequest{}, serrors.NewError(400, "INVALID_SCOPE", "Missing execution scope")
	}
	runtime := r.Header.Get("X-Agentic-Run-ID")
	attempt, err := strconv.ParseInt(r.Header.Get("X-Agentic-Epoch"), 10, 64)
	if err != nil || attempt < 1 || runtime == "" || scope.RoundID == "" || scope.TaskID == "" {
		return table.Task{}, taskRunRequest{}, serrors.NewError(
			400,
			"INVALID_SCOPE",
			"Missing execution identity",
		)
	}
	identity, err := table.RunningIdentity(r.Context(), a.DB, scope.RoundID, runtime)
	if err != nil {
		return table.Task{}, taskRunRequest{}, serrors.NewError(
			409,
			"STALE_EXECUTION",
			"Business round is no longer active",
		)
	}
	if identity.TaskID != scope.TaskID {
		return table.Task{}, taskRunRequest{}, serrors.NewError(
			403,
			"SCOPE_MISMATCH",
			"Execution task mismatch",
		)
	}
	b := taskRunRequest{RunID: scope.RoundID, Epoch: identity.Epoch, Worker: runtime, RuntimeEpoch: attempt}
	// The bound gateway takes over the role of the old tool's start callback.
	if _, err = table.ClaimRunningRun(r.Context(), a.DB, scope.RoundID, runtime); err != nil {
		return table.Task{}, b, err
	}
	task, err := a.checkGate(r.Context(), identity.TaskID, b)
	if err != nil {
		return task, b, err
	}
	if task.TenantID != scope.TenantID || task.ProjectID != scope.ProjectID {
		return task, b, serrors.NewError(403, "SCOPE_MISMATCH", "Execution project mismatch")
	}
	return task, b, nil
}

func (a *Service) prepareSandbox(ctx context.Context, t table.Task, round string) (workspace.Endpoint, error) {
	if a.Workspaces == nil {
		return workspace.Endpoint{}, errors.New("AIO workspace provider is not configured")
	}
	repository, err := a.Projects.Repository(ctx, t.TenantID, t.ProjectID, t.Head, t.Branch)
	if err != nil {
		return workspace.Endpoint{}, err
	}
	return a.Workspaces.Ensure(ctx, t.ID, round, repository)
}

func (a *Service) sandboxEndpoint(gc *core.Context) (workspace.Endpoint, string, error) {
	r := gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		return workspace.Endpoint{}, "", err
	}
	if r.PathValue("run") != b.RunID {
		return workspace.Endpoint{}, "", serrors.NewError(403, "SCOPE_MISMATCH", "Sandbox round mismatch")
	}
	apiPath := r.PathValue("path")
	if apiPath != "/v1/sandbox" && !strings.HasPrefix(apiPath, "/v1/file/") &&
		!strings.HasPrefix(apiPath, "/v1/bash/") {
		return workspace.Endpoint{}, "", serrors.NewError(
			403,
			"TOOL_NOT_ALLOWED",
			"Sandbox capability is not enabled",
		)
	}
	if a.Workspaces == nil {
		return workspace.Endpoint{}, "", serrors.NewError(503, "SANDBOX_UNAVAILABLE", "AIO is not configured")
	}
	endpoint, err := a.prepareSandbox(r.Context(), task, b.RunID)
	if err != nil {
		return workspace.Endpoint{}, "", serrors.NewError(503, "SANDBOX_PREPARING", err.Error())
	}
	if err = a.Workspaces.Fence(r.Context(), task.ID, b.RunID, b.RuntimeEpoch); err != nil {
		return workspace.Endpoint{}, "", serrors.NewError(503, "SANDBOX_FENCING", err.Error())
	}

	return endpoint, apiPath, nil
}

func (a *Service) submitFinish(gc *core.Context, body codingv1.FinishRequest) (codingv1.FinishResponse, error) {
	r := gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		return codingv1.FinishResponse{}, err
	}
	if strings.TrimSpace(body.Summary) == "" || strings.TrimSpace(body.Judgment.Summary) == "" ||
		(body.Judgment.Status != "Met" && body.Judgment.Status != "Unmet" && body.Judgment.Status != "NeedsHuman") {
		return codingv1.FinishResponse{}, serrors.NewError(
			422,
			"INVALID_TOOL_INPUT",
			"summary and valid judgment are required",
		)
	}
	operation := r.Header.Get("Idempotency-Key")
	if !strings.HasPrefix(operation, b.Worker+":") {
		return codingv1.FinishResponse{}, serrors.NewError(
			400,
			"INVALID_OPERATION",
			"Missing stable tool operation identity",
		)
	}
	request, _ := json.Marshal(body)
	result, err := table.SubmitRunFinish(r.Context(), a.DB, b.RunID, request, operation)
	if err != nil {
		return codingv1.FinishResponse{}, err
	}
	if result.RowsAffected() != 1 {
		return codingv1.FinishResponse{}, serrors.NewError(
			409,
			"FINISH_CONFLICT",
			"Different delivery request already exists",
		)
	}
	return codingv1.FinishResponse{Accepted: true, RoundID: b.RunID, TaskID: task.ID}, nil
}

func (a *Service) sandboxChecks(gc *core.Context) (map[string]any, error) {
	r := gc.Request
	task, b, err := a.sandboxIdentity(r)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if raw, err = table.RunSettings(r.Context(), a.DB, b.RunID); err != nil {
		return nil, err
	}
	var cfg project.Settings
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	counted, err := table.CountCheckAttempt(r.Context(), a.DB, b.RunID, cfg.RepairLimit+1)
	if err != nil {
		return nil, err
	}
	if counted.RowsAffected() != 1 {
		return map[string]any{
			"error": map[string]any{
				"code":    "REPAIR_LIMIT_REACHED",
				"message": "Project check budget exhausted. Submit finish with the remaining failures.",
			},
		}, nil
	}

	endpoint, err := a.Workspaces.Access(r.Context(), task.ID)
	if err != nil {
		return nil, err
	}
	checks, err := aio.RunChecks(r.Context(), endpoint, cfg.Checks)
	if err != nil {
		return nil, serrors.NewError(503, "CHECK_OUTCOME_UNKNOWN", fmt.Sprint(err))
	}
	return map[string]any{"checks": checks}, nil
}
