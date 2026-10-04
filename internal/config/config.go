package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	ToolProjects                                                                                                                                      map[string]string
	DatabaseURL, PublicURL, TenantURL, TenantToken, ServiceToken, GitLabURL, GitLabPublicURL, GitLabToken, ModelURL, RuntimeURL, RuntimeToken, Listen string
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), PublicURL: os.Getenv("PUBLIC_URL"), TenantURL: os.Getenv("TENANT_URL"), TenantToken: os.Getenv("TENANT_TOKEN"), ServiceToken: os.Getenv("SERVICE_TOKEN"), GitLabURL: os.Getenv("GITLAB_URL"), GitLabPublicURL: os.Getenv("GITLAB_PUBLIC_URL"), GitLabToken: os.Getenv("GITLAB_TOKEN"), ModelURL: os.Getenv("MODEL_URL"), RuntimeURL: os.Getenv("RUNTIME_URL"), RuntimeToken: os.Getenv("RUNTIME_TOKEN"), Listen: os.Getenv("LISTEN_ADDR")}
	if e := json.Unmarshal([]byte(os.Getenv("TOOLS_PROJECTS_JSON")), &c.ToolProjects); e != nil || len(c.ToolProjects) == 0 {
		return c, errors.New("TOOLS_PROJECTS_JSON must map GitLab project IDs to tool service URLs")
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.GitLabPublicURL == "" {
		c.GitLabPublicURL = c.GitLabURL
	}
	for name, value := range map[string]string{"DATABASE_URL": c.DatabaseURL, "TENANT_TOKEN": c.TenantToken, "SERVICE_TOKEN": c.ServiceToken, "RUNTIME_TOKEN": c.RuntimeToken, "GITLAB_TOKEN": c.GitLabToken} {
		if value == "" {
			return c, fmt.Errorf("missing %s", name)
		}
	}
	for name, value := range map[string]string{"PUBLIC_URL": c.PublicURL, "TENANT_URL": c.TenantURL, "GITLAB_URL": c.GitLabURL, "MODEL_URL": c.ModelURL, "RUNTIME_URL": c.RuntimeURL} {
		u, e := url.Parse(value)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("invalid %s", name)
		}
	}
	if len(c.ServiceToken) < 32 || len(c.TenantToken) < 32 || len(c.RuntimeToken) < 32 {
		return c, errors.New("service tokens must be at least 32 characters")
	}
	c.TenantURL = strings.TrimRight(c.TenantURL, "/")
	c.RuntimeURL = strings.TrimRight(c.RuntimeURL, "/")
	c.GitLabURL = strings.TrimRight(c.GitLabURL, "/")
	return c, nil
}
