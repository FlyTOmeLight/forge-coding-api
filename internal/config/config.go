package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/FlyTOmeLight/server-go/conf"
)

type Config struct {
	WorkspaceNamespace, SandboxGatewayURL, WorkspaceJobImage                                                                                                   string
	EnabledProjects                                                                                                                                            map[string]bool
	DatabaseURL, PublicURL, TenantURL, TenantToken, ServiceToken, GitLabURL, GitLabPublicURL, GitLabToken, GitLabTokenFile, ModelURL, RuntimeURL, RuntimeToken string
}

// FromSnapshot reads the immutable business configuration supplied by server-go.
func FromSnapshot(snapshot *conf.Config) (Config, error) {
	var c Config
	if err := snapshot.UnmarshalKey("Coding", &c); err != nil {
		return c, err
	}
	var projects struct{ ProjectsJSON string }
	if err := snapshot.UnmarshalKey("Coding", &projects); err != nil {
		return c, err
	}

	if e := json.Unmarshal(
		[]byte(projects.ProjectsJSON),
		&c.EnabledProjects,
	); e != nil ||
		len(c.EnabledProjects) == 0 {
		return c, errors.New("CODING_PROJECTS_JSON must map enabled GitLab project IDs to true")
	}
	if c.WorkspaceNamespace == "" {
		return c, errors.New("WORKSPACE_NAMESPACE is required")
	}
	{
		if c.WorkspaceJobImage == "" || c.SandboxGatewayURL == "" {
			return c, errors.New(
				"WORKSPACE_JOB_IMAGE and SANDBOX_GATEWAY_URL are required with WORKSPACE_NAMESPACE",
			)
		}
		u, err := url.Parse(c.SandboxGatewayURL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New("invalid SANDBOX_GATEWAY_URL")
		}
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
