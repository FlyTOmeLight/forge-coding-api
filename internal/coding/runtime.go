package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	rows, e := a.DB.Query(ctx, `SELECT id FROM coding_runs WHERE status IN ('Queued','Running','Stopping') AND (status='Queued' OR runtime_id IS NOT NULL OR execution_snapshot IS NOT NULL) ORDER BY created_at LIMIT 100`)
	if e != nil {
		return e
	}
	ids, e := pgx.CollectRows(rows, pgx.RowTo[string])
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = a.reconcileRound(ctx, id); e != nil {
			slog.Error("round reconciliation failed", "round", id, "error", e)
		}
	}
	return nil
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
	var status, requirements, claimed string
	var raw, execution json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT runtime_id,status,requirements,config_snapshot,execution_snapshot,claimed_by FROM coding_runs WHERE id=$1 FOR UPDATE`, id).Scan(&runtimeID, &status, &requirements, &raw, &execution, &claimed); e != nil {
		return e
	}
	if status != "Queued" && status != "Running" && status != "Stopping" {
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
	if runtimeID == nil {
		if status == "Stopping" || t.Control != "Agent" {
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Stopped',finished_at=now() WHERE id=$1`, id); e != nil {
				return e
			}
			t.ActiveRun = nil
			t.StopConfirmed = true
			t.Control = "Human"
			if t.Status == "Canceling" {
				t.Status = "Canceled"
			} else {
				t.Status = "Review"
			}
			if e = writeTask(ctx, tx, t); e != nil {
				return e
			}
			return tx.Commit(ctx)
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
		if _, e = tx.Exec(ctx, `UPDATE coding_runs SET runtime_id=$2,status='Running' WHERE id=$1`, id, run.ID); e != nil {
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
	// A canceled model-only run has no external process to stop. A started tool
	// must acknowledge its stopped process before a new round can begin.
	if run.Status == "Canceled" && claimed == "" {
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
	}
	if run.Status == "Completed" || run.Status == "Failed" { // Delivery has its own verified completion endpoint.
		if claimed == "" {
			t.ActiveRun = nil
			t.Control = "Human"
			t.StopConfirmed = true
			t.Status = "Failed"
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Failed',output=$2,finished_at=now() WHERE id=$1`, id, mustJSON(Object{"runtimeStatus": run.Status, "error": run.Error, "summary": run.Output})); e != nil {
				return e
			}
			if e = writeTask(ctx, tx, t); e != nil {
				return e
			}
		} else if status != "Stopping" {
			t.Control = "Stopping"
			t.Epoch++
			t.StopConfirmed = false
			now := time.Now().UTC()
			t.StopRequestedAt = &now
			if _, e = tx.Exec(ctx, `UPDATE coding_runs SET status='Stopping' WHERE id=$1`, id); e != nil {
				return e
			}
			if e = writeTask(ctx, tx, t); e != nil {
				return e
			}
		}
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
	endpoint, e := a.toolEndpoint(ctx, t.TenantID, t.ProjectID)
	if e != nil {
		return nil, e
	}
	tool := func(name, description string, params Object) Object {
		return Object{"name": name, "description": description, "kind": "HTTP", "url": strings.TrimRight(endpoint, "/") + "/rounds/" + round + "/" + name, "tokenEnv": "CODING_TOOLS_TOKEN", "policy": "Unsafe", "parameters": params}
	}
	empty := Object{"type": "object", "properties": Object{}, "additionalProperties": false}
	tools := []Object{tool("prepare", "Read repository rules, scoped source files and baseline checks before editing.", empty), tool("edit_and_check", "Apply exact localized edits and run required checks. Preserve existing tests. Inspect failed checks before another attempt.", Object{"type": "object", "properties": Object{"edits": Object{"type": "array", "minItems": 1, "maxItems": 12, "items": Object{"type": "object", "properties": Object{"path": Object{"type": "string"}, "before": Object{"type": "string"}, "after": Object{"type": "string"}}, "required": []string{"path", "before", "after"}, "additionalProperties": false}}}, "required": []string{"edits"}, "additionalProperties": false}), tool("deliver", "Commit checked changes, push the task branch and open a Draft MR. Call after checks to hand over existing changes. Assess acceptance criteria separately from test results in judgment; use NeedsHuman when evidence is missing. Never merge.", Object{"type": "object", "properties": Object{"summary": Object{"type": "string"}, "judgment": Object{"type": "object", "properties": Object{"status": Object{"type": "string", "enum": []string{"Met", "Unmet", "NeedsHuman"}}, "summary": Object{"type": "string"}}, "required": []string{"status", "summary"}, "additionalProperties": false}}, "required": []string{"summary", "judgment"}, "additionalProperties": false})}
	spec := Object{"scope": Object{"tenantID": t.TenantID, "projectID": t.ProjectID, "actorID": t.OwnerID, "taskID": t.ID, "roundID": round}, "query": requirements, "systemPrompt": "You are a coding agent. First call prepare and obey repository AGENTS rules. Diagnose the requested bug. Use edit_and_check for small scoped changes, preserving existing tests. Call deliver once to hand over the checked revision and Draft MR. Never invent tool results or claim the task is merged. Do not exceed the configured repair limit.", "model": Object{"name": c.Model, "baseURL": strings.TrimRight(a.Config.ModelURL, "/") + "/v1"}, "tools": tools, "maxTurns": c.RepairLimit + 4}
	return json.Marshal(Object{"spec": spec, "timeoutSeconds": c.TimeoutMinutes * 60})
}
