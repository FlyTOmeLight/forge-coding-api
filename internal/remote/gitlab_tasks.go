package remote

import (
	"context"
	"fmt"
)

// MergeRequest records GitLab facts separately from local execution and approval.
type MergeRequest struct {
	IID    int64  `json:"iid"`
	State  string `json:"state"`
	Head   string `json:"sha"`
	Source string `json:"source_branch"`
	Target string `json:"target_branch"`
	URL    string `json:"web_url"`
	Draft  bool   `json:"draft"`
}

func (g *GitLab) MergeRequest(ctx context.Context, project, iid int64) (MergeRequest, error) {
	var mr MergeRequest
	e := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/merge_requests/%d", project, iid), nil, &mr)
	return mr, e
}

// MRDiscussions retains the complete position, commit and discussion context.
func (g *GitLab) MRDiscussions(ctx context.Context, project, iid int64) ([]map[string]any, error) {
	all := []map[string]any{}
	for page := 1; ; page++ {
		var batch []map[string]any
		e := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/merge_requests/%d/discussions?per_page=100&page=%d", project, iid, page), nil, &batch)
		if e != nil {
			return nil, e
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}
