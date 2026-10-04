package remote

import (
	"context"
	"fmt"
	"net/url"
)

type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

func (g *GitLab) Branch(ctx context.Context, project int64, branch string) (Branch, error) {
	var b Branch
	e := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/repository/branches/%s", project, url.PathEscape(branch)), nil, &b)
	return b, e
}
func (g *GitLab) Branches(ctx context.Context, project int64) ([]Branch, error) {
	all := []Branch{}
	for page := 1; ; page++ {
		var batch []Branch
		e := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/repository/branches?per_page=100&page=%d", project, page), nil, &batch)
		if e != nil {
			return nil, e
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}
