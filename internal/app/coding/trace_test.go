package coding

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskTraceReadScopeAndRuntimeBinding(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	var round string
	if err := f.a.DB.QueryRow(f.ctx, `SELECT id FROM coding_runs WHERE task_id=$1`, f.task.ID).
		Scan(&round); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/tenants/t/projects/p/tasks/" + f.task.ID + "/runs/" + round + "/trace"
	code, out := f.call(t, f.operator, "GET", path, nil, 0, "")
	if code != 200 || out["run"] != nil {
		t.Fatal(code, out)
	}
	calls := 0
	mismatch := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+f.a.Config.RuntimeToken ||
			r.URL.Query().Get("after") != "7" {
			t.Error("trace credentials/cursor not forwarded")
		}
		task := f.task.ID
		if mismatch {
			task = "another-task"
		}
		JSON(
			w,
			200,
			Object{
				"run": Object{
					"id":     "runtime-trace",
					"status": "Running",
					"scope": Object{
						"tenantID":  "t",
						"projectID": "p",
						"taskID":    task,
						"roundID":   round,
					},
				},
				"events":  []Object{},
				"nextSeq": 7,
				"hasMore": false,
			},
		)
	}))
	defer server.Close()
	f.a.Config.RuntimeURL = server.URL
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_runs SET runtime_id='runtime-trace' WHERE id=$1`,
		round,
	); err != nil {
		t.Fatal(err)
	}
	code, out = f.call(t, f.member, "GET", path+"?after=7", nil, 0, "")
	if code != 200 || calls != 1 || out["run"] == nil {
		t.Fatal(code, out, calls)
	}
	for _, tc := range []struct {
		user, path string
		want       int
	}{
		{user: f.other, path: path, want: 404},
		{user: f.operator, path: path + "?after=-1", want: 400},
		{user: f.operator, path: "/api/v1/tenants/t/projects/p/tasks/another-task/runs/" + round + "/trace", want: 404},
		{user: f.operator, path: "/api/v1/tenants/t/projects/p/tasks/" + f.task.ID + "/runs/missing/trace", want: 404},
	} {
		code, _ = f.call(t, tc.user, "GET", tc.path, nil, 0, "")
		if code != tc.want {
			t.Fatal(tc, code)
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized request reached runtime")
	}
	mismatch = true
	code, _ = f.call(t, f.operator, "GET", path+"?after=7", nil, 0, "")
	if code != 502 {
		t.Fatal("mismatched runtime scope accepted", code)
	}
}
