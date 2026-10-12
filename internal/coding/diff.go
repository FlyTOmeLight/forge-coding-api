package coding

import (
	"errors"
	"github.com/FlyTOmeLight/forge-coding-api/internal/remote"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type TaskDiff struct {
	MRIID        int64  `json:"mrIid"`
	Base         string `json:"base"`
	Head         string `json:"head"`
	Start        string `json:"start"`
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch"`
	Patch        string `json:"patch"`
}

func (a *App) getTaskDiff(w http.ResponseWriter, r *http.Request, u User) {
	if _, err := a.taskRead(r, u); err != nil {
		fail(w, err)
		return
	}
	t, err := scanTask(a.DB.QueryRow(r.Context(), `SELECT `+taskColumns+` FROM coding_tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, r.PathValue("task"), r.PathValue("tid"), r.PathValue("pid")))
	if errors.Is(err, pgx.ErrNoRows) {
		err = problem(404, "NOT_FOUND", "任务不存在")
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if t.MRIID == 0 {
		JSON(w, 200, TaskDiff{Head: t.Head, SourceBranch: t.Branch, TargetBranch: t.TargetBranch})
		return
	}
	project, err := a.project(r.Context(), t.TenantID, t.ProjectID)
	if err != nil {
		fail(w, err)
		return
	}
	mr, err := a.GitLab.MergeRequest(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		fail(w, problem(502, "GITLAB_DIFF_UNAVAILABLE", "无法读取 GitLab MR 差异，请重试或打开 GitLab MR"))
		return
	}
	if mr.DiffRefs.Head == "" || mr.DiffRefs.Base == "" {
		fail(w, problem(409, "MR_DIFF_PENDING", "GitLab 正在生成差异，请稍后重新读取"))
		return
	}
	patch, err := a.GitLab.RawMergeRequestDiff(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		var dependency *remote.Error
		if errors.As(err, &dependency) && dependency.Code == "RESPONSE_TOO_LARGE" {
			fail(w, problem(502, "GITLAB_DIFF_TOO_LARGE", "MR 差异超过页面读取上限，请打开 GitLab MR 查看"))
			return
		}
		fail(w, problem(502, "GITLAB_DIFF_UNAVAILABLE", "无法读取 GitLab MR 差异，请重试或打开 GitLab MR"))
		return
	}
	// A push or target-branch update must not pair an older patch with newer commit labels.
	latest, err := a.GitLab.MergeRequest(r.Context(), project.GitLabID, t.MRIID)
	if err != nil {
		fail(w, problem(502, "GITLAB_DIFF_UNAVAILABLE", "无法确认 GitLab 差异版本，请重新读取"))
		return
	}
	if latest.DiffRefs != mr.DiffRefs || latest.Source != mr.Source || latest.Target != mr.Target {
		fail(w, problem(409, "MR_DIFF_CHANGED", "读取期间 GitLab 差异版本已变化，请重新读取"))
		return
	}
	JSON(w, 200, TaskDiff{MRIID: t.MRIID, Base: mr.DiffRefs.Base, Head: mr.DiffRefs.Head, Start: mr.DiffRefs.Start, SourceBranch: mr.Source, TargetBranch: mr.Target, Patch: patch})
}
