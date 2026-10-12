package coding

import (
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	"strings"
	"testing"
)

// GitLab updates an MR's diff metadata separately from the source branch ref.
// Delivery must check the ref, both when the MR lags a push and a human moves it.
func TestFinishUsesCurrentSourceBranchRevision(t *testing.T) {
	for _, tc := range []struct {
		name       string
		branch, mr string
		want       int
	}{
		{"mr_metadata_lags_agent_push", strings.Repeat("b", 40), strings.Repeat("a", 40), 200},
		{"mr_metadata_lags_human_push", strings.Repeat("c", 40), strings.Repeat("b", 40), 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupTask(t)
			if code, out := f.act(t, "Resume", nil); code != 200 {
				t.Fatal(code, out)
			}
			run := f.dispatch(t)
			body := Object{"runId": run["id"], "epoch": run["epoch"], "worker": "executor", "head": strings.Repeat("b", 40)}
			path := "/internal/v1/tasks/" + f.task.ID
			if code, out := f.internal(t, path+"/gate", body); code != 200 {
				t.Fatal(code, out)
			}
			f.delivering(t, run["id"])
			f.head = tc.branch
			f.mrHeadOverride = tc.mr
			body["status"] = "Passed"
			body["mrIid"] = 1
			body["output"] = Object{"summary": "comments checked", "checksCommit": strings.Repeat("b", 40)}
			code, out := f.internal(t, path+"/finish", body)
			if code != tc.want {
				t.Fatal(code, out)
			}
			f.refresh(t)
			if tc.want == 200 && (f.task.Head != tc.branch || f.task.Checks != "Passed" || f.task.ActiveRun != nil) {
				t.Fatal(f.task)
			}
			if tc.want == 200 {
				version := f.task.Version
				if err := f.a.reconcileGitLabTasks(f.ctx); err != nil {
					t.Fatal(err)
				}
				f.refresh(t)
				if f.task.Head != tc.branch || f.task.Checks != "Passed" || f.task.Version != version {
					t.Fatal("stale MR metadata reverted a verified delivery", f.task)
				}
			}
			if tc.want == 409 && f.task.ActiveRun == nil {
				t.Fatal("unverified delivery was accepted")
			}
		})
	}
}

func TestFailedPublicationWithoutRemoteBranchStops(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	run := f.dispatch(t)
	f.delivering(t, run["id"])
	state := Object{"finishRequest": Object{"summary": "candidate"}, "artifact": workspace.Artifact{ExpectedHead: strings.Repeat("b", 40)}, "deliveryChecks": []Object{{"passed": true}}}
	if _, err := f.a.DB.Exec(f.ctx, `UPDATE coding_runs SET output=$2 WHERE id=$1`, run["id"], mustJSON(state)); err != nil {
		t.Fatal(err)
	}
	f.missingBranch = true
	f.a.Workspaces.(*testWorkspace).jobError = &workspace.OperationFailed{Phase: "publish", Detail: "checks modified candidate source"}
	if err := f.a.reconcileDelivery(f.ctx, run["id"].(string)); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.task.Status != "Failed" || f.task.ActiveRun != nil || !f.task.StopConfirmed {
		t.Fatal(f.task)
	}
}
