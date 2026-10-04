package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	a                       *App
	ctx                     context.Context
	operator, member, other string
}
type taskFixture struct {
	*fixture
	task          CodingTask
	head, mrState string
	level         atomic.Int32
	lease         atomic.Bool
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
	a, e := New(ctx, config.Config{DatabaseURL: u.String(), ServiceToken: strings.Repeat("a", 32), RuntimeToken: strings.Repeat("b", 32), TenantToken: strings.Repeat("c", 32), ModelURL: "http://model", ToolProjects: map[string]string{"77": "http://tools"}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		a.DB.Close()
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA `+name+` CASCADE`)
		_ = conn.Close(context.Background())
	})
	if e = a.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	f := &taskFixture{fixture: &fixture{a: a, ctx: ctx, operator: "operator", member: "member", other: "other"}, head: strings.Repeat("a", 40), mrState: "opened"}
	f.level.Store(40)
	f.lease.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
			JSON(w, 200, Project{ID: "p", TenantID: "t", GitLabID: 77, Path: "task-test/repo"})
			return
		case "/internal/v1/executions":
			JSON(w, 202, RuntimeRun{ID: "executor", Status: "Queued"})
			return
		case "/v1/runs/executor":
			JSON(w, 200, RuntimeRun{ID: "executor", Status: "Running"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/lease") {
			JSON(w, 200, Object{"allowed": f.lease.Load()})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/branches/") {
			JSON(w, 200, Object{"name": "master", "commit": Object{"id": f.head}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/discussions") {
			JSON(w, 200, []any{})
			return
		}
		JSON(w, 200, Object{"iid": 1, "state": f.mrState, "sha": f.head, "source_branch": "fix", "target_branch": "master"})
	}))
	t.Cleanup(remote.Close)
	a.Config.TenantURL = remote.URL
	a.Config.RuntimeURL = remote.URL
	a.GitLab.URL = remote.URL
	raw, _ := json.Marshal(CodingSettings{Version: 1, BaseBranch: "master", Model: "local-model", AllowedPaths: []string{"src/"}, Checks: []string{"go test ./..."}, RepairLimit: 3, TimeoutMinutes: 30})
	if _, e = a.DB.Exec(ctx, `INSERT INTO coding_project_settings(tenant_id,project_id,version,settings,checked_at) VALUES('t','p',1,$1,now())`, raw); e != nil {
		t.Fatal(e)
	}
	code, out := f.call(t, f.operator, "POST", "/api/v1/tenants/t/projects/p/tasks", Object{"title": "Fix traversal", "description": "a/../b passes", "criteria": "reject raw ..", "mrIid": 1, "synthetic": true}, 0, "")
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
	code, out := f.call(t, f.operator, "POST", "/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/actions", body, 0, "")
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
func (f *taskFixture) internal(t *testing.T, path string, b Object) (int, Object) {
	b["runtimeEpoch"] = 1
	raw, _ := json.Marshal(b)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+f.a.Config.ServiceToken)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	out := Object{}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Code, out
}
func (f *taskFixture) dispatch(t *testing.T) Object {
	t.Helper()
	for range 2 {
		if e := f.a.reconcileRuns(f.ctx); e != nil {
			t.Fatal(e)
		}
	}
	f.refresh(t)
	code, out := f.internal(t, "/internal/v1/rounds/"+*f.task.ActiveRun+"/start", Object{"worker": "executor"})
	if code != 200 {
		t.Fatal(code, out)
	}
	return Object{"id": out["id"], "epoch": out["epoch"]}
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
	body := Object{"runId": run["id"], "epoch": run["epoch"], "worker": "executor", "status": "Passed", "head": f.head}
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
	if e := f.a.DB.QueryRow(f.ctx, `SELECT execution_snapshot FROM coding_runs WHERE id=$1`, *f.task.ActiveRun).Scan(&first); e != nil {
		t.Fatal(e)
	}
	_, e := f.a.DB.Exec(f.ctx, `UPDATE coding_project_settings SET settings=jsonb_set(settings,'{model}','"changed"')`)
	if e != nil {
		t.Fatal(e)
	}
	f.dispatch(t)
	var second []byte
	_ = f.a.DB.QueryRow(f.ctx, `SELECT execution_snapshot FROM coding_runs WHERE id=$1`, *f.task.ActiveRun).Scan(&second)
	if !bytes.Equal(first, second) {
		t.Fatal("snapshot changed")
	}
	for range 2 {
		if e = f.a.Migrate(f.ctx); e != nil {
			t.Fatal(e)
		}
	}
	_ = fmt.Sprint(second)
}

func TestTaskApprovalAndMRFacts(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval_is_not_completion", true: "cancel_is_retained_after_merge"}[cancel], func(t *testing.T) {
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
			code, out = f.call(t, f.operator, "POST", "/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/sync", Object{}, 0, "")
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
		})
	}
}
func TestTaskTransferAndCodeVersion(t *testing.T) {
	f := setupTask(t)
	original := f.task
	code, out := f.act(t, "Transfer", Object{"ownerId": "member"})
	if code != 200 {
		t.Fatal(code, out)
	}
	if f.task.OwnerID != "member" || f.task.Head != original.Head || f.task.Control != original.Control || f.task.MRIID != original.MRIID {
		t.Fatal(f.task)
	}
	code, _ = f.act(t, "Approve", nil)
	if code != 200 {
		t.Fatal(code)
	}
	f.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	code, out = f.call(t, f.operator, "POST", "/api/v1/tenants/t/projects/p/tasks/"+f.task.ID+"/sync", Object{}, 0, "")
	if code != 200 {
		t.Fatal(code, out)
	}
	f.refresh(t)
	if f.task.Head != f.head || f.task.ApprovalHead != "" || f.task.Checks != "NotRun" || f.task.Control != "Human" {
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
	body := Object{"version": 1, "baseBranch": "master", "model": "local-model", "allowedPaths": []string{"src/"}, "checks": []string{"go test ./..."}, "repairLimit": 3, "timeoutMinutes": 30}
	code, out := f.call(t, f.operator, "PUT", "/api/v1/tenants/t/projects/p/coding-settings", body, 0, "")
	if code != 200 || out["version"] != float64(2) || out["checkedAt"] != nil {
		t.Fatal(code, out)
	}
	if code, out := f.act(t, "Resume", nil); code != 409 {
		t.Fatal(code, out)
	}
	if code, out := f.call(t, f.operator, "PUT", "/api/v1/tenants/t/projects/p/coding-settings", body, 0, ""); code != 412 {
		t.Fatal(code, out)
	}
}
