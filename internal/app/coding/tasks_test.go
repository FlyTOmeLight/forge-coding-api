package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

type fixture struct {
	a                       *App
	ctx                     context.Context
	operator, member, other string
}

type taskFixture struct {
	gitlabProjectID int64
	*fixture
	task            table.Task
	head, mrState   string
	mrTitle         string
	mrDraft         bool
	mrUpdateStatus  int
	mrUpdateIgnored bool
	mrUpdateHead    string
	mrHeadOverride  string
	runtimeStatus   string
	missingBranch   bool
	mrWrites        atomic.Int32
	level           atomic.Int32
	lease           atomic.Bool
}

func setupTask(t *testing.T) *taskFixture {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "test_" + Token()[:16]
	if _, e = conn.Exec(ctx, `CREATE SCHEMA `+name); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	db, e := table.Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	h := client.NewHTTP()
	a := New(
		config.Config{
			DatabaseURL:       u.String(),
			ServiceToken:      strings.Repeat("a", 32),
			RuntimeToken:      strings.Repeat("b", 32),
			TenantToken:       strings.Repeat("c", 32),
			ModelURL:          "http://model",
			SandboxGatewayURL: "http://coding",
			EnabledProjects:   map[string]bool{"77": true},
		},
		db,
		h,
		nil,
		nil,
	)
	t.Cleanup(func() {
		h.CloseIdleConnections()
		db.Close()
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA `+name+` CASCADE`)
		_ = conn.Close(context.Background())
	})
	if e = table.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}

	f := &taskFixture{
		fixture:         &fixture{a: a, ctx: ctx, operator: "operator", member: "member", other: "other"},
		head:            strings.Repeat("a", 40),
		mrState:         "opened",
		gitlabProjectID: 77,
		mrTitle:         "Draft: Fix traversal",
		mrDraft:         true,
	}
	f.level.Store(40)
	f.lease.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bash/exec":
			JSON(
				w,
				200,
				Object{
					"success": true,
					"data": Object{
						"session_id": "test",
						"command_id": "check",
						"status":     "completed",
						"exit_code":  0,
					},
				},
			)
			return
		case "/api/v4/projects/77/repository/branches":
			JSON(w, 200, []Object{{"name": "master", "commit": Object{"id": f.head}}})
			return
		case "/internal/v1/session/check":
			var b struct{ Session string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			JSON(w, 200, User{ID: b.Session, Operator: b.Session == "operator"})
			return
		case "/internal/v1/access/check":
			var b struct {
				UserID    string `json:"user_id"`
				TenantID  string `json:"tenant_id"`
				ProjectID string `json:"project_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			n := int(f.level.Load())
			if b.UserID == "member" {
				n = 30
			}
			if b.UserID == "other" || b.TenantID != "t" || b.ProjectID != "p" {
				n = 0
			}
			JSON(w, 200, Object{"level": n})
			return
		case "/internal/v1/projects/resolve":
			JSON(
				w,
				200,
				Project{ID: "p", TenantID: "t", GitLabID: f.gitlabProjectID, Path: "task-test/repo"},
			)
			return
		case "/v1/agents/coding-agent":
			JSON(w, 200, AgentChoice{ID: "coding-agent", Name: "Coding", PublishedVersion: 1})
			return
		case "/v1/agents/coding-agent/versions/1":
			JSON(
				w,
				200,
				PublishedAgent{
					AgentID: "coding-agent",
					Number:  1,
					Config: PublishedAgentConfig{Model: struct {
						Name string `json:"name"`
					}{Name: "local-model"}, Toolsets: []string{"filesystem"}, Tools: []AgentTool{{Name: "run_checks", URL: "http://coding/internal/v1/tools/run_checks"}, {Name: "finish", EndRun: true, URL: "http://coding/internal/v1/tools/finish"}}},
				},
			)
			return
		case "/internal/v1/executions":
			JSON(w, 202, RuntimeRun{ID: "executor", Status: "Queued"})
			return
		case "/v1/runs/executor":
			state := f.runtimeStatus
			if state == "" {
				state = "Running"
			}
			JSON(w, 200, RuntimeRun{ID: "executor", Status: state, Error: "tool outcome needs review"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/lease") {
			JSON(w, 200, Object{"allowed": f.lease.Load()})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/branches/") {
			if f.missingBranch {
				JSON(w, 404, Object{"message": "not found"})
				return
			}
			JSON(w, 200, Object{"name": "master", "commit": Object{"id": f.head}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/discussions") {
			JSON(w, 200, []any{})
			return
		}
		if r.Method == "PUT" {
			f.mrWrites.Add(1)
			if f.mrUpdateStatus != 0 {
				JSON(w, f.mrUpdateStatus, Object{"message": "update failed"})
				return
			}
			var b struct {
				Title string `json:"title"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			if !f.mrUpdateIgnored {
				f.mrTitle = b.Title
				f.mrDraft = strings.HasPrefix(b.Title, "Draft:")
			}
			if f.mrUpdateHead != "" {
				f.head = f.mrUpdateHead
			}
		}
		mrHead := f.head
		if f.mrHeadOverride != "" {
			mrHead = f.mrHeadOverride
		}
		JSON(
			w,
			200,
			Object{
				"iid":           1,
				"title":         f.mrTitle,
				"draft":         f.mrDraft,
				"state":         f.mrState,
				"sha":           mrHead,
				"source_branch": "fix",
				"target_branch": "master",
			},
		)
	}))
	t.Cleanup(remote.Close)
	a.Config.TenantURL = remote.URL
	a.Config.RuntimeURL = remote.URL
	setGitLab(t, a, remote.URL, "test-gitlab-token")
	a.Workspaces = &testWorkspace{endpoint: workspace.Endpoint{URL: remote.URL, Token: "test"}}
	raw, _ := json.Marshal(
		CodingSettings{
			Version:        1,
			BaseBranch:     "master",
			AgentID:        "coding-agent",
			AgentVersion:   1,
			Model:          "local-model",
			AllowedPaths:   []string{"src/"},
			Checks:         []string{"go test ./..."},
			RepairLimit:    3,
			TimeoutMinutes: 30,
		},
	)
	if _, e = a.DB.Exec(
		ctx,
		`INSERT INTO coding_project_settings(tenant_id,project_id,version,settings,checked_at) VALUES('t','p',1,$1,now())`,
		raw,
	); e != nil {
		t.Fatal(e)
	}
	code, out := f.call(
		t,
		f.operator,
		"POST",
		"/api/v1/tenants/t/projects/p/tasks",
		Object{
			"title":       "Fix traversal",
			"description": "a/../b passes",
			"criteria":    "reject raw ..",
			"mrIid":       1,
			"synthetic":   true,
		},
		0,
		"",
	)
	if code != 201 {
		t.Fatal(code, out)
	}
	b, _ := json.Marshal(out)
	_ = json.Unmarshal(b, &f.task)
	return f
}

func (f *fixture) call(t *testing.T, token, method, path string, b any, v int64, key string) (int, Object) {
	t.Helper()
	raw, _ := json.Marshal(b)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.AddCookie(&http.Cookie{Name: "forge_session", Value: token})
	if key == "" {
		key = Token()
	}
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	out := Object{}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Code, out
}

func (f *taskFixture) act(t *testing.T, action string, extra Object) (int, Object) {
	body := Object{"action": action, "version": f.task.Version, "head": f.task.Head, "text": "Fix raw traversal"}
	for k, v := range extra {
		body[k] = v
	}
	code, out := f.call(
		t,
		f.operator,
		"POST",
		"/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/actions",
		body,
		0,
		"",
	)
	if code == 200 {
		raw, _ := json.Marshal(out)
		_ = json.Unmarshal(raw, &f.task)
	}
	return code, out
}

func (f *taskFixture) refresh(t *testing.T) {
	code, out := f.call(t, f.operator, "GET", "/api/v1/tenants/t/projects/p/tasks/"+f.task.ID, nil, 0, "")
	if code != 200 {
		t.Fatal(code, out)
	}
	raw, _ := json.Marshal(out["task"])
	_ = json.Unmarshal(raw, &f.task)
}

// These tests exercise platform delivery and lease gates directly. They do not
// expose the retired Python callback endpoints in the production router.
func (f *taskFixture) internal(t *testing.T, path string, b Object) (int, Object) {
	t.Helper()
	b["runtimeEpoch"] = 1
	raw, _ := json.Marshal(b)
	var req taskRunRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	var result any
	var err error
	if strings.HasSuffix(path, "/gate") {
		result, err = f.a.checkGate(f.ctx, f.task.ID, req)
	} else {
		result, err = f.a.recordDelivery(f.ctx, f.task.ID, req)
	}
	w := httptest.NewRecorder()
	if err != nil {
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", path, nil)
		fail(c, err)
	} else {
		JSON(w, 200, result)
	}
	out := Object{}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return w.Code, out
}

func (f *taskFixture) dispatch(t *testing.T) Object {
	t.Helper()
	for range 2 {
		if err := f.a.reconcileRuns(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	f.refresh(t)
	var epoch int64
	if err := f.a.DB.QueryRow(f.ctx, `SELECT epoch FROM coding_runs WHERE id=$1 AND runtime_id='executor'`, *f.task.ActiveRun).
		Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return Object{"id": *f.task.ActiveRun, "epoch": epoch}
}

func (f *taskFixture) delivering(t *testing.T, round any) {
	t.Helper()
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_runs SET status='Delivering',output=output || '{"finishRequest":{"summary":"verified"}}'::jsonb WHERE id=$1`,
		round,
	); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDispatchRevokeAndLateDelivery(t *testing.T) {
	f := setupTask(t)
	code, out := f.act(t, "Resume", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	run := f.dispatch(t)
	body := Object{"runId": run["id"], "epoch": run["epoch"], "worker": "executor"}
	path := "/internal/v1/tasks/" + f.task.ID
	code, _ = f.internal(t, path+"/gate", body)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = f.act(t, "Intervene", nil)
	if code != 200 || f.task.StopConfirmed {
		t.Fatal(code)
	}
	code, _ = f.internal(t, path+"/gate", body)
	if code != 409 {
		t.Fatal("late write", code)
	}
	code, _ = f.act(t, "Resume", nil)
	if code != 409 {
		t.Fatal("resumed before stop", code)
	}
	body["status"] = "Passed"
	body["head"] = f.head
	code, _ = f.internal(t, path+"/finish", body)
	if code != 409 {
		t.Fatal("late delivery", code)
	}
	body["status"] = "Stopped"
	code, out = f.internal(t, path+"/finish", body)
	if code != 200 {
		t.Fatal(code, out)
	}
	f.refresh(t)
	if !f.task.StopConfirmed || f.task.ActiveRun != nil {
		t.Fatal(f.task)
	}
}

func TestTenantRevocationCannotReviveExecution(t *testing.T) {
	f := setupTask(t)
	_, _ = f.act(t, "Resume", nil)
	run := f.dispatch(t)
	body := Object{"runId": run["id"], "epoch": run["epoch"], "worker": "executor"}
	f.level.Store(0)
	if e := f.a.reconcileRuns(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.level.Store(40)
	code, _ := f.internal(t, "/internal/v1/tasks/"+f.task.ID+"/gate", body)
	if code != 409 {
		t.Fatal(code)
	}
	f.refresh(t)
	if f.task.Control != "Stopping" || f.task.StopConfirmed {
		t.Fatal(f.task)
	}
}

func TestExpiredRuntimeLeaseRejectsToolAndDelivery(t *testing.T) {
	f := setupTask(t)
	_, _ = f.act(t, "Resume", nil)
	run := f.dispatch(t)
	f.lease.Store(false)
	body := Object{
		"runId":  run["id"],
		"epoch":  run["epoch"],
		"worker": "executor",
		"status": "Passed",
		"head":   f.head,
	}
	for _, suffix := range []string{"gate", "finish"} {
		code, _ := f.internal(t, "/internal/v1/tasks/"+f.task.ID+"/"+suffix, body)
		if code != 409 {
			t.Fatal(suffix, code)
		}
	}
}

func TestTaskReadsRequireTenantProjectAuthorization(t *testing.T) {
	f := setupTask(t)
	for _, path := range []string{"/api/v1/tenants/foreign/projects/p/tasks/", "/api/v1/tenants/t/projects/foreign/tasks/"} {
		code, _ := f.call(t, f.operator, "GET", path+f.task.ID, nil, 0, "")
		if code != 404 {
			t.Fatal(code)
		}
	}
	f.level.Store(0)
	code, _ := f.call(t, f.operator, "GET", "/api/v1/tenants/t/projects/p/tasks/"+f.task.ID, nil, 0, "")
	if code != 404 {
		t.Fatal(code)
	}
}

func TestSnapshotSurvivesDispatchRetry(t *testing.T) {
	f := setupTask(t)
	_, _ = f.act(t, "Resume", nil)
	if e := f.a.reconcileRuns(f.ctx); e != nil {
		t.Fatal(e)
	}
	var first []byte
	if e := f.a.DB.QueryRow(f.ctx, `SELECT execution_snapshot FROM coding_runs WHERE id=$1`, *f.task.ActiveRun).
		Scan(&first); e != nil {
		t.Fatal(e)
	}
	_, e := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_project_settings SET settings=jsonb_set(settings,'{model}','"changed"')`,
	)
	if e != nil {
		t.Fatal(e)
	}
	f.dispatch(t)
	var second []byte
	_ = f.a.DB.QueryRow(f.ctx, `SELECT execution_snapshot FROM coding_runs WHERE id=$1`, *f.task.ActiveRun).
		Scan(&second)
	if !bytes.Equal(first, second) {
		t.Fatal("snapshot changed")
	}
	for range 2 {
		if e = table.Migrate(f.ctx, f.a.DB); e != nil {
			t.Fatal(e)
		}
	}
	_ = fmt.Sprint(second)
}

func TestTaskApprovalAndMRFacts(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "approval_is_not_completion", true: "cancel_is_retained_after_merge"}[cancel],
			func(t *testing.T) {
				f := setupTask(t)
				code, out := f.act(t, "Approve", nil)
				if code != 200 || f.task.Status != "Review" {
					t.Fatal(code, out)
				}
				if cancel {
					code, out = f.act(t, "Cancel", nil)
					if code != 200 {
						t.Fatal(code, out)
					}
				}
				f.mrState = "merged"
				code, out = f.call(
					t,
					f.operator,
					"POST",
					"/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/sync",
					Object{},
					0,
					"",
				)
				if code != 200 {
					t.Fatal(code, out)
				}
				f.refresh(t)
				want := "Completed"
				if cancel {
					want = "Canceled"
				}
				if f.task.Status != want {
					t.Fatal(f.task.Status, want)
				}
			},
		)
	}
}

func TestTaskTransferAndCodeVersion(t *testing.T) {
	f := setupTask(t)
	original := f.task
	code, out := f.act(t, "Transfer", Object{"ownerId": "member"})
	if code != 200 {
		t.Fatal(code, out)
	}
	if f.task.OwnerID != "member" || f.task.Head != original.Head || f.task.Control != original.Control ||
		f.task.MRIID != original.MRIID {
		t.Fatal(f.task)
	}
	code, _ = f.act(t, "Approve", nil)
	if code != 200 {
		t.Fatal(code)
	}
	f.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	code, out = f.call(
		t,
		f.operator,
		"POST",
		"/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/sync",
		Object{},
		0,
		"",
	)
	if code != 200 {
		t.Fatal(code, out)
	}
	f.refresh(t)
	if f.task.Head != f.head || f.task.ApprovalHead != "" || f.task.Checks != "NotRun" ||
		f.task.Control != "Human" {
		t.Fatal(f.task)
	}
	code, _ = f.act(t, "Approve", Object{"head": original.Head})
	if code != 409 {
		t.Fatal("stale approval accepted", code)
	}
}

func TestQueuedTaskInterventionNeedsNoWorkerStop(t *testing.T) {
	f := setupTask(t)
	code, out := f.act(t, "Resume", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	code, out = f.act(t, "Intervene", nil)
	if code != 200 || !f.task.StopConfirmed || f.task.Control != "Human" || f.task.ActiveRun != nil {
		t.Fatal(code, out)
	}
	code, out = f.act(t, "Resume", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
}

func TestProjectConfigChangesRequireFreshCheck(t *testing.T) {
	f := setupTask(t)
	body := Object{
		"agentID":        "coding-agent",
		"agentVersion":   1,
		"version":        1,
		"baseBranch":     "master",
		"model":          "local-model",
		"allowedPaths":   []string{"src/"},
		"checks":         []string{"go test ./..."},
		"repairLimit":    3,
		"timeoutMinutes": 30,
	}
	code, out := f.call(t, f.operator, "PUT", "/api/v1/tenants/t/projects/p/coding-settings", body, 0, "")
	if code != 200 || out["version"] != float64(2) || out["checkedAt"] != nil {
		t.Fatal(code, out)
	}
	if code, out := f.act(t, "Resume", nil); code != 409 {
		t.Fatal(code, out)
	}
	if code, out := f.call(
		t,
		f.operator,
		"PUT",
		"/api/v1/tenants/t/projects/p/coding-settings",
		body,
		0,
		"",
	); code != 412 {
		t.Fatal(code, out)
	}
}

func TestTaskGitLabLinkUsesPublishedResource(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		mr                   int
		branchStatus         int
		wantTarget, wantPath string
		wantStatus           int
	}{
		{name: "unpublished branch", mr: 0, branchStatus: 404, wantTarget: "project", wantPath: "/task-test/repo", wantStatus: 200},
		{name: "published branch", mr: 0, branchStatus: 200, wantTarget: "branch", wantPath: "/task-test/repo/-/tree/agent%2Fexample", wantStatus: 200},
		{name: "existing MR", mr: 1, branchStatus: 500, wantTarget: "mergeRequest", wantPath: "/task-test/repo/-/merge_requests/1", wantStatus: 200},
		{name: "lookup failure is not absence", mr: 0, branchStatus: 500, wantTarget: "", wantPath: "", wantStatus: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupTask(t)
			f.a.Config.GitLabPublicURL = "https://gitlab.example.test"
			_, err := f.a.DB.Exec(
				f.ctx,
				`UPDATE coding_tasks SET mr_iid=$1,branch='agent/example' WHERE id=$2`,
				tc.mr,
				f.task.ID,
			)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.EscapedPath() != "/api/v4/projects/77/repository/branches/agent%2Fexample" {
					t.Errorf("unexpected branch query %s", r.URL.EscapedPath())
				}
				JSON(
					w,
					tc.branchStatus,
					Object{"name": "agent/example", "commit": Object{"id": f.head}},
				)
			}))
			defer server.Close()
			setGitLab(t, f.a, server.URL, "test-gitlab-token")
			status, out := f.call(
				t,
				f.operator,
				"GET",
				"/api/v1/tenants/t/projects/p/tasks/"+f.task.ID,
				nil,
				0,
				"",
			)
			if status != tc.wantStatus {
				t.Fatalf("status %d, want %d", status, tc.wantStatus)
			}
			if tc.wantStatus == 200 &&
				(out["gitlabTarget"] != tc.wantTarget || out["gitlabUrl"] != "https://gitlab.example.test"+tc.wantPath) {
				t.Fatalf("wrong link: target=%v url=%v", out["gitlabTarget"], out["gitlabUrl"])
			}
			if tc.mr > 0 && calls != 0 {
				t.Fatal("existing MR should not depend on source branch lookup")
			}
		})
	}
}

func TestTaskApprovalMarksMRReady(t *testing.T) {
	f := setupTask(t)
	code, out := f.act(t, "Approve", nil)
	if code != 200 || f.task.ApprovalHead != f.task.Head {
		t.Fatal(code, out)
	}
	if f.mrDraft || f.mrTitle != "Fix traversal" || f.mrWrites.Load() != 1 {
		t.Fatalf("approved MR is still draft: %s (writes=%d)", f.mrTitle, f.mrWrites.Load())
	}
	code, out = f.act(t, "Approve", nil)
	if code != 200 || f.mrWrites.Load() != 1 {
		t.Fatal("ready retry must not update MR", code, out)
	}
	code, out = f.act(t, "Resume", nil)
	if code != 200 || !f.mrDraft || f.task.ApprovalHead != "" || f.task.ActiveRun == nil {
		t.Fatal("next round must restore draft", code, out, f.mrDraft)
	}
}

func TestTaskApprovalRejectsUnsafeMR(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *taskFixture)
		code   int
		writes int32
	}{
		{name: "remote_commit_changed", setup: func(t *testing.T, f *taskFixture) { f.head = strings.Repeat("b", 40) }, code: 409, writes: 0},
		{name: "closed", setup: func(t *testing.T, f *taskFixture) { f.mrState = "closed" }, code: 409, writes: 0},
		{name: "no_mr", setup: func(t *testing.T, f *taskFixture) {
			_, e := f.a.DB.Exec(f.ctx, `UPDATE coding_tasks SET mr_iid=0 WHERE id=$1`, f.task.ID)
			if e != nil {
				t.Fatal(e)
			}
		}, code: 409, writes: 0},
		{name: "not_stopped", setup: func(t *testing.T, f *taskFixture) {
			_, e := f.a.DB.Exec(f.ctx, `UPDATE coding_tasks SET stop_confirmed=false WHERE id=$1`, f.task.ID)
			if e != nil {
				t.Fatal(e)
			}
		}, code: 409, writes: 0},
		{name: "running", setup: func(t *testing.T, f *taskFixture) {
			code, out := f.act(t, "Resume", nil)
			if code != 200 {
				t.Fatal(code, out)
			}
		}, code: 409, writes: 0},
		{name: "not_reviewer", setup: func(t *testing.T, f *taskFixture) {
			_, e := f.a.DB.Exec(f.ctx, `UPDATE coding_tasks SET reviewer_ids='{}' WHERE id=$1`, f.task.ID)
			if e != nil {
				t.Fatal(e)
			}
		}, code: 403, writes: 0},
		{name: "gitlab_failure", setup: func(t *testing.T, f *taskFixture) { f.mrUpdateStatus = 503 }, code: 502, writes: 1},
		{name: "still_draft", setup: func(t *testing.T, f *taskFixture) { f.mrUpdateIgnored = true }, code: 502, writes: 1},
		{name: "changed_during_update", setup: func(t *testing.T, f *taskFixture) { f.mrUpdateHead = strings.Repeat("b", 40) }, code: 409, writes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupTask(t)
			tc.setup(t, f)
			code, out := f.act(t, "Approve", nil)
			if code != tc.code || f.mrWrites.Load() != tc.writes {
				t.Fatal(code, out, "writes", f.mrWrites.Load())
			}
			var approved string
			if e := f.a.DB.QueryRow(f.ctx, `SELECT approval_head FROM coding_tasks WHERE id=$1`, f.task.ID).
				Scan(&approved); e != nil {
				t.Fatal(e)
			}
			if approved != "" {
				t.Fatal("failed remote transition saved local approval")
			}
		})
	}
}

func TestTaskResumeRequiresDraftTransition(t *testing.T) {
	f := setupTask(t)
	f.mrDraft = false
	f.mrTitle = "Fix traversal"
	f.mrUpdateStatus = 503
	code, out := f.act(t, "Resume", nil)
	if code != 502 {
		t.Fatal(code, out)
	}
	var runs int
	if e := f.a.DB.QueryRow(f.ctx, `SELECT count(*) FROM coding_runs WHERE task_id=$1`, f.task.ID).
		Scan(&runs); e != nil {
		t.Fatal(e)
	}
	if runs != 0 {
		t.Fatal("queued execution despite failed draft transition")
	}
}

func TestRuntimeNeedsReviewStopsAndPreservesReason(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.dispatch(t)
	f.runtimeStatus = "NeedsReview"
	if err := f.a.reconcileRuns(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.ActiveRun != nil || !f.task.StopConfirmed || f.task.Status != "Failed" {
		t.Fatal(f.task)
	}
	if f.a.Workspaces.(*testWorkspace).stops == 0 {
		t.Fatal("compute was not stopped")
	}
}

func TestCompletedRuntimeCanStillBeStoppedDuringDelivery(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	run := f.dispatch(t)
	f.delivering(t, run["id"])
	f.runtimeStatus = "Completed"
	if code, out := f.act(t, "Intervene", nil); code != 200 {
		t.Fatal(code, out)
	}
	if err := f.a.reconcileRuns(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.ActiveRun != nil || !f.task.StopConfirmed || f.task.Control != "Human" {
		t.Fatal(f.task)
	}
	if f.a.Workspaces.(*testWorkspace).stops == 0 {
		t.Fatal("delivery compute was not stopped")
	}
}

func TestCompletedRuntimeWithoutFinishFailsAndStops(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.dispatch(t)
	f.runtimeStatus = "Completed"
	if err := f.a.reconcileRuns(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.ActiveRun != nil || f.task.Status != "Failed" || !f.task.StopConfirmed {
		t.Fatal(f.task)
	}
}

type testWorkspace struct {
	endpoint                   workspace.Endpoint
	stops                      int
	stopped                    []string
	pauses, resumes, published int
	jobResults                 map[string][]byte
	jobError                   error
	cleanupError               error
	ensureError, fenceError    error
}

func (w *testWorkspace) Ensure(context.Context, string, string, workspace.Repository) (workspace.Endpoint, error) {
	return w.endpoint, w.ensureError
}

func (w *testWorkspace) Access(context.Context, string) (workspace.Endpoint, error) {
	return w.endpoint, nil
}

func (w *testWorkspace) Fence(context.Context, string, string, int64) error { return w.fenceError }

func (w *testWorkspace) Stop(_ context.Context, id string) error {
	w.stops++
	w.stopped = append(w.stopped, id)
	return nil
}

func (w *testWorkspace) CleanupEvaluation(context.Context, string, string) error {
	w.stops++
	return w.cleanupError
}

func (w *testWorkspace) StopAll(context.Context, string, string) error { w.stops++; return nil }

func (w *testWorkspace) RunJob(_ context.Context, _, _ string, phase string, _ map[string]string) ([]byte, error) {
	if w.jobError != nil {
		return nil, w.jobError
	}
	if result, ok := w.jobResults[phase]; ok {
		return result, nil
	}
	return nil, workspace.ErrPreparing
}

func (w *testWorkspace) Verifier(context.Context, string, string) (workspace.Endpoint, error) {
	return w.endpoint, nil
}

func (w *testWorkspace) Pause(context.Context, string, string) error { w.pauses++; return nil }

func (w *testWorkspace) Resume(context.Context, string, string) error { w.resumes++; return nil }

func (w *testWorkspace) Published(context.Context, string, string) error { w.published++; return nil }

func setGitLab(t *testing.T, a *App, endpoint, token string) {
	t.Helper()
	g, err := gitlab.New(endpoint, token, "", a.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	a.GitLab = g
}
