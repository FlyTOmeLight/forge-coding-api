package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
)

// Each test owns both source and target branches; the project default branch is untouched.
func setupGitLabTask(t *testing.T) (*taskFixture, gitlab.MergeRequest, func(string, string, any, any)) {
	t.Helper()
	base, token := os.Getenv("TEST_GITLAB_URL"), os.Getenv("TEST_GITLAB_TOKEN")
	project, err := strconv.ParseInt(os.Getenv("TEST_GITLAB_PROJECT_ID"), 10, 64)
	if base == "" || token == "" || err != nil || project <= 0 {
		t.Skip("TEST_GITLAB_URL, TEST_GITLAB_TOKEN and TEST_GITLAB_PROJECT_ID required")
	}
	f := setupTask(t)
	f.gitlabProjectID = project
	setGitLab(t, f.a, base, token)
	f.a.Config.EnabledProjects[strconv.FormatInt(project, 10)] = true
	endpoint := fmt.Sprintf("%s/api/v4/projects/%d", strings.TrimRight(base, "/"), project)
	call := func(method, path string, body any, target any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, e := http.NewRequestWithContext(f.ctx, method, endpoint+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		req.Header.Set("Content-Type", "application/json")
		res, e := f.a.HTTP.Do(req)
		if e != nil {
			t.Fatal("GitLab request failed", method, path)
		}
		defer res.Body.Close()
		if method == "DELETE" && res.StatusCode == http.StatusNotFound {
			return
		} // GitLab may delete the source branch on merge.
		if res.StatusCode >= 300 {
			t.Fatalf("GitLab %s %s: HTTP %d", method, path, res.StatusCode)
		}
		if target != nil {
			if e = json.NewDecoder(res.Body).Decode(target); e != nil {
				t.Fatal(e)
			}
		}
	}
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	call("GET", "", nil, &p)
	target := "forge-sync-check-" + Token()[:12]
	call("POST", "/repository/branches", Object{"branch": target, "ref": p.DefaultBranch}, nil)
	t.Cleanup(func() { call("DELETE", "/repository/branches/"+url.PathEscape(target), nil, nil) })
	branch := target + "-source"
	var commit struct {
		ID string `json:"id"`
	}
	call(
		"POST",
		"/repository/commits",
		Object{
			"branch":         branch,
			"start_branch":   target,
			"commit_message": "Verify Forge MR readiness",
			"actions": []Object{
				{
					"action":    "create",
					"file_path": ".forge-ready-check",
					"content":   "isolated readiness verification\n",
				},
			},
		},
		&commit,
	)
	t.Cleanup(func() { call("DELETE", "/repository/branches/"+url.PathEscape(branch), nil, nil) })
	var mr gitlab.MergeRequest
	call(
		"POST",
		"/merge_requests",
		Object{
			"source_branch": branch,
			"target_branch": target,
			"title":         "Draft: Forge readiness verification",
		},
		&mr,
	)
	t.Cleanup(func() { call("DELETE", fmt.Sprintf("/merge_requests/%d", mr.IID), nil, nil) })
	if !mr.Draft {
		t.Fatal("fixture MR must start draft")
	}
	_, err = f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_tasks SET mr_iid=$2,branch=$3,target_branch=$4,head=$5 WHERE id=$1`,
		f.task.ID,
		mr.IID,
		branch,
		target,
		commit.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	return f, mr, call
}

func TestTaskApprovalWithGitLab(t *testing.T) {
	f, mr, _ := setupGitLabTask(t)
	project, head := f.gitlabProjectID, f.task.Head
	code, out := f.act(t, "Approve", nil)
	if code != 200 || f.task.ApprovalHead != head {
		t.Fatal(code, out)
	}
	ready, err := f.a.GitLab.MergeRequest(f.ctx, project, mr.IID)
	if err != nil || ready.Draft || ready.Head != head || ready.State != "opened" {
		t.Fatalf("approval did not mark exact revision ready: draft=%v state=%s", ready.Draft, ready.State)
	}
	code, out = f.act(t, "Resume", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	draft, err := f.a.GitLab.MergeRequest(f.ctx, project, mr.IID)
	if err != nil || !draft.Draft || f.task.ApprovalHead != "" {
		t.Fatal("new round did not restore draft")
	}
	t.Logf("GitLab project %d MR !%d: Draft → Ready → Draft; target branch untouched", project, mr.IID)
}

func TestGitLabMergeAutomaticallyCompletesTask(t *testing.T) {
	f, mr, call := setupGitLabTask(t)
	if code, out := f.act(t, "Approve", nil); code != 200 {
		t.Fatal(code, out)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.a.RunDispatcher(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("dispatcher did not stop")
		}
	}()
	path := fmt.Sprintf("/merge_requests/%d", mr.IID)
	deadline := time.Now().Add(15 * time.Second)
	for {
		var state map[string]any
		call("GET", path, nil, &state)
		if state["detailed_merge_status"] == "mergeable" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture MR not mergeable: %v", state["detailed_merge_status"])
		}
		time.Sleep(100 * time.Millisecond)
	}
	var merged gitlab.MergeRequest
	call("PUT", path+"/merge", Object{"sha": f.task.Head}, &merged)
	if merged.State != "merged" {
		t.Fatal("fixture MR was not merged")
	}
	start := time.Now()
	deadline = time.Now().Add(12 * time.Second)
	for {
		var status string
		if err := f.a.DB.QueryRow(f.ctx, `SELECT status FROM coding_tasks WHERE id=$1`, f.task.ID).
			Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "Completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("merged MR never automatically completed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var events int
	if err := f.a.DB.QueryRow(f.ctx, `SELECT count(*) FROM coding_events WHERE task_id=$1 AND actor_id='gitlab-sync' AND detail->>'mrState'='merged'`, f.task.ID).
		Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatal("missing automatic completion event", events)
	}
	t.Logf(
		"GitLab project %d MR !%d merged into isolated target; Forge automatically Completed in %s, without /sync",
		f.gitlabProjectID,
		mr.IID,
		time.Since(start).Round(time.Millisecond),
	)
}
