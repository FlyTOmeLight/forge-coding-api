package task

import (
	"errors"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type TaskDiff = codingv1.TaskDiff

func (a *Service) getTaskDiff(gc *core.Context, u access.User) (TaskDiff, error) {
	r := gc.Request
	if _, err := a.Permissions.Read(r, u); err != nil {
		return TaskDiff{}, err
	}
	t, err := table.FindScopedTask(r.Context(), a.DB, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid"))
	if errors.Is(err, pgx.ErrNoRows) {
		err = serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	if err != nil {
		return TaskDiff{}, err
	}
	if t.MRIID == 0 {
		return TaskDiff{Head: t.Head, SourceBranch: t.Branch, TargetBranch: t.TargetBranch}, nil
	}
	project, err := a.Permissions.Resolve(r.Context(), t.TenantID, t.ProjectID)
	if err != nil {
		return TaskDiff{}, err
	}
	mr, err := a.GitLab.MergeRequest(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		return TaskDiff{}, serrors.NewError(
			502,
			"GITLAB_DIFF_UNAVAILABLE",
			"无法读取 GitLab MR 差异，请重试或打开 GitLab MR",
		)
	}
	if mr.DiffRefs.Head == "" || mr.DiffRefs.Base == "" {
		return TaskDiff{}, serrors.NewError(409, "MR_DIFF_PENDING", "GitLab 正在生成差异，请稍后重新读取")
	}
	patch, err := a.GitLab.RawMergeRequestDiff(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		if errors.Is(err, gitlab.ErrDiffTooLarge) {
			return TaskDiff{}, serrors.NewError(
				502,
				"GITLAB_DIFF_TOO_LARGE",
				"MR 差异超过页面读取上限，请打开 GitLab MR 查看",
			)
		}
		return TaskDiff{}, serrors.NewError(
			502,
			"GITLAB_DIFF_UNAVAILABLE",
			"无法读取 GitLab MR 差异，请重试或打开 GitLab MR",
		)
	}
	// A push or target-branch update must not pair an older patch with newer commit labels.
	latest, err := a.GitLab.MergeRequest(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		return TaskDiff{}, serrors.NewError(502, "GITLAB_DIFF_UNAVAILABLE", "无法确认 GitLab 差异版本，请重新读取")
	}
	if latest.DiffRefs != mr.DiffRefs || latest.Source != mr.Source || latest.Target != mr.Target {
		return TaskDiff{}, serrors.NewError(409, "MR_DIFF_CHANGED", "读取期间 GitLab 差异版本已变化，请重新读取")
	}
	return TaskDiff{
		MRIID:        t.MRIID,
		Base:         mr.DiffRefs.Base,
		Head:         mr.DiffRefs.Head,
		Start:        mr.DiffRefs.Start,
		SourceBranch: mr.Source,
		TargetBranch: mr.Target,
		Patch:        patch,
	}, nil
}
