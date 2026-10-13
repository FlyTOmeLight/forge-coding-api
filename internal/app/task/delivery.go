package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/aio"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type deliveryState struct {
	Finish   json.RawMessage           `json:"finishRequest"`
	Baseline []map[string]any          `json:"baseline"`
	Artifact *workspace.Artifact       `json:"artifact"`
	Checks   []map[string]any          `json:"deliveryChecks"`
	Receipt  *workspace.PublishReceipt `json:"publication"`
}

func (a *Service) publicationPermit(gc *core.Context) (codingv1.PermitResponse, error) {
	r := gc.Request
	var t table.Task
	identity, err := table.PublicationIdentity(r.Context(), a.DB, r.PathValue("run"))
	if err != nil {
		return codingv1.PermitResponse{}, serrors.NewError(
			409,
			"STALE_EXECUTION",
			"Delivery round is not active",
		)
	}
	t, err = table.FindTask(r.Context(), a.DB, identity.TaskID)
	if err != nil {
		return codingv1.PermitResponse{}, err
	}
	level, err := a.Permissions.Level(r.Context(), t.OwnerID, t.TenantID, t.ProjectID)
	if err != nil {
		return codingv1.PermitResponse{}, err
	}
	if level < 30 || t.Control != "Agent" || t.Epoch != identity.Epoch || t.ActiveRun == nil ||
		*t.ActiveRun != r.PathValue("run") {
		return codingv1.PermitResponse{}, serrors.NewError(
			409,
			"EXECUTION_REVOKED",
			"Publication permission was revoked",
		)
	}
	return codingv1.PermitResponse{Allowed: true}, nil
}

func (a *Service) reconcileDelivery(ctx context.Context, id string) (resultErr error) {
	var taskID string
	defer func() {
		var failed *workspace.OperationFailed
		if errors.As(resultErr, &failed) && failed.Phase != "publish" {
			if stopErr := a.Workspaces.StopAll(ctx, taskID, id); stopErr != nil {
				resultErr = stopErr
				return
			}
			identity, err := table.RunRuntimeIdentity(ctx, a.DB, id)
			if err != nil {
				resultErr = err
				return
			}
			_, resultErr = a.recordDelivery(
				ctx,
				identity.TaskID,
				taskRunRequest{
					RunID:  id,
					Worker: identity.RuntimeID,
					Epoch:  identity.Epoch,
					Status: "Stopped",
					Output: mustJSON(
						map[string]any{"runtimeStatus": "Failed", "error": failed.Error()},
					),
				},
			)
		}
	}()
	delivery, err := table.DeliveryRun(ctx, a.DB, id)
	if err != nil {
		return err
	}
	taskID = delivery.TaskID
	t, err := table.FindTask(ctx, a.DB, taskID)
	if err != nil {
		return err
	}
	level, err := a.Permissions.Level(ctx, t.OwnerID, t.TenantID, t.ProjectID)
	if err != nil {
		return err
	}
	if t.Control != "Agent" || t.Epoch != delivery.Epoch || level < 30 {
		return errors.New("delivery permission was revoked")
	}
	var cfg project.Settings
	var state deliveryState
	if err = json.Unmarshal(delivery.ConfigSnapshot, &cfg); err != nil {
		return err
	}
	if err = json.Unmarshal(delivery.Output, &state); err != nil {
		return err
	}
	var finish struct {
		Summary  string          `json:"summary"`
		Judgment json.RawMessage `json:"judgment"`
	}
	if err = json.Unmarshal(state.Finish, &finish); err != nil {
		return err
	}
	repository, err := a.Projects.Repository(ctx, t.TenantID, t.ProjectID, t.Head, t.Branch)
	if err != nil {
		return err
	}
	scopes, _ := json.Marshal(cfg.AllowedPaths)
	env := map[string]string{
		"REPOSITORY_URL":       repository.URL,
		"BASE_HEAD":            t.Head,
		"WORK_BRANCH":          t.Branch,
		"ALLOWED_PATHS":        string(scopes),
		"SUMMARY":              finish.Summary,
		"COMMIT_DATE":          delivery.CreatedAt.UTC().Format(time.RFC3339),
		"EXPECTED_REMOTE_HEAD": t.Head,
		"PERMIT_URL": strings.TrimRight(
			a.Config.SandboxGatewayURL,
			"/",
		) + "/internal/v1/rounds/" + id + "/publication-permit",
	}
	if t.MRIID == 0 {
		project, err := a.Permissions.Resolve(ctx, t.TenantID, t.ProjectID)
		if err != nil {
			return err
		}
		_, err = a.GitLab.Branch(ctx, project.GitLabID, t.Branch)
		if gitlab.IsStatus(err, 404) {
			env["EXPECTED_REMOTE_HEAD"] = ""
		} else if err != nil {
			return err
		}
	}

	save := func(key string, value any) error {
		_, err := table.SaveDeliveryEvidence(ctx, a.DB, id, key, mustJSON(value))
		return err
	}
	if state.Artifact == nil {
		if err = a.Workspaces.Pause(ctx, t.ID, id); err != nil {
			return err
		}
		raw, err := a.Workspaces.RunJob(ctx, t.ID, id, "snapshot", env)
		if errors.Is(err, workspace.ErrPreparing) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.Workspaces.Resume(ctx, t.ID, id); err != nil {
			return err
		}
		var artifact workspace.Artifact
		if err = json.Unmarshal(raw, &artifact); err != nil {
			return err
		}
		if err = save("expectedHead", artifact.ExpectedHead); err != nil {
			return err
		}
		return save("artifact", artifact)
	}
	if state.Checks == nil {
		endpoint, err := a.Workspaces.Verifier(ctx, t.ID, id)
		if errors.Is(err, workspace.ErrPreparing) {
			return nil
		}
		if err != nil {
			return err
		}
		checks, err := aio.RunChecks(ctx, endpoint, cfg.Checks)
		if err != nil {
			return err
		}
		if err = save("deliveryChecks", checks); err != nil {
			return err
		}
		return a.Workspaces.Stop(ctx, id+"-verify")
	}
	if err = a.Workspaces.Stop(ctx, id+"-verify"); err != nil {
		return err
	}
	if state.Receipt == nil {
		if err = a.Workspaces.Pause(ctx, t.ID, id); err != nil {
			return err
		}
		raw, err := a.Workspaces.RunJob(ctx, t.ID, id, "publish", env)
		if errors.Is(err, workspace.ErrPreparing) {
			return nil
		}
		if err != nil {
			var failed *workspace.OperationFailed
			if !errors.As(err, &failed) {
				return err
			}
			project, lookupErr := a.Permissions.Resolve(ctx, t.TenantID, t.ProjectID)
			if lookupErr != nil {
				return lookupErr
			}
			branch, lookupErr := a.GitLab.Branch(ctx, project.GitLabID, t.Branch)
			if lookupErr != nil && !gitlab.IsStatus(lookupErr, 404) {
				return lookupErr
			}
			if branch.Commit.ID == state.Artifact.ExpectedHead {
				if err = a.Workspaces.Stop(ctx, t.ID); err != nil {
					return err
				}
				return save(
					"publication",
					workspace.PublishReceipt{
						Head:    branch.Commit.ID,
						Tree:    state.Artifact.Tree,
						Patch:   state.Artifact.Patch,
						Changed: len(state.Artifact.Changed) > 0,
					},
				)
			}
			if stopErr := a.Workspaces.StopAll(ctx, taskID, id); stopErr != nil {
				return stopErr
			}
			_, recordErr := a.recordDelivery(
				ctx,
				t.ID,
				taskRunRequest{
					RunID:  id,
					Worker: delivery.RuntimeID,
					Epoch:  delivery.Epoch,
					Status: "Stopped",
					Output: mustJSON(
						map[string]any{
							"runtimeStatus": "Failed",
							"error":         failed.Error(),
							"remoteHead":    branch.Commit.ID,
						},
					),
				},
			)
			return recordErr
		}
		if err = a.Workspaces.Resume(ctx, t.ID, id); err != nil {
			return err
		}
		var receipt workspace.PublishReceipt
		if err = json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		return save("publication", receipt)
	}
	if !state.Receipt.Changed && t.MRIID == 0 {
		return &workspace.OperationFailed{
			Phase:  "delivery",
			Detail: "No code changes; an empty MR cannot be delivery.CreatedAt",
		}
	}
	project, err := a.Permissions.Resolve(ctx, t.TenantID, t.ProjectID)
	if err != nil {
		return err
	}
	iid := t.MRIID
	if iid == 0 {
		// Query by source branch before creation to reconcile an earlier successful
		// MR creation whose local result was lost.
		existing, err := a.GitLab.SourceMergeRequests(ctx, project.GitLabID, t.Branch)
		if err != nil {
			return err
		}
		if len(existing) > 1 {
			return &workspace.OperationFailed{
				Phase:  "delivery",
				Detail: "Multiple MRs exist for the task branch",
			}
		}
		if len(existing) == 1 {
			iid = existing[0].IID
		} else {
			mr, err := a.GitLab.CreateDraftMergeRequest(
				ctx,
				project.GitLabID,
				t.Branch,
				t.TargetBranch,
				t.Title,
				finish.Summary,
			)
			if err != nil {
				return err
			}
			iid = mr.IID
		}
	}
	passed := true
	for _, check := range state.Checks {
		if ok, _ := check["passed"].(bool); !ok {
			passed = false
		}
	}
	status := "Passed"
	if !passed {
		status = "Failed"
	}
	if err = a.Workspaces.Published(ctx, t.ID, state.Receipt.Head); err != nil {
		return err
	}
	body := taskRunRequest{
		RunID:  id,
		Worker: delivery.RuntimeID,
		Epoch:  delivery.Epoch,
		Head:   state.Receipt.Head,
		MRIID:  iid,
		Status: status,
		Output: mustJSON(
			map[string]any{
				"patch":         state.Receipt.Patch,
				"checksCommit":  state.Receipt.Head,
				"configVersion": cfg.Version,
				"baseline":      state.Baseline,
				"summary":       finish.Summary,
				"judgment":      finish.Judgment,
				"checks":        []map[string]any{{"passed": passed, "commands": state.Checks}},
				"candidateTree": state.Artifact.Tree,
			},
		),
	}
	_, err = a.recordDelivery(ctx, t.ID, body)
	return err
}
