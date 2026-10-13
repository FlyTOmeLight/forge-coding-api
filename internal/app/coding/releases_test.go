package coding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func TestCodingReleaseGatesHistoryRecoveryAndFrozenTasks(t *testing.T) {
	f := setupTask(t)
	original, err := f.a.codingSettings(f.ctx, "t", "p")
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := url.Parse(f.a.Config.RuntimeURL)
	proxy := httputil.NewSingleHostReverseProxy(remote)
	approvalStatus := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/coding-release-approval" {
			proxy.ServeHTTP(w, r)
			return
		}
		if approvalStatus != 200 {
			JSON(
				w,
				approvalStatus,
				Object{"error": Object{"code": "EvaluationGateFailed", "message": "未通过评测"}},
			)
			return
		}
		var req Object
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["tenantID"] != "t" || req["projectID"] != "p" || req["actorID"] != "operator" {
			t.Error("approval identity not derived from session")
		}
		JSON(
			w,
			200,
			Object{
				"experimentID":   "approved",
				"agentID":        "candidate",
				"agentVersion":   2,
				"suiteDigest":    "digest",
				"runtimeAgentID": "coding-agent",
				"runtimeVersion": 1,
				"model":          "local-model",
				"scenario": Object{
					"baseHead":     strings.Repeat("a", 40),
					"allowedPaths": []string{"src/calc.py"},
					"checks":       []string{"python3 -m unittest"},
				},
			},
		)
	}))
	defer server.Close()
	f.a.Config.RuntimeURL = server.URL
	path := "/api/v1/tenants/t/projects/p/coding-releases"
	body := Object{
		"settingsVersion": original.Version,
		"experimentID":    "approved",
		"agentID":         "candidate",
		"agentVersion":    2,
	}
	assert := func(want int, token, method, path string, body Object, key string) Object {
		t.Helper()
		code, out := f.call(t, token, method, path, body, 0, key)
		if code != want {
			t.Fatalf("want %d got %d: %+v", want, code, out)
		}
		return out
	}
	assert(403, f.member, "POST", path, body, "")
	assert(404, f.other, "POST", path, body, "")
	stale := Object{"settingsVersion": 0}
	assert(412, f.operator, "POST", path, stale, "")
	approvalStatus = 409
	assert(409, f.operator, "POST", path, body, "")
	approvalStatus = 200
	f.head = strings.Repeat("b", 40)
	assert(409, f.operator, "POST", path, body, "")
	f.head = strings.Repeat("a", 40)
	key := Token()
	enabled := assert(201, f.operator, "POST", path, body, key)
	replay := assert(200, f.operator, "POST", path, body, key)
	if enabled["id"] != replay["id"] {
		t.Fatal("retry created another release")
	}
	conflict := Object{"settingsVersion": 2}
	assert(409, f.operator, "POST", path, conflict, key)
	current, err := f.a.codingSettings(f.ctx, "t", "p")
	if err != nil || current.ReleaseID != enabled["id"] || current.Checks[0] != "python3 -m unittest" ||
		current.CheckedAt == nil {
		t.Fatal(current, err)
	}
	task := assert(
		201,
		f.operator,
		"POST",
		"/api/v1/tenants/t/projects/p/tasks/new",
		Object{
			"title":       "New task",
			"description": "Fix code",
			"reproduce":   "Run tests",
			"criteria":    "Tests pass",
		},
		Token(),
	)
	var frozen []byte
	if err := f.a.DB.QueryRow(f.ctx, `SELECT config_snapshot FROM coding_runs WHERE task_id=$1`, task["id"]).
		Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	history := assert(200, f.member, "GET", path, nil, "")
	items := history["items"].([]any)
	if len(items) != 2 {
		t.Fatal("history", history)
	}
	baseline := ""
	for _, item := range items {
		row := item.(map[string]any)
		if row["action"] == "Baseline" {
			baseline = row["id"].(string)
		}
	}
	restorePath := path + "/" + baseline + "/restore"
	assert(412, f.operator, "POST", restorePath, Object{"settingsVersion": original.Version}, Token())
	assert(404, f.operator, "POST", path+"/foreign/restore", Object{"settingsVersion": current.Version}, Token())
	restored := assert(201, f.operator, "POST", restorePath, Object{"settingsVersion": current.Version}, Token())
	if restored["action"] != "Restore" {
		t.Fatal(restored)
	}
	restoredSettings, err := f.a.codingSettings(f.ctx, "t", "p")
	if err != nil || strings.Join(restoredSettings.Checks, ",") != strings.Join(original.Checks, ",") {
		t.Fatal(restoredSettings, err)
	}
	var taskRecord table.Task
	encoded, _ := json.Marshal(task)
	_ = json.Unmarshal(encoded, &taskRecord)
	pinned, err := f.a.readyTaskSettings(f.ctx, taskRecord)
	if err != nil || pinned.ReleaseID != current.ReleaseID || pinned.Checks[0] != "python3 -m unittest" {
		t.Fatal("queued task retargeted", pinned, err)
	}
	saved := restoredSettings
	saved.Checks = []string{"true"}
	raw, _ := json.Marshal(saved)
	mutated := Object{}
	_ = json.Unmarshal(raw, &mutated)
	assert(409, f.operator, "PUT", "/api/v1/tenants/t/projects/p/coding-settings", mutated, Token())
	if err := table.Migrate(f.ctx, f.a.DB); err != nil {
		t.Fatal(err)
	}
	got := assert(200, f.operator, "GET", path, nil, "")
	if len(got["items"].([]any)) != 3 {
		t.Fatal("history lost on migration")
	}
}

func TestNewProjectCannotBypassEvaluationWithRuntimeBinding(t *testing.T) {
	f := setupTask(t)
	if _, err := f.a.DB.Exec(f.ctx, `DELETE FROM coding_project_settings WHERE project_id='p'`); err != nil {
		t.Fatal(err)
	}
	parameters := CodingSettings{
		BaseBranch:     "master",
		AllowedPaths:   []string{"src"},
		Checks:         []string{"go test ./..."},
		RepairLimit:    1,
		TimeoutMinutes: 10,
	}
	path := "/api/v1/tenants/t/projects/p/coding-settings"
	arbitrary := parameters
	arbitrary.AgentID = "coding-agent"
	arbitrary.AgentVersion = 1
	raw, _ := json.Marshal(arbitrary)
	body := Object{}
	_ = json.Unmarshal(raw, &body)
	if code, out := f.call(t, f.operator, "PUT", path, body, 0, ""); code != 409 {
		t.Fatal(code, out)
	}
	raw, _ = json.Marshal(parameters)
	_ = json.Unmarshal(raw, &body)
	delete(body, "agentID")
	delete(body, "agentVersion")
	if code, out := f.call(t, f.operator, "PUT", path, body, 0, ""); code != 200 {
		t.Fatal(code, out)
	}
	settings, err := f.a.codingSettings(f.ctx, "t", "p")
	if err != nil || settings.Version != 1 || settings.AgentID != "" || settings.CheckedAt != nil {
		t.Fatal(settings, err)
	}
}
