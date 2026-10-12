package remote

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// DiffRefs identifies the commits used for a GitLab MR diff.
type DiffRefs struct {
	Base  string `json:"base_sha"`
	Head  string `json:"head_sha"`
	Start string `json:"start_sha"`
}

// MergeRequest records GitLab facts separately from local execution and approval.
type MergeRequest struct {
	IID      int64    `json:"iid"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Head     string   `json:"sha"`
	Source   string   `json:"source_branch"`
	Target   string   `json:"target_branch"`
	URL      string   `json:"web_url"`
	Draft    bool     `json:"draft"`
	DiffRefs DiffRefs `json:"diff_refs"`
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

// GitLab's REST API changes draft status through the title prefix.
var draftPrefix = regexp.MustCompile(`(?i)^(?:(?:draft|wip):\s*|\[(?:draft|wip)\]\s*|\((?:draft|wip)\)\s*)+`)

func (g *GitLab) SetMergeRequestDraft(ctx context.Context, project int64, mr MergeRequest, draft bool) (MergeRequest, error) {
	title := strings.TrimSpace(draftPrefix.ReplaceAllString(mr.Title, ""))
	if draft {
		title = "Draft: " + title
	}
	var updated MergeRequest
	err := g.call(ctx, "PUT", fmt.Sprintf("/projects/%d/merge_requests/%d", project, mr.IID), map[string]any{"title": title}, &updated)
	return updated, err
}

// RawMergeRequestDiff retains GitLab's complete unified patch, including file headers.
func (g *GitLab) RawMergeRequestDiff(ctx context.Context, project, iid int64) (string, error) {
	var patch string
	err := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/merge_requests/%d/raw_diffs", project, iid), nil, &patch)
	return patch, err
}

func (g *GitLab) SourceMergeRequests(ctx context.Context, project int64, branch string) ([]MergeRequest, error) {
	var result []MergeRequest
	err := g.call(ctx, "GET", fmt.Sprintf("/projects/%d/merge_requests?state=opened&source_branch=%s", project, url.QueryEscape(branch)), nil, &result)
	return result, err
}
func (g *GitLab) CreateDraftMergeRequest(ctx context.Context, project int64, source, target, title, summary string) (MergeRequest, error) {
	var result MergeRequest
	err := g.call(ctx, "POST", fmt.Sprintf("/projects/%d/merge_requests", project), map[string]any{"source_branch": source, "target_branch": target, "title": "Draft: " + title, "description": summary}, &result)
	return result, err
}
