package coding

import (
	"strings"
	"testing"

	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

func TestDeliveryRetainsTaskSandbox(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	run := f.dispatch(t)
	f.delivering(t, run["id"])
	w := f.a.Workspaces.(*testWorkspace)
	head := strings.Repeat("b", 40)
	w.jobResults = map[string][]byte{
		"snapshot": mustJSON(
			workspace.Artifact{ExpectedHead: head, Tree: "tree", Changed: []string{"src/code.go"}},
		),
		"publish": mustJSON(workspace.PublishReceipt{Head: head, Tree: "tree", Changed: true}),
	}
	f.head = head
	for i := 0; i < 4; i++ {
		if err := f.a.reconcileDelivery(f.ctx, run["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	f.refresh(t)
	if f.task.ActiveRun != nil || f.task.Checks != "Passed" {
		t.Fatal(f.task)
	}
	for _, id := range w.stopped {
		if id == f.task.ID {
			t.Fatal("successful delivery destroyed task compute")
		}
	}
	if w.pauses != 2 || w.resumes != 2 || w.published != 1 {
		t.Fatal("snapshot and publication process barriers missing", w)
	}
	stops := w.stops
	if err := f.a.reapSandboxes(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w.stops != stops {
		t.Fatal("recent review sandbox was collected")
	}
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_tasks SET updated_at=now()-interval '31 minutes' WHERE id=$1`,
		f.task.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reapSandboxes(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w.stops != stops+1 {
		t.Fatal("idle sandbox retained indefinitely")
	}
	if err := f.a.reapSandboxes(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w.stops != stops+1 {
		t.Fatal("collected sandbox repeatedly deleted")
	}
}

func TestActiveSandboxIsNotCollectedOnIdleTimeout(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.dispatch(t)
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_tasks SET updated_at=now()-interval '1 hour' WHERE id=$1`,
		f.task.ID,
	); err != nil {
		t.Fatal(err)
	}
	w := f.a.Workspaces.(*testWorkspace)
	before := w.stops
	if err := f.a.reapSandboxes(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w.stops != before {
		t.Fatal("active execution lost compute")
	}
}

func TestCancelRetainedSandboxConfirmsComputeStopped(t *testing.T) {
	f := setupTask(t)
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_tasks SET sandbox_active=true WHERE id=$1`,
		f.task.ID,
	); err != nil {
		t.Fatal(err)
	}
	w := f.a.Workspaces.(*testWorkspace)
	if code, out := f.act(t, "Cancel", nil); code != 200 {
		t.Fatal(code, out)
	}
	f.refresh(t)
	if !f.task.StopConfirmed || w.stops == 0 || f.task.Status != "Canceled" {
		t.Fatal("cancel acknowledged without stopping retained compute", f.task)
	}
	var active bool
	if err := f.a.DB.QueryRow(f.ctx, `SELECT sandbox_active FROM coding_tasks WHERE id=$1`, f.task.ID).
		Scan(&active); err != nil ||
		active {
		t.Fatal(err, active)
	}
}

func TestPendingPublicationKeepsSandboxPaused(t *testing.T) {
	f := setupTask(t)
	if code, out := f.act(t, "Resume", nil); code != 200 {
		t.Fatal(code, out)
	}
	run := f.dispatch(t)
	f.delivering(t, run["id"])
	state := Object{
		"finishRequest":  Object{"summary": "candidate"},
		"artifact":       workspace.Artifact{ExpectedHead: strings.Repeat("b", 40)},
		"deliveryChecks": []Object{{"passed": true}},
	}
	if _, err := f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_runs SET output=$2 WHERE id=$1`,
		run["id"],
		mustJSON(state),
	); err != nil {
		t.Fatal(err)
	}
	w := f.a.Workspaces.(*testWorkspace)
	for i := 0; i < 2; i++ {
		if err := f.a.reconcileDelivery(f.ctx, run["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	if w.pauses != 2 || w.resumes != 0 {
		t.Fatal("pending publication allowed concurrent Git metadata writes", w)
	}
}
