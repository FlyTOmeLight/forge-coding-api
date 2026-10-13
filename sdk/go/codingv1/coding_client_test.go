package codingv1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

func TestSDKRoutesAndAuthentication(t *testing.T) {
	scope := ProjectScope{TenantID: "t", ProjectID: "p", Session: "session", CSRFToken: "csrf"}
	identity := ExecutionIdentity{
		Scope: ToolScope{TenantID: "t", ProjectID: "p", ActorID: "actor", TaskID: "task", RoundID: "run"},
		RunID: "runtime",
		Epoch: 2,
	}
	base := "/api/v1/tenants/t/projects/p"
	cases := []struct {
		name, method, path, body, key string
		internal                      bool
		call                          func(*Client) error
	}{
		{
			name:   "list",
			method: "GET",
			path:   base + "/tasks",
			body:   `{"items":[]}`,
			call:   func(c *Client) error { _, e := c.ListTasks(context.Background(), scope); return e },
		},
		{
			name:   "detail",
			method: "GET",
			path:   base + "/tasks/task",
			body:   `{"task":{"id":"task"}}`,
			call:   func(c *Client) error { _, e := c.GetTask(context.Background(), scope, "task"); return e },
		},
		{
			name:   "associate",
			method: "POST",
			path:   base + "/tasks",
			body:   `{"id":"task"}`,
			call: func(c *Client) error {
				_, e := c.AssociateTask(context.Background(), scope, &AssociateTaskRequest{MRIID: 1})
				return e
			},
		},
		{
			name:   "create",
			method: "POST",
			path:   base + "/tasks/new",
			body:   `{"id":"task"}`,
			key:    "create-key",
			call: func(c *Client) error {
				_, e := c.CreateTask(
					context.Background(),
					scope,
					"create-key",
					&CreateTaskRequest{Title: "fix"},
				)
				return e
			},
		},
		{
			name:   "action",
			method: "POST",
			path:   base + "/tasks/task/actions",
			body:   `{"id":"task"}`,
			call: func(c *Client) error {
				_, e := c.ActOnTask(
					context.Background(),
					scope,
					"task",
					&TaskActionRequest{Action: "Takeover", Version: 1},
				)
				return e
			},
		},
		{
			name:   "sync",
			method: "POST",
			path:   base + "/tasks/task/sync",
			body:   `{"id":"task"}`,
			call:   func(c *Client) error { _, e := c.SyncTask(context.Background(), scope, "task"); return e },
		},
		{
			name:   "diff",
			method: "GET",
			path:   base + "/tasks/task/diff",
			body:   `{"patch":"diff"}`,
			call:   func(c *Client) error { _, e := c.GetTaskDiff(context.Background(), scope, "task"); return e },
		},
		{
			name:   "trace",
			method: "GET",
			path:   base + "/tasks/task/runs/run/trace?after=8",
			body:   `{"nextSeq":9,"events":[]}`,
			call:   func(c *Client) error { _, e := c.GetRunTrace(context.Background(), scope, "task", "run", 8); return e },
		},
		{
			name:   "settings",
			method: "GET",
			path:   base + "/coding-settings",
			body:   `{"settings":{"version":1}}`,
			call:   func(c *Client) error { _, e := c.GetSettings(context.Background(), scope); return e },
		},
		{
			name:   "save",
			method: "PUT",
			path:   base + "/coding-settings",
			body:   `{"version":2}`,
			call: func(c *Client) error {
				_, e := c.SaveSettings(context.Background(), scope, &SaveSettingsRequest{Version: 1})
				return e
			},
		},
		{
			name:   "check",
			method: "POST",
			path:   base + "/coding-settings/check",
			body:   `{"baseHead":"head"}`,
			call:   func(c *Client) error { _, e := c.CheckSettings(context.Background(), scope); return e },
		},
		{
			name:   "connections",
			method: "GET",
			path:   "/api/v1/platform/connections",
			body:   `{}`,
			call:   func(c *Client) error { _, e := c.Connections(context.Background(), scope); return e },
		},
		{
			name:   "releases",
			method: "GET",
			path:   base + "/coding-releases",
			body:   `{"items":[]}`,
			call:   func(c *Client) error { _, e := c.ListReleases(context.Background(), scope); return e },
		},
		{
			name:   "enable",
			method: "POST",
			path:   base + "/coding-releases",
			body:   `{"id":"release"}`,
			key:    "enable-key",
			call: func(c *Client) error {
				_, e := c.EnableRelease(
					context.Background(),
					scope,
					"enable-key",
					&EnableReleaseRequest{AgentID: "candidate", AgentVersion: 2},
				)
				return e
			},
		},
		{
			name:   "restore",
			method: "POST",
			path:   base + "/coding-releases/release/restore",
			body:   `{"id":"release"}`,
			key:    "restore-key",
			call: func(c *Client) error {
				_, e := c.RestoreRelease(
					context.Background(),
					scope,
					"release",
					"restore-key",
					&RestoreReleaseRequest{SettingsVersion: 2},
				)
				return e
			},
		},
		{
			name:     "evaluation",
			method:   "POST",
			path:     "/internal/v1/evaluation-environments/trial",
			body:     `{"status":"Ready","stopped":false}`,
			internal: true,
			call: func(c *Client) error {
				_, e := c.AdvanceEvaluation(
					context.Background(),
					"trial",
					&EvaluationEnvironmentRequest{Phase: "prepare"},
				)
				return e
			},
		},
		{
			name:     "finish",
			method:   "POST",
			path:     "/internal/v1/tools/finish",
			body:     `{"accepted":true}`,
			key:      "runtime:finish",
			internal: true,
			call: func(c *Client) error {
				_, e := c.SubmitFinish(
					context.Background(),
					identity,
					"runtime:finish",
					&FinishRequest{Summary: "done"},
				)
				return e
			},
		},
		{
			name:     "checks",
			method:   "POST",
			path:     "/internal/v1/tools/run_checks",
			body:     `{"checks":[]}`,
			internal: true,
			call:     func(c *Client) error { _, e := c.RunChecks(context.Background(), identity); return e },
		},
		{
			name:     "evaluation-finish",
			method:   "POST",
			path:     "/internal/v1/evaluation-tools/finish",
			body:     `{"accepted":true}`,
			internal: true,
			call: func(c *Client) error {
				_, e := c.EvaluationFinish(
					context.Background(),
					identity,
					&EvaluationFinishRequest{Summary: "done"},
				)
				return e
			},
		},
		{
			name:     "evaluation-checks",
			method:   "POST",
			path:     "/internal/v1/evaluation-tools/run_checks",
			body:     `{"checks":[]}`,
			internal: true,
			call:     func(c *Client) error { _, e := c.EvaluationChecks(context.Background(), identity); return e },
		},
		{
			name:     "permit",
			method:   "POST",
			path:     "/internal/v1/rounds/run/publication-permit",
			body:     `{"allowed":true}`,
			internal: true,
			call:     func(c *Client) error { _, e := c.PublicationPermit(context.Background(), "run"); return e },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.RequestURI() != tc.path ||
					r.Header.Get("Idempotency-Key") != tc.key {
					t.Error("wrong request", r.Method, r.URL.RequestURI())
				}
				if tc.internal {
					if r.Header.Get("Authorization") != "Bearer service-token" {
						t.Error("missing service identity")
					}
				} else {
					cookie, e := r.Cookie("forge_session")
					if e != nil || cookie.Value != "session" ||
						r.Header.Get("X-CSRF-Token") != "csrf" {
						t.Error("missing session identity")
					}
				}
				if r.ContentLength > 0 {
					var body map[string]any
					if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
						t.Error(e)
					}
					if _, ok := body["Session"]; ok {
						t.Error("identity included in body")
					}
					if tc.name == "create" && body["title"] != "fix" {
						t.Error("request body lost")
					}
					if tc.name == "action" &&
						(body["version"] != float64(1) || body["action"] != "Takeover") {
						t.Error("action contract changed")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, e := NewClient(server.URL, "service-token", nil)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			if e := tc.call(c); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestSDKFailuresCancellationAndSessionIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{name: "business", body: `{"error":{"code":"STALE_CONFIG","message":"refresh"}}`, code: "STALE_CONFIG", status: 412},
		{name: "framework", body: `{"code":"Unauthorized","requestId":"request","statusCode":103401}`, code: "Unauthorized", status: 401},
		{name: "non-json", body: "upstream failed", code: "HTTPError", status: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, e := NewClient(server.URL, "", nil)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			_, e = c.ListTasks(context.Background(), ProjectScope{TenantID: "t", ProjectID: "p"})
			var detail *serrors.Error
			if !errors.As(e, &detail) || detail.Code != tc.code || detail.Status != tc.status {
				t.Fatal(e)
			}
			if tc.name == "framework" && (detail.RequestID != "request" || detail.StatusCode != 103401) {
				t.Fatal("metadata lost")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("forge_session")
		if e != nil || cookie.Value != r.Header.Get("X-CSRF-Token") {
			t.Error("shared session state")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	c, e := NewClient(server.URL, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.ListTasks(ctx, ProjectScope{TenantID: "t", ProjectID: "p"}); !errors.Is(e, context.Canceled) {
		t.Fatal("cancellation lost", e)
	}
	var workers sync.WaitGroup
	for _, user := range []string{"alice", "bob"} {
		workers.Add(1)
		go func(user string) {
			defer workers.Done()
			for range 5 {
				_, e := c.ListTasks(
					context.Background(),
					ProjectScope{TenantID: "t", ProjectID: "p", Session: user, CSRFToken: user},
				)
				if e != nil {
					t.Error(e)
				}
			}
		}(user)
	}
	workers.Wait()
}

func TestSDKSandboxStreamsAndHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	sent := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/evaluation-environments/trial/sandbox/v1/file/read" ||
			r.Header.Get("X-Agentic-Run-ID") != "runtime" ||
			r.Header.Get("X-Agentic-Epoch") != "2" {
			t.Error("sandbox identity/path lost")
		}
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		close(sent)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	c, e := NewClient(server.URL, "service-token", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, e := c.Sandbox(
		ctx,
		&SandboxRequest{
			TrialID:  "trial",
			Method:   "GET",
			Path:     "/v1/file/read",
			Identity: ExecutionIdentity{RunID: "runtime", Epoch: 2},
		},
	)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	<-sent
	raw := make([]byte, 5)
	if _, e := io.ReadFull(response.Body, raw); e != nil || string(raw) != "first" {
		t.Fatal(e)
	}
	cancel()
	if _, e := response.Body.Read(make([]byte, 1)); e == nil {
		t.Fatal("stream ignores cancellation")
	}
	for _, endpoint := range []string{"relative", "http://user:password@localhost", "http://localhost?query=1", "http://localhost#fragment"} {
		if _, e := NewClient(endpoint, "", nil); e == nil {
			t.Fatal("invalid endpoint accepted", endpoint)
		}
	}
	if _, e := c.Sandbox(context.Background(), &SandboxRequest{Method: "GET", Path: "/admin"}); e == nil {
		t.Fatal("unsupported sandbox API accepted")
	}
}
