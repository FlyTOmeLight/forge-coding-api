package task

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func TestPublicSDKCallsCodingControllers(t *testing.T) {
	f := setupTask(t)
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.a.Handler().ServeHTTP(w, r) }),
	)
	defer server.Close()
	sdk, err := codingv1.NewClient(server.URL, f.a.Config.ServiceToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	scope := codingv1.ProjectScope{TenantID: "t", ProjectID: "p", Session: f.operator, CSRFToken: "test-csrf"}
	list, err := sdk.ListTasks(ctx, scope)
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != f.task.ID {
		t.Fatal(list, err)
	}
	detail, err := sdk.GetTask(ctx, scope, f.task.ID)
	if err != nil || detail.Task.ID != f.task.ID || !detail.Permissions.Manage {
		t.Fatal(detail, err)
	}
	settings, err := sdk.GetSettings(ctx, scope)
	if err != nil || settings.Settings.AgentID != "coding-agent" {
		t.Fatal(settings, err)
	}
	history, err := sdk.ListReleases(ctx, scope)
	if err != nil || len(history.Items) != 0 {
		t.Fatal(history, err)
	}
	key := Token()
	req := &codingv1.CreateTaskRequest{
		Title:       "SDK task",
		Description: "Fix code",
		Reproduce:   "Run tests",
		Criteria:    "Tests pass",
	}
	task, err := sdk.CreateTask(ctx, scope, key, req)
	if err != nil || task.Control != "Agent" || task.ActiveRun == nil {
		t.Fatal(task, err)
	}
	replay, err := sdk.CreateTask(ctx, scope, key, req)
	if err != nil || replay.ID != task.ID {
		t.Fatal("idempotency lost", replay, err)
	}
	trace, err := sdk.GetRunTrace(ctx, scope, task.ID, *task.ActiveRun, 0)
	if err != nil || trace.Run != nil || len(trace.Events) != 0 {
		t.Fatal(trace, err)
	}
	_, err = sdk.SaveSettings(
		ctx,
		scope,
		&codingv1.SaveSettingsRequest{
			Version:        settings.Settings.Version,
			AgentID:        "other",
			AgentVersion:   1,
			BaseBranch:     "master",
			AllowedPaths:   []string{"src"},
			Checks:         []string{"go test ./..."},
			RepairLimit:    1,
			TimeoutMinutes: 10,
		},
	)
	var failure *serrors.Error
	if !errors.As(err, &failure) || failure.Status != 409 {
		t.Fatal("business failure lost", err)
	}
	_, err = sdk.AdvanceEvaluation(ctx, strings.Repeat("a", 32), &codingv1.EvaluationEnvironmentRequest{})
	if !errors.As(err, &failure) || failure.Code != "INVALID_EVALUATION" || failure.Status != 400 {
		t.Fatal("service credentials not accepted", err)
	}
	scope.ProjectID = "other"
	if _, err := sdk.GetTask(ctx, scope, task.ID); !errors.As(err, &failure) || failure.Status != 404 {
		t.Fatal("project isolation lost", err)
	}
}
