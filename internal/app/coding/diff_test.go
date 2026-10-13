package coding

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTaskDiffReadsCurrentMRInsteadOfHistoricalRound(t *testing.T) {
	f := setupTask(t)
	old := "diff --git a/src/calc.py b/src/calc.py\n--- a/src/calc.py\n+++ b/src/calc.py\n@@ -0,0 +1 @@\n+def add(a,b): return a+b\n"
	patch := "diff --git a/src/calc.py b/src/calc.py\n--- a/src/calc.py\n+++ b/src/calc.py\n@@ -0,0 +1,2 @@\n+# Current MR comment\n+def add(a,b): return a+b\n"
	head, base := strings.Repeat("b", 40), strings.Repeat("c", 40)
	if _, err := f.a.DB.Exec(
		f.ctx,
		`INSERT INTO coding_runs(id,task_id,epoch,head,status,requirements,config_snapshot,output) VALUES('old-round',$1,1,$2,'Passed','first change','{}',jsonb_build_object('patch',$3::text)),('latest-round',$1,2,$4,'Passed','validate existing code','{}','{"patch":""}')`,
		f.task.ID,
		f.head,
		old,
		head,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_tasks SET mr_iid=9,head=$2 WHERE id=$1`,
		f.task.ID,
		head,
	); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	var unavailable, changing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" || r.Header.Get("PRIVATE-TOKEN") != "test-gitlab-token" {
			t.Error("diff request must be an authenticated read")
		}
		if unavailable.Load() {
			JSON(w, 503, Object{"message": "GitLab unavailable"})
			return
		}
		switch r.URL.Path {
		case "/api/v4/projects/77/merge_requests/9":
			h := head
			if changing.Load() && reads.Load()%3 == 0 {
				h = strings.Repeat("d", 40)
			}
			JSON(
				w,
				200,
				Object{
					"iid":           9,
					"sha":           h,
					"source_branch": "agent/fix",
					"target_branch": "master",
					"diff_refs":     Object{"base_sha": base, "head_sha": h, "start_sha": base},
				},
			)
		case "/api/v4/projects/77/merge_requests/9/raw_diffs":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(patch))
		default:
			t.Error("wrong project or MR path", r.URL.Path)
			JSON(w, 404, Object{})
		}
	}))
	defer server.Close()
	setGitLab(t, f.a, server.URL, "test-gitlab-token")
	path := "/api/v1/tenants/t/projects/p/tasks/" + f.task.ID + "/diff"
	code, out := f.call(t, f.member, "GET", path, nil, 0, "")
	if code != 200 || out["patch"] != patch || out["head"] != head || out["base"] != base ||
		out["mrIid"] != float64(9) {
		t.Fatal(code, out)
	}
	if reads.Load() != 3 {
		t.Fatal("expected consistent MR metadata and raw diff reads", reads.Load())
	}
	for _, bad := range []string{strings.Replace(path, "/t/projects/", "/other/projects/", 1), strings.Replace(path, "/p/tasks/", "/other/tasks/", 1), strings.Replace(path, f.task.ID, "missing-task", 1)} {
		if code, _ = f.call(t, f.member, "GET", bad, nil, 0, ""); code != 404 {
			t.Fatal("scope mismatch accepted", bad, code)
		}
	}
	if code, _ = f.call(t, f.other, "GET", path, nil, 0, ""); code != 404 {
		t.Fatal("read permission bypass", code)
	}
	if reads.Load() != 3 {
		t.Fatal("unauthorized request reached GitLab", reads.Load())
	}
	unavailable.Store(true)
	if code, out = f.call(t, f.member, "GET", path, nil, 0, ""); code != 502 || out["patch"] != nil {
		t.Fatal("dependency failure used historical patch", code, out)
	}
	unavailable.Store(false)
	reads.Store(0)
	changing.Store(true)
	if code, out = f.call(t, f.member, "GET", path, nil, 0, ""); code != 409 || out["patch"] != nil {
		t.Fatal("mixed diff versions returned", code, out)
	}
	changing.Store(false)
	reads.Store(0)
	if _, err := f.a.DB.Exec(f.ctx, `UPDATE coding_tasks SET mr_iid=0 WHERE id=$1`, f.task.ID); err != nil {
		t.Fatal(err)
	}
	if code, out = f.call(
		t,
		f.member,
		"GET",
		path,
		nil,
		0,
		"",
	); code != 200 || out["patch"] != "" || out["mrIid"] != float64(0) ||
		reads.Load() != 0 {
		t.Fatal("task without MR borrowed a saved round patch", code, out)
	}
}
