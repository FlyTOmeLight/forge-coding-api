package tenant

import (
	"context"
	"net/http"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Operator bool   `json:"operator"`
}

type Project struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Path     string `json:"path"`
	GitLabID int64  `json:"gitlab_id"`
}

type SessionRequest struct {
	Session string `json:"session"`
	Method  string `json:"method"`
	Origin  string `json:"origin"`
	CSRF    string `json:"csrf"`
}

type Client struct {
	endpoint, token string
	http            *http.Client
}

func New(endpoint, token string, h *http.Client) *Client {
	return &Client{endpoint: endpoint, token: token, http: h}
}

func (c *Client) Session(ctx context.Context, req SessionRequest) (User, error) {
	var user User
	err := client.Request(ctx, c.http, "POST", c.endpoint+"/internal/v1/session/check", c.token, req, &user)
	return user, err
}

func (c *Client) Level(ctx context.Context, uid, tid, pid string) (int, error) {
	var result struct {
		Level int `json:"level"`
	}
	err := client.Request(
		ctx,
		c.http,
		"POST",
		c.endpoint+"/internal/v1/access/check",
		c.token,
		map[string]string{"user_id": uid, "tenant_id": tid, "project_id": pid, "action": "read"},
		&result,
	)
	return result.Level, err
}

func (c *Client) Resolve(ctx context.Context, tid, pid string) (Project, error) {
	var project Project
	err := client.Request(
		ctx,
		c.http,
		"POST",
		c.endpoint+"/internal/v1/projects/resolve",
		c.token,
		map[string]string{"tenant_id": tid, "project_id": pid},
		&project,
	)
	return project, err
}

func (c *Client) Ready(ctx context.Context) bool {
	return client.Request(ctx, c.http, "GET", c.endpoint+"/healthz", c.token, nil, nil) == nil
}
