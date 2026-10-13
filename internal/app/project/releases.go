package project

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
	"github.com/FlyTOmeLight/forge-coding-api/internal/codingid"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func releaseFromRow(row table.ProjectRelease, err error) (codingv1.Release, error) {
	if err != nil {
		return codingv1.Release{}, err
	}
	out := codingv1.Release{
		ID:                 row.ID,
		ActorID:            row.ActorID,
		ActorName:          row.ActorName,
		Action:             row.Action,
		RestoredFrom:       row.RestoredFrom,
		SourceAgentID:      row.SourceAgentID,
		SourceAgentVersion: row.SourceAgentVersion,
		ExperimentID:       row.ExperimentID,
		SuiteDigest:        row.SuiteDigest,
		CreatedAt:          row.CreatedAt,
	}
	err = json.Unmarshal(row.Settings, &out.Settings)
	return out, err
}

func insertRelease(
	ctx context.Context,
	tx pgx.Tx,
	tid, pid, key, hash string,
	item codingv1.Release,
) (codingv1.Release, error) {
	raw, err := json.Marshal(item.Settings)
	if err != nil {
		return codingv1.Release{}, err
	}
	row, err := table.InsertProjectRelease(
		ctx,
		tx,
		tid,
		pid,
		key,
		hash,
		table.ProjectRelease{
			ID:                 item.ID,
			ActorID:            item.ActorID,
			ActorName:          item.ActorName,
			Action:             item.Action,
			RestoredFrom:       item.RestoredFrom,
			SourceAgentID:      item.SourceAgentID,
			SourceAgentVersion: item.SourceAgentVersion,
			ExperimentID:       item.ExperimentID,
			SuiteDigest:        item.SuiteDigest,
			Settings:           raw,
		},
	)
	return releaseFromRow(row, err)
}

func (a *Service) ListReleases(gc *core.Context, u access.User) (codingv1.ReleaseList, error) {
	level, err := a.Permissions.Read(gc.Request, u)
	if err != nil {
		return codingv1.ReleaseList{}, err
	}
	rows, err := table.ListProjectReleases(
		gc.Request.Context(),
		a.DB,
		gc.Request.PathValue("tid"),
		gc.Request.PathValue("pid"),
	)
	if err != nil {
		return codingv1.ReleaseList{}, err
	}
	out := codingv1.ReleaseList{Items: []codingv1.Release{}, CanEnable: level >= 40}
	for _, row := range rows {
		item, err := releaseFromRow(row, nil)
		if err != nil {
			return codingv1.ReleaseList{}, err
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (a *Service) ChangeRelease(
	gc *core.Context,
	u access.User,
	req codingv1.EnableReleaseRequest,
	restore bool,
) (codingv1.Release, bool, error) {
	r := gc.Request
	level, err := a.Permissions.Read(r, u)
	if err != nil {
		return codingv1.Release{}, false, err
	}
	if level < 40 {
		return codingv1.Release{}, false, serrors.NewError(403, "PROJECT_ADMIN_REQUIRED", "启用或回退需要项目管理员权限")
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 100 {
		return codingv1.Release{}, false, serrors.NewError(400, "IDEMPOTENCY_REQUIRED", "启用或回退需要唯一请求标识")
	}
	raw, _ := json.Marshal(req)
	hash := codingid.Hash(string(raw) + ":" + u.ID + ":" + r.PathValue("release"))
	tid, pid := r.PathValue("tid"), r.PathValue("pid")
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return codingv1.Release{}, false, err
	}
	defer tx.Rollback(r.Context())
	if err = table.LockProjectSettings(r.Context(), tx, pid); err != nil {
		return codingv1.Release{}, false, err
	}
	previousHash, err := table.ProjectReleaseRequestHash(r.Context(), tx, tid, pid, key)
	if err == nil {
		if previousHash != hash {
			return codingv1.Release{}, false, serrors.NewError(409, "IDEMPOTENCY_CONFLICT", "请求标识已用于其他内容")
		}
		row, err := table.ProjectReleaseByRequest(r.Context(), tx, tid, pid, key)
		previous, err := releaseFromRow(row, err)
		if err != nil {
			return codingv1.Release{}, false, err
		}
		return previous, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return codingv1.Release{}, false, err
	}
	settingsRow, err := table.GetProjectSettings(r.Context(), tx, tid, pid, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return codingv1.Release{}, false, serrors.NewError(409, "PROJECT_NOT_READY", "请先配置项目基准分支和执行参数")
	}
	if err != nil {
		return codingv1.Release{}, false, err
	}
	current, err := decodeSettings(settingsRow)
	if err != nil {
		return codingv1.Release{}, false, err
	}
	if current.Version != req.SettingsVersion {
		return codingv1.Release{}, false, serrors.NewError(412, "STALE_CONFIG", "项目生效配置已变化，请刷新后核对")
	}
	next := current
	release := codingv1.Release{
		ID:                 codingid.ID("release"),
		ActorID:            u.ID,
		ActorName:          u.Name,
		Action:             "Enable",
		SourceAgentID:      req.AgentID,
		SourceAgentVersion: req.AgentVersion,
		ExperimentID:       req.ExperimentID,
	}
	if restore {
		row, err := table.ProjectReleaseByID(r.Context(), tx, tid, pid, r.PathValue("release"))
		target, err := releaseFromRow(row, err)
		if errors.Is(err, pgx.ErrNoRows) {
			return codingv1.Release{}, false, serrors.NewError(404, "NOT_FOUND", "此项目中没有该启用记录")
		}
		if err != nil {
			return codingv1.Release{}, false, err
		}
		next = Settings(target.Settings)
		release.Action, release.RestoredFrom, release.SourceAgentID, release.SourceAgentVersion, release.ExperimentID, release.SuiteDigest = "Restore", target.ID, target.SourceAgentID, target.SourceAgentVersion, target.ExperimentID, target.SuiteDigest
	} else {
		approval, err := a.Runtime.ReleaseApproval(r.Context(), agentic.ReleaseApprovalRequest{
			TenantID:     tid,
			ProjectID:    pid,
			ActorID:      u.ID,
			ExperimentID: req.ExperimentID,
			AgentID:      req.AgentID,
			AgentVersion: req.AgentVersion,
		})
		if err != nil {
			return codingv1.Release{}, false, err
		}
		if approval.AgentID != req.AgentID || approval.AgentVersion != req.AgentVersion ||
			approval.ExperimentID != req.ExperimentID ||
			approval.SuiteDigest == "" ||
			approval.RuntimeAgentID == "" ||
			approval.RuntimeVersion < 1 {
			return codingv1.Release{}, false, serrors.NewError(502, "INVALID_APPROVAL", "评测审批结果不完整")
		}
		project, err := a.ProjectID(r.Context(), tid, pid)
		if err != nil {
			return codingv1.Release{}, false, err
		}
		branch, err := a.GitLab.Branch(r.Context(), project, current.BaseBranch)
		if err != nil {
			return codingv1.Release{}, false, err
		}
		if branch.Commit.ID != approval.Scenario.BaseHead {
			return codingv1.Release{}, false, serrors.NewError(
				409,
				"EVALUATION_BASE_CHANGED",
				"项目基准分支已变化，请用当前提交重新评测",
			)
		}
		next.AgentID, next.AgentVersion, next.Model, next.AllowedPaths, next.Checks = approval.RuntimeAgentID, approval.RuntimeVersion, approval.Model, approval.Scenario.AllowedPaths, approval.Scenario.Checks
		release.SuiteDigest = approval.SuiteDigest
	}
	if err := next.Validate(); err != nil {
		return codingv1.Release{}, false, err
	}
	if err := a.CheckAgentBinding(r.Context(), tid, pid, &next); err != nil {
		return codingv1.Release{}, false, err
	}
	if restore {
		project, err := a.ProjectID(r.Context(), tid, pid)
		if err != nil {
			return codingv1.Release{}, false, err
		}
		if _, err = a.GitLab.Branch(r.Context(), project, next.BaseBranch); err != nil {
			return codingv1.Release{}, false, err
		}
	}
	if current.ReleaseID == "" && current.AgentID != "" && current.AgentVersion > 0 {
		initial := codingv1.Release{
			ID:        codingid.ID("release"),
			ActorID:   u.ID,
			ActorName: u.Name,
			Action:    "Baseline",
			Settings:  codingv1.Settings(current),
		}
		initial.Settings.ReleaseID = initial.ID
		if _, err := insertRelease(r.Context(), tx, tid, pid, "baseline:"+initial.ID, "", initial); err != nil {
			return codingv1.Release{}, false, err
		}
	}
	now := time.Now().UTC()
	next.ReleaseID, next.Version, next.CheckedAt = release.ID, current.Version+1, &now
	release.Settings = codingv1.Settings(next)
	release, err = insertRelease(r.Context(), tx, tid, pid, key, hash, release)
	if err != nil {
		return codingv1.Release{}, false, err
	}
	nextRaw, _ := json.Marshal(next)
	if err = table.UpdateReleasedProjectSettings(r.Context(), tx, tid, pid, nextRaw, now); err != nil {
		return codingv1.Release{}, false, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return codingv1.Release{}, false, err
	}
	return release, true, nil
}

func sameReleaseConfiguration(a, b Settings) bool {
	return a.AgentID == b.AgentID && a.AgentVersion == b.AgentVersion && a.BaseBranch == b.BaseBranch &&
		reflect.DeepEqual(a.AllowedPaths, b.AllowedPaths) &&
		reflect.DeepEqual(a.Checks, b.Checks)
}
