package task

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
)

func TestDispatcherAutomaticallyCompletesMergedTask(t *testing.T) {
	f := setupTask(t)
	f.mrState = "merged"
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		var status string
		if err := f.a.DB.QueryRow(f.ctx, `SELECT status FROM coding_tasks WHERE id=$1`, f.task.ID).
			Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "Completed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GitLab MR is merged, but background task status remains %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGitLabSyncPreservesUnchangedVersionsAndTerminalTasks(t *testing.T) {
	for _, state := range []string{"opened", "closed", "Canceled", "no_mr"} {
		t.Run(state, func(t *testing.T) {
			f := setupTask(t)
			switch state {
			case "closed":
				f.mrState = "closed"
			case "Canceled":
				if code, out := f.act(t, "Cancel", nil); code != 200 {
					t.Fatal(code, out)
				}
				f.mrState = "merged"
			case "no_mr":
				if _, err := f.a.DB.Exec(
					f.ctx,
					`UPDATE coding_tasks SET mr_iid=0 WHERE id=$1`,
					f.task.ID,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.refresh(t)
			want := "Review"
			if state == "closed" {
				want = "Undelivered"
			}
			if state == "Canceled" {
				want = "Canceled"
			}
			if f.task.Status != want {
				t.Fatal(f.task.Status, want)
			}
			version := f.task.Version
			f.mrState = "opened"
			if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.refresh(t)
			if f.task.Version != version || f.task.Status != want || f.task.ActiveRun != nil {
				t.Fatal("unchanged or reopened MR changed task", f.task)
			}
		})
	}
}

func TestGitLabSyncInvalidatesApprovalForNewCommit(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Approve", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.head = strings.Repeat("b", 40)
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.Head != f.head || f.task.ApprovalHead != "" || f.task.Checks != "NotRun" ||
		f.task.Control != "Human" {
		t.Fatal(f.task)
	}
}

func TestGitLabSyncFailureDoesNotBlockOtherTasksAndRetries(t *testing.T) {
	f := setupTask(t)
	_, err := f.a.DB.Exec(
		f.ctx,
		`INSERT INTO coding_tasks(id,tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,mr_iid,head,reviewer_ids) SELECT 'zz_task',tenant_id,project_id,owner_id,title,description,criteria,branch,target_branch,2,head,reviewer_ids FROM coding_tasks WHERE id=$1`,
		f.task.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	upstream := f.a.GitLab
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/77/merge_requests/1":
			JSON(w, 503, Object{"message": "unavailable"})
		case "/api/v4/projects/77/merge_requests/2":
			JSON(w, 200, Object{"iid": 2, "state": "merged", "sha": f.head})
		default:
			JSON(w, 200, []any{})
		}
	}))
	defer remote.Close()
	setGitLab(t, f.a, remote.URL, "test-gitlab-token")
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	var original, other string
	if err := f.a.DB.QueryRow(f.ctx, `SELECT status FROM coding_tasks WHERE id=$1`, f.task.ID).
		Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.QueryRow(f.ctx, `SELECT status FROM coding_tasks WHERE id='zz_task'`).
		Scan(&other); err != nil {
		t.Fatal(err)
	}
	if original != "Review" || other != "Completed" {
		t.Fatal(original, other)
	}
	f.a.GitLab = upstream
	f.mrState = "merged"
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.Status != "Completed" {
		t.Fatal("failed task was not retried")
	}
}

func TestGitLabSyncCommentsAreIdempotent(t *testing.T) {
	f := setupTask(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/discussions") {
			JSON(
				w,
				200,
				[]any{
					Object{
						"id": "discussion",
						"notes": []any{
							Object{"id": 1, "body": "Please review", "resolved": false},
						},
					},
				},
			)
			return
		}
		JSON(w, 200, Object{"iid": 1, "state": "opened", "sha": f.head})
	}))
	defer upstream.Close()
	setGitLab(t, f.a, upstream.URL, "test-gitlab-token")
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	version := f.task.Version
	if !strings.Contains(string(f.task.Discussions), "Please review") {
		t.Fatal("comments were not imported")
	}
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	var events int
	if err := f.a.DB.QueryRow(f.ctx, `SELECT count(*) FROM coding_events WHERE task_id=$1 AND type='GitLabSync'`, f.task.ID).
		Scan(&events); err != nil {
		t.Fatal(err)
	}
	if f.task.Version != version || events != 1 {
		t.Fatal("unchanged comments generated redundant updates", events)
	}
}

func TestMergedTaskStaysCompletedWhileQueuedRunStops(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.mrState = "merged"
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reconcileRuns(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.Status != "Completed" || f.task.ActiveRun != nil || !f.task.StopConfirmed {
		t.Fatal("stopping execution overwrote GitLab completion", f.task.Status)
	}
}

func TestMergedTaskStaysCompletedAfterModelFailure(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	for range 2 {
		if err := f.a.reconcileRuns(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, RuntimeRun{ID: "executor", Status: "Failed"})
	}))
	defer runtime.Close()
	f.a.Config.RuntimeURL = runtime.URL
	f.a.Runtime = agentic.New(runtime.URL, f.a.Config.RuntimeToken, f.a.HTTP)
	f.mrState = "merged"
	if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reconcileRuns(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.Status != "Completed" || f.task.ActiveRun != nil || !f.task.StopConfirmed {
		t.Fatal("model result overwrote GitLab completion", f.task.Status)
	}
}
