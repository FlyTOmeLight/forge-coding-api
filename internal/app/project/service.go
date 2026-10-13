package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type Settings codingv1.Settings

type Service struct {
	Config      config.Config
	DB          *pgxpool.Pool
	HTTP        *http.Client
	GitLab      *gitlab.GitLab
	Permissions *access.Service
	Runtime     *agentic.Client
}

func New(cfg config.Config, db *pgxpool.Pool, h *http.Client, g *gitlab.GitLab, permissions *access.Service) *Service {
	return &Service{
		Config:      cfg,
		DB:          db,
		HTTP:        h,
		GitLab:      g,
		Permissions: permissions,
		Runtime:     agentic.New(cfg.RuntimeURL, cfg.RuntimeToken, h),
	}
}

func (a *Service) Settings(ctx context.Context, tid, pid string) (Settings, error) {
	row, err := table.GetProjectSettings(ctx, a.DB, tid, pid, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{
			Version:        0,
			RepairLimit:    3,
			TimeoutMinutes: 30,
			AllowedPaths:   []string{},
			Checks:         []string{},
		}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return decodeSettings(row)
}

func decodeSettings(row table.ProjectSettings) (Settings, error) {
	var out Settings
	err := json.Unmarshal(row.Settings, &out)
	out.Version, out.CheckedAt = row.Version, row.CheckedAt
	return out, err
}

func (a *Service) ProjectID(ctx context.Context, tid, pid string) (int64, error) {
	p, err := a.Permissions.Resolve(ctx, tid, pid)
	return p.GitLabID, err
}

func (a *Service) RequireExecutor(ctx context.Context, tid, pid string) error {
	id, err := a.ProjectID(ctx, tid, pid)
	if err != nil {
		return err
	}
	if !a.Config.EnabledProjects[strconv.FormatInt(id, 10)] {
		return serrors.NewError(409, "PROJECT_EXECUTOR_UNAVAILABLE", "此项目尚未启用 Coding 执行")
	}
	return nil
}

func (a *Service) Repository(ctx context.Context, tid, pid, head, branch string) (workspace.Repository, error) {
	p, err := a.Permissions.Resolve(ctx, tid, pid)
	if err != nil {
		return workspace.Repository{}, err
	}
	if !a.Config.EnabledProjects[strconv.FormatInt(p.GitLabID, 10)] {
		return workspace.Repository{}, serrors.NewError(
			409,
			"PROJECT_EXECUTOR_UNAVAILABLE",
			"Project is not enabled for Coding execution",
		)
	}
	token, err := a.GitLab.Credential()
	if err != nil {
		return workspace.Repository{}, err
	}
	return workspace.Repository{
		URL:         strings.TrimRight(a.Config.GitLabURL, "/") + "/" + p.Path + ".git",
		Head:        head,
		Branch:      branch,
		Token:       token,
		PermitToken: a.Config.ServiceToken,
	}, nil
}

func (c Settings) Validate() error {
	if strings.TrimSpace(c.AgentID) == "" || c.AgentVersion < 1 {
		return serrors.NewError(422, "AGENT_NOT_ENABLED", "请从通过评测的 Coding 报告为项目启用候选")
	}
	return c.ValidateParameters()
}

func (c Settings) ValidateParameters() error {
	if strings.TrimSpace(c.BaseBranch) == "" || len(c.AllowedPaths) == 0 || len(c.AllowedPaths) > 20 ||
		len(c.Checks) == 0 ||
		len(c.Checks) > 10 ||
		c.RepairLimit < 1 ||
		c.RepairLimit > 3 ||
		c.TimeoutMinutes < 1 ||
		c.TimeoutMinutes > 30 {
		return serrors.NewError(
			422,
			"INVALID_EXECUTION_SETTINGS",
			"请选择已发布 Agent 及版本，并设置基准分支、修改范围和必过检查；修复次数为 1–3，执行时间为 1–30 分钟",
		)
	}
	for _, p := range c.AllowedPaths {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) == "." {
			return serrors.NewError(422, "INVALID_PATH", "修改范围必须是仓库内的相对文件或目录")
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return serrors.NewError(422, "INVALID_PATH", "修改范围不能包含上级目录")
			}
		}
	}
	for _, cmd := range c.Checks {
		if strings.TrimSpace(cmd) == "" || len(cmd) > 500 || strings.ContainsAny(cmd, "\r\n") {
			return serrors.NewError(422, "INVALID_CHECK", "每行填写一条检查命令")
		}
	}
	return nil
}

func (a *Service) GetSettings(gc *core.Context, u access.User) (map[string]any, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return nil, e
	}
	c, e := a.Settings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return nil, e
	}
	id, e := a.ProjectID(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return nil, e
	}
	branches, e := a.GitLab.Branches(r.Context(), id)
	if e != nil {
		return nil, e
	}
	runtime := map[string]any{"available": a.Runtime.Ready(r.Context()), "service": "agentic-kit"}
	agents := []agentic.AgentChoice{}
	if level >= 40 && c.AgentID != "" {
		agent, err := a.Runtime.Agent(r.Context(), c.AgentID)
		e = err
		agents = append(agents, agent)
		if e != nil {
			return nil, e
		}
	}
	return map[string]any{
		"settings": c,
		"branches": branches,
		"runtime":  runtime,
		"agents":   agents,
		"permissions": map[string]any{
			"readCode":           level >= 20,
			"createTask":         level >= 30,
			"configureExecution": level >= 40,
		},
	}, nil
}

func (a *Service) SaveSettings(gc *core.Context, u access.User, c Settings) (Settings, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return Settings{}, e
	}
	if level < 40 {
		return Settings{}, serrors.NewError(403, "PROJECT_ADMIN_REQUIRED", "需要项目管理员权限")
	}
	current, err := a.Settings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if err != nil {
		return Settings{}, err
	}
	if current.Version == 0 && (c.AgentID != "" || c.AgentVersion != 0) {
		return Settings{}, serrors.NewError(409, "EVALUATED_RELEASE_REQUIRED", "请先保存执行参数，再从通过的评测报告启用候选")
	}
	if e = c.ValidateParameters(); e != nil {
		return Settings{}, e
	}
	if c.AgentID != "" {
		if e = a.CheckAgentBinding(r.Context(), r.PathValue("tid"), r.PathValue("pid"), &c); e != nil {
			return Settings{}, e
		}
	} else {
		c.Model = ""
	}
	if current.Version > 0 && (c.AgentID != current.AgentID || c.AgentVersion != current.AgentVersion) {
		return Settings{}, serrors.NewError(409, "EVALUATED_RELEASE_REQUIRED", "请通过评测报告启用候选，或从项目启用记录回退")
	}
	if current.ReleaseID != "" {
		if !sameReleaseConfiguration(current, c) || (c.ReleaseID != "" && c.ReleaseID != current.ReleaseID) {
			return Settings{}, serrors.NewError(
				409,
				"EVALUATED_RELEASE_REQUIRED",
				"当前绑定来自评测启用。修改 Agent、基准分支、范围或检查须重新评测并启用",
			)
		}
		c.ReleaseID = current.ReleaseID
	} else if c.ReleaseID != "" {
		return Settings{}, serrors.NewError(422, "INVALID_RELEASE", "请通过评测报告启用版本")
	}
	c.CheckedAt = nil
	raw, _ := json.Marshal(c)
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return Settings{}, e
	}
	defer tx.Rollback(r.Context())
	e = table.LockProjectSettings(r.Context(), tx, r.PathValue("pid"))
	if e != nil {
		return Settings{}, e
	}
	row, err := table.GetProjectSettings(r.Context(), tx, r.PathValue("tid"), r.PathValue("pid"), true)
	e = err
	version := row.Version
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Settings{}, e
	}
	if version != c.Version {
		return Settings{}, serrors.NewError(412, "STALE_CONFIG", "项目配置已变化，请刷新后保存")
	}
	e = table.SaveProjectSettings(r.Context(), tx, r.PathValue("tid"), r.PathValue("pid"), raw)
	if e != nil {
		return Settings{}, e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return Settings{}, e
	}
	c.Version++
	return c, nil
}

func (a *Service) CheckSettings(gc *core.Context, u access.User) (map[string]any, error) {
	r := gc.Request
	level, e := a.Permissions.Read(r, u)
	if e != nil {
		return nil, e
	}
	if level < 40 {
		return nil, serrors.NewError(403, "PROJECT_ADMIN_REQUIRED", "需要项目管理员权限")
	}
	c, e := a.Settings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return nil, e
	}
	if e = c.Validate(); e != nil {
		return nil, e
	}
	id, e := a.ProjectID(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		return nil, e
	}
	b, e := a.GitLab.Branch(r.Context(), id, c.BaseBranch)
	if e != nil {
		return nil, e
	}
	if b.Commit.ID == "" {
		return nil, serrors.NewError(409, "EMPTY_BASE", "基准分支没有可用提交")
	}
	if e = a.CheckAgentBinding(r.Context(), r.PathValue("tid"), r.PathValue("pid"), &c); e != nil {
		return nil, e
	}
	now := time.Now().UTC()
	updated, e := table.MarkProjectSettingsChecked(
		r.Context(),
		a.DB,
		r.PathValue("tid"),
		r.PathValue("pid"),
		c.Version,
		now,
	)
	if e != nil {
		return nil, e
	}
	if !updated {
		return nil, serrors.NewError(412, "STALE_CONFIG", "检查期间配置已变化，请重新检查")
	}
	c.CheckedAt = &now
	return map[string]any{"settings": c, "baseHead": b.Commit.ID, "message": "仓库与 Agent 发布版本检查通过；必过检查会在每轮实际执行"}, nil
}
