package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "gitlab.com/gitlab-org/api/client-go"
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

func mergeRequestValue(m *sdk.MergeRequest) MergeRequest {
	value := MergeRequest{
		IID:    m.IID,
		Title:  m.Title,
		State:  m.State,
		Head:   m.SHA,
		Source: m.SourceBranch,
		Target: m.TargetBranch,
		URL:    m.WebURL,
		Draft:  m.Draft,
	}
	value.DiffRefs = DiffRefs{Base: m.DiffRefs.BaseSha, Head: m.DiffRefs.HeadSha, Start: m.DiffRefs.StartSha}
	return value
}

func (g *GitLab) MergeRequest(ctx context.Context, project, iid int64) (MergeRequest, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return MergeRequest{}, err
	}
	mr, _, err := g.client.MergeRequests.GetMergeRequest(project, iid, nil, opts...)
	if err != nil {
		return MergeRequest{}, err
	}
	return mergeRequestValue(mr), nil
}

func (g *GitLab) MRDiscussions(ctx context.Context, project, iid int64) ([]map[string]any, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return nil, err
	}
	all := []map[string]any{}
	list := &sdk.ListMergeRequestDiscussionsOptions{ListOptions: sdk.ListOptions{PerPage: 100, Page: 1}}
	for {
		batch, response, err := g.client.Discussions.ListMergeRequestDiscussions(project, iid, list, opts...)
		if err != nil {
			return nil, err
		}
		for _, item := range batch {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, err
			}
			var value map[string]any
			if err = json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			all = append(all, value)
		}
		if response.NextPage == 0 {
			return all, nil
		}
		list.Page = response.NextPage
	}
}

var draftPrefix = regexp.MustCompile(`(?i)^(?:(?:draft|wip):\s*|\[(?:draft|wip)\]\s*|\((?:draft|wip)\)\s*)+`)

func (g *GitLab) SetMergeRequestDraft(
	ctx context.Context,
	project int64,
	mr MergeRequest,
	draft bool,
) (MergeRequest, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return MergeRequest{}, err
	}
	title := strings.TrimSpace(draftPrefix.ReplaceAllString(mr.Title, ""))
	if draft {
		title = "Draft: " + title
	}
	updated, _, err := g.client.MergeRequests.UpdateMergeRequest(
		project,
		mr.IID,
		&sdk.UpdateMergeRequestOptions{Title: &title},
		opts...)
	if err != nil {
		return MergeRequest{}, err
	}
	return mergeRequestValue(updated), nil
}

var ErrDiffTooLarge = errors.New("GitLab diff exceeds 4 MiB")

type diffBuffer struct{ buffer bytes.Buffer }

func (b *diffBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 4<<20 {
		return 0, ErrDiffTooLarge
	}
	return b.buffer.Write(p)
}

// The SDK's byte-slice method buffers an unbounded body. Use its request and
// response machinery with a bounded writer for the existing 4 MiB diff contract.
func (g *GitLab) RawMergeRequestDiff(ctx context.Context, project, iid int64) (string, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return "", err
	}
	req, err := g.client.NewRequest(
		http.MethodGet,
		fmt.Sprintf("projects/%d/merge_requests/%d/raw_diffs", project, iid),
		nil,
		opts,
	)
	if err != nil {
		return "", err
	}
	var patch diffBuffer
	if _, err = g.client.Do(req, &patch); err != nil {
		return "", err
	}
	return patch.buffer.String(), nil
}

func (g *GitLab) SourceMergeRequests(ctx context.Context, project int64, branch string) ([]MergeRequest, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return nil, err
	}
	state := "opened"
	batch, _, err := g.client.MergeRequests.ListProjectMergeRequests(
		project,
		&sdk.ListProjectMergeRequestsOptions{State: &state, SourceBranch: &branch},
		opts...)
	if err != nil {
		return nil, err
	}
	result := []MergeRequest{}
	for _, mr := range batch {
		result = append(
			result,
			MergeRequest{
				IID:    mr.IID,
				Title:  mr.Title,
				State:  mr.State,
				Head:   mr.SHA,
				Source: mr.SourceBranch,
				Target: mr.TargetBranch,
				URL:    mr.WebURL,
				Draft:  mr.Draft,
			},
		)
	}
	return result, nil
}

func (g *GitLab) CreateDraftMergeRequest(
	ctx context.Context,
	project int64,
	source, target, title, summary string,
) (MergeRequest, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return MergeRequest{}, err
	}
	title = "Draft: " + title
	mr, _, err := g.client.MergeRequests.CreateMergeRequest(
		project,
		&sdk.CreateMergeRequestOptions{
			SourceBranch: &source,
			TargetBranch: &target,
			Title:        &title,
			Description:  &summary,
		},
		opts...)
	if err != nil {
		return MergeRequest{}, err
	}
	return mergeRequestValue(mr), nil
}
