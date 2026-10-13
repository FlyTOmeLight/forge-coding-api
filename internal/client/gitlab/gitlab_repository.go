package gitlab

import (
	"context"

	sdk "gitlab.com/gitlab-org/api/client-go"
)

type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

func branchValue(b *sdk.Branch) Branch {
	value := Branch{Name: b.Name, Protected: b.Protected}
	if b.Commit != nil {
		value.Commit.ID = b.Commit.ID
	}
	return value
}

func (g *GitLab) Branch(ctx context.Context, project int64, branch string) (Branch, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return Branch{}, err
	}
	b, _, err := g.client.Branches.GetBranch(project, branch, opts...)
	if err != nil {
		return Branch{}, err
	}
	return branchValue(b), nil
}

func (g *GitLab) Branches(ctx context.Context, project int64) ([]Branch, error) {
	opts, err := g.options(ctx)
	if err != nil {
		return nil, err
	}
	all := []Branch{}
	list := &sdk.ListBranchesOptions{ListOptions: sdk.ListOptions{PerPage: 100, Page: 1}}
	for {
		batch, response, err := g.client.Branches.ListBranches(project, list, opts...)
		if err != nil {
			return nil, err
		}
		for _, branch := range batch {
			all = append(all, branchValue(branch))
		}
		if response.NextPage == 0 {
			return all, nil
		}
		list.Page = response.NextPage
	}
}
