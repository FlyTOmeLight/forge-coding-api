package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/aio"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

func (a *Service) RunDispatcher(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	synced := make(chan struct{})
	go func() { defer close(synced); a.runGitLabSync(ctx) }()
	defer func() { cancel(); <-synced }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if e := a.reconcileRuns(ctx); e != nil && ctx.Err() == nil {
				logger.FromContext(ctx).Errorf(ctx, "execution reconciliation failed: %v", e)
			}
		}
	}
}

func (a *Service) reconcileRuns(ctx context.Context) error {
	ids, e := table.ListActiveRunIDs(ctx, a.DB)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = a.reconcileRound(ctx, id); e != nil && !errors.Is(e, workspace.ErrPreparing) {
			logger.FromContext(ctx).Errorf(ctx, "round reconciliation failed: round=%s: %v", id, e)
		}
	}
	return a.reapSandboxes(ctx)
}

func (a *Service) reconcileRound(ctx context.Context, id string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var taskID string
	if taskID, e = table.RunTaskID(ctx, tx, id); e != nil {
		return e
	}
	t, e := table.TryLockTask(ctx, tx, taskID)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	runRow, e := table.LockRunExecution(ctx, tx, id)
	if e != nil {
		return e
	}
	runtimeID, status, requirements, raw, execution := runRow.RuntimeID, runRow.Status, runRow.Requirements, runRow.ConfigSnapshot, runRow.ExecutionSnapshot
	if status != "Queued" && status != "Running" && status != "Stopping" && status != "Delivering" {
		return nil
	}
	level, e := a.Permissions.Level(ctx, t.OwnerID, t.TenantID, t.ProjectID)
	if e != nil {
		return e
	}
	if level < 30 && t.Control == "Agent" {
		t.Control = "Stopping"
		t.Epoch++
		t.StopConfirmed = false
		now := time.Now().UTC()
		t.StopRequestedAt = &now
		status = "Stopping"
		if _, e = table.MarkRunStopping(ctx, tx, id); e != nil {
			return e
		}
		if e = table.WriteTask(ctx, tx, t); e != nil {
			return e
		}
		if e = table.TaskEvent(ctx, tx, t.ID, "authorization", "ExecutionRevoked", map[string]any{}); e != nil {
			return e
		}
	}
	if status == "Delivering" {
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		return a.reconcileDelivery(ctx, id)
	}

	if runtimeID == nil {
		if status == "Stopping" || t.Control != "Agent" {
			if e = a.Workspaces.StopAll(ctx, t.ID, id); e != nil {
				return e
			}
			if _, e = table.StopRun(ctx, tx, id); e != nil {
				return e
			}
			t.ActiveRun = nil
			t.StopConfirmed = true
			t.Control = "Human"
			if t.Status == "Canceling" {
				t.Status = "Canceled"
			} else if t.Status == "Running" || t.Status == "Queued" {
				t.Status = "Review"
			}
			if e = table.WriteTask(ctx, tx, t); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		{
			endpoint, prepareErr := a.prepareSandbox(ctx, t, id)
			if prepareErr != nil {
				return prepareErr
			}
			if _, e = table.ActivateTaskSandbox(ctx, tx, t.ID); e != nil {
				return e
			}
			var output map[string]json.RawMessage
			var stored []byte
			if stored, e = table.RunOutput(ctx, tx, id); e != nil {
				return e
			}
			if e = json.Unmarshal(stored, &output); e != nil {
				return e
			}
			if len(output["baseline"]) == 0 {
				var config project.Settings
				if e = json.Unmarshal(raw, &config); e != nil {
					return e
				}
				baseline, err := aio.RunChecks(ctx, endpoint, config.Checks)
				if err != nil {
					return err
				}
				if _, e = table.SaveRunBaseline(ctx, tx, id, mustJSON(baseline)); e != nil {
					return e
				}
			}
		}
		if len(execution) == 0 {
			var config project.Settings
			if e = json.Unmarshal(raw, &config); e != nil {
				return e
			}
			execution, e = a.executionSnapshot(ctx, t, id, requirements, config)
			if e != nil {
				return e
			}
			if _, e = table.SaveExecutionSnapshot(ctx, tx, id, execution); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		run, e := a.Runtime.Submit(ctx, id, execution)
		if e != nil {
			return e
		}
		if _, e = table.StartRun(ctx, tx, id, run.ID); e != nil {
			return e
		}
		t.Status = "Running"
		if e = table.WriteTask(ctx, tx, t); e != nil {
			return e
		}
		if e = table.TaskEvent(
			ctx,
			tx,
			t.ID,
			"runtime",
			"ExecutionSubmitted",
			map[string]any{"roundId": id, "runtimeId": run.ID},
		); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if status == "Stopping" || t.Control != "Agent" {
		if e = a.Runtime.Cancel(ctx, *runtimeID); e != nil {
			return e
		}
	}
	run, e := a.Runtime.GetRun(ctx, *runtimeID)
	if e != nil {
		return e
	}
	if run.Status == "Canceled" || run.Status == "Failed" || run.Status == "NeedsReview" ||
		run.Status == "Completed" && (status == "Stopping" || t.Control != "Agent") {
		if err := a.Workspaces.StopAll(ctx, t.ID, id); err != nil {
			return err
		}
		var roundEpoch int64
		if roundEpoch, e = table.RunEpoch(ctx, tx, id); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		_, e = a.recordDelivery(
			ctx,
			t.ID,
			taskRunRequest{
				RunID:  id,
				Worker: *runtimeID,
				Epoch:  roundEpoch,
				Status: "Stopped",
				Output: mustJSON(
					map[string]any{
						"runtimeStatus": run.Status,
						"error":         run.Error,
						"summary":       run.Output,
					},
				),
			},
		)
		return e
	}
	if run.Status == "Completed" {
		var hasFinish bool
		if hasFinish, e = table.RunHasFinish(ctx, tx, id); e != nil {
			return e
		}
		if hasFinish {
			if _, e = table.MarkRunDelivering(ctx, tx, id); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
	}
	if run.Status == "Completed" {
		if e = a.Workspaces.StopAll(ctx, t.ID, id); e != nil {
			return e
		}
		var roundEpoch int64
		if roundEpoch, e = table.RunEpoch(ctx, tx, id); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		_, e = a.recordDelivery(
			ctx,
			t.ID,
			taskRunRequest{
				RunID:  id,
				Worker: *runtimeID,
				Epoch:  roundEpoch,
				Status: "Stopped",
				Output: mustJSON(
					map[string]any{
						"runtimeStatus": "Failed",
						"error":         "Agent ended without submitting finish",
						"summary":       run.Output,
					},
				),
			},
		)
		return e
	}

	return tx.Commit(ctx)
}

func mustJSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

func (a *Service) executionSnapshot(
	ctx context.Context,
	t table.Task,
	round, requirements string,
	c project.Settings,
) ([]byte, error) {
	e := a.Projects.RequireExecutor(ctx, t.TenantID, t.ProjectID)
	if e != nil {
		return nil, e
	}
	var discussions []struct {
		ID       string `json:"id"`
		Resolved bool   `json:"resolved"`
		Notes    []struct {
			ID       int64           `json:"id"`
			Body     string          `json:"body"`
			System   bool            `json:"system"`
			Position json.RawMessage `json:"position"`
			Author   struct {
				Username string `json:"username"`
			} `json:"author"`
		} `json:"notes"`
	}
	if len(t.Discussions) > 0 {
		if e := json.Unmarshal(t.Discussions, &discussions); e != nil {
			return nil, e
		}
	}
	comments := []map[string]any{}
	for _, discussion := range discussions {
		for _, note := range discussion.Notes {
			if note.System || note.Body == "" {
				continue
			}
			comments = append(
				comments,
				map[string]any{
					"discussionID": discussion.ID,
					"noteID":       note.ID,
					"author":       note.Author.Username,
					"body":         note.Body,
					"position":     note.Position,
					"resolved":     discussion.Resolved,
				},
			)
		}
	}
	query, e := json.Marshal(
		map[string]any{
			"requirements": requirements,
			"task": map[string]any{
				"title":       t.Title,
				"description": t.Description,
				"criteria":    t.Criteria,
				"reproduce":   t.Reproduce,
				"constraints": t.Constraints,
				"reference":   t.Reference,
				"head":        t.Head,
			},
			"comments": comments,
			"workspace": map[string]any{
				"path":         "/workspace/repo",
				"allowedPaths": c.AllowedPaths,
				"checks":       c.Checks,
				"repairLimit":  c.RepairLimit,
			},
		},
	)
	if e != nil {
		return nil, e
	}
	spec := map[string]any{
		"scope": map[string]any{
			"tenantID":  t.TenantID,
			"projectID": t.ProjectID,
			"actorID":   t.OwnerID,
			"taskID":    t.ID,
			"roundID":   round,
		},
		"agentID":      c.AgentID,
		"agentVersion": c.AgentVersion,
		"query":        string(query),
	}
	{
		spec["filesystem"] = map[string]any{
			"provider": "AIO",
			"url": strings.TrimRight(
				a.Config.SandboxGatewayURL,
				"/",
			) + "/internal/v1/rounds/" + round + "/sandbox",
			"tokenEnv":  "CODING_TOOLS_TOKEN",
			"workspace": "/workspace/repo",
		}
	}
	return json.Marshal(map[string]any{"spec": spec, "timeoutSeconds": c.TimeoutMinutes * 60})
}
