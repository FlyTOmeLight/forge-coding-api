package remote

import (
	"context"
	"net/http"
	"os"
	"strings"
)

type GitLab struct {
	URL, Token, TokenFile string
	HTTP                  *http.Client
}

func (g *GitLab) call(ctx context.Context, method, path string, body, target any) error {
	token, err := g.Credential()
	if err != nil {
		return err
	}
	return request(ctx, g.HTTP, "gitlab", method, g.URL+"/api/v4"+path, body, http.Header{"PRIVATE-TOKEN": []string{token}}, target)
}
func (g *GitLab) Version(ctx context.Context) (map[string]any, error) {
	var v map[string]any
	e := g.call(ctx, "GET", "/version", nil, &v)
	return v, e
}

// Credential reads the projected Secret on each operation. Kubernetes replaces
// its symlink atomically; configured files never fall back to stale env values.
func (g *GitLab) Credential() (string, error) {
	if g.TokenFile == "" {
		return g.Token, nil
	}
	raw, err := os.ReadFile(g.TokenFile)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "", &Error{Service: "gitlab", Code: "CREDENTIAL_UNAVAILABLE"}
	}
	return strings.TrimSpace(string(raw)), nil
}

type GitLabCredential struct {
	ID        int64    `json:"id"`
	UserID    int64    `json:"user_id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	Revoked   bool     `json:"revoked"`
}

func (g *GitLab) CredentialStatus(ctx context.Context) (GitLabCredential, error) {
	var status GitLabCredential
	err := g.call(ctx, "GET", "/personal_access_tokens/self", nil, &status)
	return status, err
}
