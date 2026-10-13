package task

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodingSubmitsPublishedAgentReference(t *testing.T) {
	f := setupTask(t)
	c, err := f.a.readyTaskSettings(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.a.executionSnapshot(f.ctx, f.task, "round", "fix the issue", c)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Spec map[string]any `json:"spec"`
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.Spec["agentID"] != "coding-agent" || request.Spec["agentVersion"] != float64(1) {
		t.Fatalf("missing published identity: %s", raw)
	}
	for _, field := range []string{"systemPrompt", "model", "tools", "maxTurns"} {
		if _, ok := request.Spec[field]; ok {
			t.Fatalf("application supplied Agent definition: %s", field)
		}
	}
}

func TestAgentBindingRequiresThisProjectsWorkspace(t *testing.T) {
	f := setupTask(t)
	c, err := f.a.projectService().Settings(f.ctx, "t", "p")
	if err != nil {
		t.Fatal(err)
	}
	f.a.Config.EnabledProjects["77"] = false
	if err = f.a.projectService().CheckAgentBinding(f.ctx, "t", "p", &c); err == nil {
		t.Fatal("foreign workspace accepted")
	}
	c.AgentID = ""
	if err = c.Validate(); err == nil {
		t.Fatal("unbound configuration accepted")
	}
}

func TestExistingTaskKeepsItsFrozenAgentAfterProjectActivation(t *testing.T) {
	f := setupTask(t)
	original, err := f.a.readyTaskSettings(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(original)
	_, err = f.a.DB.Exec(
		f.ctx,
		`INSERT INTO coding_runs(id,task_id,epoch,head,status,requirements,config_snapshot,execution_snapshot) VALUES('prior',$1,1,$2,'Stopped','old',$3,'{}')`,
		f.task.ID,
		f.task.Head,
		raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.a.DB.Exec(
		f.ctx,
		`UPDATE coding_project_settings SET settings=jsonb_set(settings,'{agentID}','"new-agent"'),checked_at=NULL WHERE project_id='p'`,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := f.a.readyTaskSettings(f.ctx, f.task)
	if err != nil || frozen.AgentID != original.AgentID || frozen.AgentVersion != original.AgentVersion {
		t.Fatalf("existing task changed: %+v %v", frozen, err)
	}
	snapshot, err := f.a.executionSnapshot(f.ctx, f.task, "next", "continue", frozen)
	if err != nil || !strings.Contains(string(snapshot), `"agentID":"coding-agent"`) {
		t.Fatalf("lost frozen execution: %s %v", snapshot, err)
	}
}

func TestExecutionSnapshotIncludesReviewContext(t *testing.T) {
	f := setupTask(t)
	f.task.Discussions = json.RawMessage(
		`[{"id":"review","notes":[{"id":26,"body":"Add comments to each function","author":{"username":"reviewer"},"position":{"head_sha":"reviewed-head","new_path":"src/calc.py","new_line":1}},{"id":27,"body":"left review comments","system":true}]}]`,
	)
	c, err := f.a.readyTaskSettings(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.a.executionSnapshot(f.ctx, f.task, "round", "Follow the review comments", c)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Spec struct {
			Query string `json:"query"`
		} `json:"spec"`
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Follow the review comments", "Add comments to each function", "src/calc.py", "reviewed-head", f.task.Criteria} {
		if !strings.Contains(request.Spec.Query, text) {
			t.Fatalf("execution lost review context %q: %s", text, request.Spec.Query)
		}
	}
	if strings.Contains(request.Spec.Query, "left review comments") {
		t.Fatal("system note included as review feedback")
	}
}
