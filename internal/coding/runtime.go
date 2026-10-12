package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RuntimeRun struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func (a *App) runtimeReady(ctx context.Context) bool {
	return a.request(ctx, "GET", a.Config.RuntimeURL+"/internal/v1/healthz", a.Config.RuntimeToken, nil, nil) == nil
}
func (a *App) runtimeLease(ctx context.Context, id string, epoch int64) error {
	if id == "" || epoch < 1 {
		return problem(409, "STALE_EXECUTION", "执行身份无效")
	}
	var result struct {
		Allowed bool `json:"allowed"`
	}
	e := a.request(ctx, "GET", a.Config.RuntimeURL+"/internal/v1/executions/"+id+"/lease?epoch="+strconv.FormatInt(epoch, 10), a.Config.RuntimeToken, nil, &result)
	if e != nil {
		return e
	}
	if !result.Allowed {
		return problem(409, "RUNTIME_LEASE_LOST", "当前执行者的执行权已失效")
	}
	return nil
}
func (a *App) RunDispatcher(ctx context.Context) error {
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
				slog.Error("execution reconciliation failed", "error", e)
			}
		}
	}
}
func (a *App) reconcileRuns(ctx context.Context) error {
	rows, e := a.DB.Query(ctx, `SELECT id FROM coding_runs WHERE status IN ('Queued','Running','Stopping','Delivering') AND (status='Queued' OR runtime_id IS NOT NULL OR execution_snapshot IS NOT NULL OR (status='Stopping' AND claimed_by='')) ORDER BY created_at LIMIT 100`)
	if e != nil {
		return e
	}
	ids, e := pgx.CollectRows(rows, pgx.RowTo[string])
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = a.reconcileRound(ctx, id); e != nil && !errors.Is(e, workspace.ErrPreparing) {
			slog.Error("round reconciliation failed", "round", id, "error", e)
		}
	}
	return a.reapSandboxes(ctx)
}
func (a *App) reconcileRound(ctx context.Context, id string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var taskID string
	if e = tx.QueryRow(ctx, `SELECT task_id FROM coding_runs WHERE id=$1`, id).Scan(&taskID); e != nil {
		return e
	}
	t, e := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 FOR UPDATE SKIP LOCKED`, taskID))
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	var runtimeID *string
	var status, requirements string
	var raw, execution json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT runtime_id,status,requirements,config_snapshot,execution_snapshot FROM coding_runs WHERE id=$1 FOR UPDATE`, id).Scan(&runtimeID, &status, &requirements, &raw, &execution); e != nil {
		return e
	}
	if status != "Queued" && status != "Running" && status != "Stopping" && status != "Delivering" {
		return nil
	}
	level, e := a.taskLevel(ctx, t.OwnerID, t.TenantID, t.ProjectID)
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
		if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, id); e != nil {
			return e
		}
		if e = writeTask(ctx, tx, t); e != nil {
			return e
		}
		if e = taskEvent(ctx, tx, t.ID, "authorization", "ExecutionRevoked", Object{}); e != nil {
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
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Stopped',finished_at=now() WHERE id=$1`, id); e != nil {
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
			if e = writeTask(ctx, tx, t); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		{
			endpoint, prepareErr := a.prepareSandbox(ctx, t, id)
			if prepareErr != nil {
				return prepareErr
			}
			if _, e = tx.Exec(ctx, `UPDATE coding_tasks SET sandbox_active=true WHERE id=$1`, t.ID); e != nil {
				return e
			}
			var output map[string]json.RawMessage
			var stored []byte
			if e = tx.QueryRow(ctx, `SELECT output FROM coding_runs WHERE id=$1`, id).Scan(&stored); e != nil {
				return e
			}
			if e = json.Unmarshal(stored, &output); e != nil {
				return e
			}
			if len(output["baseline"]) == 0 {
				var config CodingSettings
				if e = json.Unmarshal(raw, &config); e != nil {
					return e
				}
				baseline, err := runAIOChecks(ctx, endpoint, config.Checks)
				if err != nil {
					return err
				}
				if _, e = tx.Exec(ctx, `UPDATE coding_runs SET output=output || jsonb_build_object('baseline',$2::jsonb) WHERE id=$1`, id, mustJSON(baseline)); e != nil {
					return e
				}
			}
		}
		if len(execution) == 0 {
			var config CodingSettings
			if e = json.Unmarshal(raw, &config); e != nil {
				return e
			}
			execution, e = a.executionSnapshot(ctx, t, id, requirements, config)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET execution_snapshot=$2 WHERE id=$1`, id, execution); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		req, e := http.NewRequestWithContext(ctx, "POST", a.Config.RuntimeURL+"/internal/v1/executions", bytes.NewReader(execution))
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+a.Config.RuntimeToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", id)
		response, e := a.HTTP.Do(req)
		if e != nil {
			return e
		}
		defer response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 202 {
			return fmt.Errorf("runtime submit HTTP %d", response.StatusCode)
		}
		var run RuntimeRun
		if e = json.NewDecoder(response.Body).Decode(&run); e != nil {
			return e
		}
		if run.ID == "" {
			return fmt.Errorf("runtime returned empty execution ID")
		}
		query := `UPDATE coding_runs SET runtime_id=$2,claimed_by=$2,status='Running' WHERE id=$1`
		if _, e = tx.Exec(ctx, query, id, run.ID); e != nil {
			return e
		}
		t.Status = "Running"
		if e = writeTask(ctx, tx, t); e != nil {
			return e
		}
		if e = taskEvent(ctx, tx, t.ID, "runtime", "ExecutionSubmitted", Object{"roundId": id, "runtimeId": run.ID}); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if status == "Stopping" || t.Control != "Agent" {
		if e = a.request(ctx, "POST", a.Config.RuntimeURL+"/v1/runs/"+*runtimeID+"/cancel", a.Config.RuntimeToken, Object{}, nil); e != nil {
			return e
		}
	}
	var run RuntimeRun
	if e = a.request(ctx, "GET", a.Config.RuntimeURL+"/v1/runs/"+*runtimeID, a.Config.RuntimeToken, nil, &run); e != nil {
		return e
	}
	if run.Status == "Canceled" || run.Status == "Failed" || run.Status == "NeedsReview" || run.Status == "Completed" && (status == "Stopping" || t.Control != "Agent") {
		if err := a.Workspaces.StopAll(ctx, t.ID, id); err != nil {
			return err
		}
		var roundEpoch int64
		if e = tx.QueryRow(ctx, `SELECT epoch FROM coding_runs WHERE id=$1`, id).Scan(&roundEpoch); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		_, e = a.recordDelivery(ctx, t.ID, taskRunRequest{RunID: id, Worker: *runtimeID, Epoch: roundEpoch, Status: "Stopped", Output: mustJSON(Object{"runtimeStatus": run.Status, "error": run.Error, "summary": run.Output})})
		return e
	}
	if run.Status == "Completed" {
		var hasFinish bool
		if e = tx.QueryRow(ctx, `SELECT output ? 'finishRequest' FROM coding_runs WHERE id=$1`, id).Scan(&hasFinish); e != nil {
			return e
		}
		if hasFinish {
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Delivering' WHERE id=$1`, id); e != nil {
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
		if e = tx.QueryRow(ctx, `SELECT epoch FROM coding_runs WHERE id=$1`, id).Scan(&roundEpoch); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		_, e = a.recordDelivery(ctx, t.ID, taskRunRequest{RunID: id, Worker: *runtimeID, Epoch: roundEpoch, Status: "Stopped", Output: mustJSON(Object{"runtimeStatus": "Failed", "error": "Agent ended without submitting finish", "summary": run.Output})})
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
func (a *App) executionSnapshot(ctx context.Context, t CodingTask, round, requirements string, c CodingSettings) ([]byte, error) {
	e := a.requireProjectExecutor(ctx, t.TenantID, t.ProjectID)
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
	comments := []Object{}
	for _, discussion := range discussions {
		for _, note := range discussion.Notes {
			if note.System || note.Body == "" {
				continue
			}
			comments = append(comments, Object{"discussionID": discussion.ID, "noteID": note.ID, "author": note.Author.Username, "body": note.Body, "position": note.Position, "resolved": discussion.Resolved})
		}
	}
	query, e := json.Marshal(Object{"requirements": requirements, "task": Object{"title": t.Title, "description": t.Description, "criteria": t.Criteria, "reproduce": t.Reproduce, "constraints": t.Constraints, "reference": t.Reference, "head": t.Head}, "comments": comments, "workspace": Object{"path": "/workspace/repo", "allowedPaths": c.AllowedPaths, "checks": c.Checks, "repairLimit": c.RepairLimit}})
	if e != nil {
		return nil, e
	}
	spec := Object{"scope": Object{"tenantID": t.TenantID, "projectID": t.ProjectID, "actorID": t.OwnerID, "taskID": t.ID, "roundID": round}, "agentID": c.AgentID, "agentVersion": c.AgentVersion, "query": string(query)}
	{
		spec["filesystem"] = Object{"provider": "AIO", "url": strings.TrimRight(a.Config.SandboxGatewayURL, "/") + "/internal/v1/rounds/" + round + "/sandbox", "tokenEnv": "CODING_TOOLS_TOKEN", "workspace": "/workspace/repo"}
	}
	return json.Marshal(Object{"spec": spec, "timeoutSeconds": c.TimeoutMinutes * 60})
}
