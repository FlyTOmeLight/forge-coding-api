package gitlab

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	sdk "gitlab.com/gitlab-org/api/client-go"
)

type GitLab struct {
	client           *sdk.Client
	token, tokenFile string
}

func New(endpoint, token, tokenFile string, h *http.Client) (*GitLab, error) {
	c, err := sdk.NewClient(
		"",
		sdk.WithBaseURL(endpoint),
		sdk.WithHTTPClient(h),
		sdk.WithoutRetries(),
		sdk.WithCustomLogger(nil),
	)
	if err != nil {
		return nil, err
	}
	return &GitLab{client: c, token: token, tokenFile: tokenFile}, nil
}

// Credential reads each Secret revision without changing shared SDK authentication.
func (g *GitLab) Credential() (string, error) {
	if g.tokenFile == "" {
		return g.token, nil
	}
	raw, err := os.ReadFile(g.tokenFile)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "", errors.New("GitLab credential unavailable")
	}
	return strings.TrimSpace(string(raw)), nil
}

func (g *GitLab) options(ctx context.Context) ([]sdk.RequestOptionFunc, error) {
	token, err := g.Credential()
	if err != nil {
		return nil, err
	}
	return []sdk.RequestOptionFunc{sdk.WithContext(ctx), sdk.WithToken(sdk.PrivateToken, token)}, nil
}

func IsStatus(err error, status int) bool {
	if status == http.StatusNotFound && errors.Is(err, sdk.ErrNotFound) {
		return true
	}
	var response *sdk.ErrorResponse
	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == status
}

func (g *GitLab) Version(ctx context.Context) (map[string]any, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return nil, err
	}
	version, _, err := g.client.Version.GetVersion(opts...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": version.Version, "revision": version.Revision}, nil
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
	opts, err := g.options(ctx)
	if err != nil {
		return GitLabCredential{}, err
	}
	token, _, err := g.client.PersonalAccessTokens.GetSinglePersonalAccessToken(opts...)
	if err != nil {
		return GitLabCredential{}, err
	}
	status := GitLabCredential{
		ID:      token.ID,
		UserID:  token.UserID,
		Name:    token.Name,
		Scopes:  token.Scopes,
		Revoked: token.Revoked,
	}
	if token.ExpiresAt != nil {
		status.ExpiresAt = token.ExpiresAt.String()
	}
	return status, nil
}
