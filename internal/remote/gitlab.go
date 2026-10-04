package remote

import (
	"context"
	"net/http"
)

type GitLab struct {
	URL, Token string
	HTTP       *http.Client
}

func (g *GitLab) call(ctx context.Context, method, path string, body, target any) error {
	return request(ctx, g.HTTP, "gitlab", method, g.URL+"/api/v4"+path, body, http.Header{"PRIVATE-TOKEN": []string{g.Token}}, target)
}
func (g *GitLab) Version(ctx context.Context) (map[string]any, error) {
	var v map[string]any
	e := g.call(ctx, "GET", "/version", nil, &v)
	return v, e
}
