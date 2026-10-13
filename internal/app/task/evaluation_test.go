package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

func TestEvaluationLifecycleNeverPublishes(t *testing.T) {
	f := setupTask(t)
	var ready atomic.Bool
	checks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/sandbox" {
			if !ready.Load() {
				w.WriteHeader(503)
			} else {
				JSON(w, 200, Object{"ready": true})
			}
			return
		}
		JSON(
			w,
			200,
			Object{
				"success": true,
				"data": Object{
					"session_id": "checks",
					"command_id": "1",
					"status":     "completed",
					"exit_code":  1,
					"stderr":     "assertion failed",
				},
			},
		)
	}))
	defer checks.Close()
	ws := &testWorkspace{
		endpoint: workspace.Endpoint{URL: checks.URL},
		jobResults: map[string][]byte{
			"snapshot": mustJSON(
				workspace.Artifact{Patch: "candidate patch", Changed: []string{"calc.py"}},
			),
		},
	}
	f.a.Workspaces = ws
	id := strings.Repeat("a", 32)
	req := evaluationRequest{
		Scope: evaluationScope{TenantID: "t", ProjectID: "p", ActorID: "member"},
		Scenario: evaluationScenario{
			BaseHead:     strings.Repeat("a", 40),
			AllowedPaths: []string{"calc.py"},
			Checks:       []string{"python3 -m unittest"},
		},
	}
	call := func(phase string) evaluationEvidence {
		t.Helper()
		req.Phase = phase
		r := httptest.NewRequest(
			"POST",
			"/internal/v1/evaluation-environments/"+id,
			bytes.NewReader(mustJSON(req)),
		)
		r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
		w := httptest.NewRecorder()
		f.a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out evaluationEvidence
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := call("prepare"); got.Status != "Preparing" {
		t.Fatal("started before workspace service ready", got)
	}
	ready.Store(true)
	if got := call("prepare"); got.Status != "Ready" {
		t.Fatal(got)
	}
	if got := call("prepare"); got.Status != "Ready" {
		t.Fatal(got)
	}
	if err := table.Migrate(f.ctx, f.a.DB); err != nil {
		t.Fatal(err)
	}
	ready.Store(false)
	if got := call("verify"); got.Status != "Verifying" || len(got.Checks) != 0 {
		t.Fatal("verification started before service ready", got)
	}
	ready.Store(true)
	got := call("verify")
	if got.Status != "Completed" || !got.Stopped || got.Patch != "candidate patch" || len(got.Checks) != 1 ||
		got.Checks[0]["exitCode"] != float64(1) {
		t.Fatal(got)
	}
	if ws.published != 0 || f.mrWrites.Load() != 0 {
		t.Fatal("evaluation published business result")
	}
	stops := ws.stops
	call("verify")
	if ws.stops != stops {
		t.Fatal("terminal request repeated lifecycle")
	}
	req.Scenario.BaseHead = strings.Repeat("b", 40)
	req.Phase = "prepare"
	r := httptest.NewRequest("POST", "/internal/v1/evaluation-environments/"+id, bytes.NewReader(mustJSON(req)))
	r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("immutable environment accepted changed input", w.Code)
	}
}

func TestEvaluationToolScopeAndStop(t *testing.T) {
	f := setupTask(t)
	ws := &testWorkspace{}
	f.a.Workspaces = ws
	id := strings.Repeat("b", 32)
	req := evaluationRequest{
		Scope: evaluationScope{TenantID: "t", ProjectID: "p", ActorID: "member"},
		Scenario: evaluationScenario{
			BaseHead:     strings.Repeat("a", 40),
			AllowedPaths: []string{"calc.py"},
			Checks:       []string{"python3 -m unittest"},
		},
		Phase: "stop",
	}
	r := httptest.NewRequest("POST", "/internal/v1/evaluation-environments/"+id, bytes.NewReader(mustJSON(req)))
	r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, scope := range []sandboxScope{{TenantID: "other", ProjectID: "p", ActorID: "member", TaskID: "eval:" + id, RoundID: "1"}, {TenantID: "t", ProjectID: "p", ActorID: "member", TaskID: "eval:" + id, RoundID: "1"}} {
		r = httptest.NewRequest(
			"POST",
			"/internal/v1/evaluation-tools/finish",
			strings.NewReader(`{"summary":"passed"}`),
		)
		r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
		r.Header.Set("X-Agentic-Scope", string(mustJSON(scope)))
		r.Header.Set("X-Agentic-Run-ID", "run")
		r.Header.Set("X-Agentic-Epoch", "1")
		w = httptest.NewRecorder()
		f.a.Handler().ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatal("stopped or foreign tool accepted", w.Code)
		}
	}
}

func TestEvaluationEvidenceSurvivesCleanupFailure(t *testing.T) {
	f := setupTask(t)
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			JSON(w, 200, Object{})
			return
		}
		checks.Add(1)
		JSON(
			w,
			200,
			Object{
				"success": true,
				"data": Object{
					"session_id": "check",
					"command_id": "1",
					"status":     "completed",
					"exit_code":  0,
				},
			},
		)
	}))
	defer server.Close()
	ws := &testWorkspace{
		endpoint:     workspace.Endpoint{URL: server.URL},
		jobResults:   map[string][]byte{"snapshot": mustJSON(workspace.Artifact{Patch: "persisted patch"})},
		cleanupError: errors.New("cleanup unavailable"),
	}
	f.a.Workspaces = ws
	id := strings.Repeat("c", 32)
	req := evaluationRequest{
		Scope: evaluationScope{TenantID: "t", ProjectID: "p", ActorID: "member"},
		Scenario: evaluationScenario{
			BaseHead:     strings.Repeat("a", 40),
			AllowedPaths: []string{"calc.py"},
			Checks:       []string{"test"},
		},
	}
	call := func(phase string) *httptest.ResponseRecorder {
		req.Phase = phase
		r := httptest.NewRequest(
			"POST",
			"/internal/v1/evaluation-environments/"+id,
			bytes.NewReader(mustJSON(req)),
		)
		r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
		w := httptest.NewRecorder()
		f.a.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("prepare"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("verify"); w.Code != 500 {
		t.Fatal("cleanup failure was hidden", w.Code)
	}
	var raw []byte
	if err := f.a.DB.QueryRow(f.ctx, `SELECT evidence FROM coding_evaluation_environments WHERE id=$1`, id).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved evaluationEvidence
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "Completed" || saved.Stopped || saved.Patch != "persisted patch" || len(saved.Checks) != 1 {
		t.Fatal("evidence lost before cleanup", saved)
	}
	ws.cleanupError = nil
	if w := call("verify"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if checks.Load() != 1 {
		t.Fatal("cleanup retry reran verification")
	}
}

func TestEvaluationRecoveryReturnsPreparingToWorker(t *testing.T) {
	f := setupTask(t)
	lease := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, Object{"allowed": true}) },
		),
	)
	defer lease.Close()
	f.a.Config.RuntimeURL = lease.URL
	id := strings.Repeat("d", 32)
	req := evaluationRequest{
		Scope: evaluationScope{TenantID: "t", ProjectID: "p", ActorID: "member"},
		Scenario: evaluationScenario{
			BaseHead:     strings.Repeat("a", 40),
			AllowedPaths: []string{"calc.py"},
			Checks:       []string{"test"},
		},
	}
	if _, err := f.a.DB.Exec(
		f.ctx,
		`INSERT INTO coding_evaluation_environments(id,request,evidence) VALUES($1,$2,$3)`,
		id,
		mustJSON(req),
		mustJSON(evaluationEvidence{Status: "Ready", BaseHead: req.Scenario.BaseHead}),
	); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepare", "fence"} {
		t.Run(phase, func(t *testing.T) {
			ws := &testWorkspace{}
			if phase == "prepare" {
				ws.ensureError = workspace.ErrPreparing
			} else {
				ws.fenceError = workspace.ErrPreparing
			}
			f.a.Workspaces = ws
			r := httptest.NewRequest(
				"GET",
				"/internal/v1/evaluation-environments/"+id+"/sandbox/v1/sandbox",
				nil,
			)
			r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
			r.Header.Set(
				"X-Agentic-Scope",
				string(
					mustJSON(
						sandboxScope{
							TenantID:  "t",
							ProjectID: "p",
							ActorID:   "member",
							TaskID:    "eval:" + id,
							RoundID:   "1",
						},
					),
				),
			)
			r.Header.Set("X-Agentic-Run-ID", "runtime")
			r.Header.Set("X-Agentic-Epoch", "2")
			w := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("worker cannot await environment recovery: %d", w.Code)
			}
		})
	}
}
