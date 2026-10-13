package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
)

func TestServerConfigSnapshot(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/coding",
		"PUBLIC_URL":   "http://localhost:8080",
		"TENANT_URL":   "http://tenant/",
		"TENANT_TOKEN": strings.Repeat("t", 32),
		"SERVICE_TOKEN": strings.Repeat(
			"s",
			32,
		),
		"RUNTIME_URL":          "http://runtime/",
		"RUNTIME_TOKEN":        strings.Repeat("r", 32),
		"CODING_PROJECTS_JSON": `{"77":true}`,
		"WORKSPACE_NAMESPACE":  "coding",
		"WORKSPACE_JOB_IMAGE":  "coding:test",
		"SANDBOX_GATEWAY_URL":  "http://coding",
		"GITLAB_URL":           "http://gitlab/",
		"GITLAB_PUBLIC_URL":    "",
		"GITLAB_TOKEN":         "test-gitlab",
		"GITLAB_TOKEN_FILE":    "",
		"MODEL_URL":            "http://model",
		"LISTEN_ADDR":          "127.0.0.1:0",
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	manager, err := conf.NewManager(filepath.Join("..", "..", "conf", "server", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := FromSnapshot(manager.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnabledProjects["77"] || cfg.TenantURL != "http://tenant" || cfg.RuntimeURL != "http://runtime" ||
		cfg.GitLabPublicURL != "http://gitlab/" ||
		manager.Snapshot().HTTPServer.Listen != "127.0.0.1:0" {
		t.Fatalf("unexpected snapshot: %+v", cfg)
	}
	t.Setenv("TENANT_TOKEN", "short")
	snapshot, err := conf.Load(filepath.Join("..", "..", "conf", "server", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromSnapshot(snapshot); err == nil {
		t.Fatal("invalid service credential accepted")
	}
}
