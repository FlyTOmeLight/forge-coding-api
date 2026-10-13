package coding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type CodingSettings codingv1.Settings

func (a *App) codingSettings(ctx context.Context, tid, pid string) (CodingSettings, error) {
	var c CodingSettings
	var raw []byte
	e := a.DB.QueryRow(ctx, `SELECT version,settings,checked_at FROM coding_project_settings WHERE tenant_id=$1 AND project_id=$2`, tid, pid).
		Scan(&c.Version, &raw, &c.CheckedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return CodingSettings{
			Version:        0,
			RepairLimit:    3,
			TimeoutMinutes: 30,
			AllowedPaths:   []string{},
			Checks:         []string{},
		}, nil
	}
	if e != nil {
		return c, e
	}
	version, checked := c.Version, c.CheckedAt
	e = json.Unmarshal(raw, &c)
	c.Version, c.CheckedAt = version, checked
	return c, e
}

func (c CodingSettings) validate() error {
	if strings.TrimSpace(c.AgentID) == "" || c.AgentVersion < 1 {
		return problem(422, "AGENT_NOT_ENABLED", "请从通过评测的 Coding 报告为项目启用候选")
	}
	return c.validateParameters()
}

func (c CodingSettings) validateParameters() error {
	if strings.TrimSpace(c.BaseBranch) == "" || len(c.AllowedPaths) == 0 || len(c.AllowedPaths) > 20 ||
		len(c.Checks) == 0 ||
		len(c.Checks) > 10 ||
		c.RepairLimit < 1 ||
		c.RepairLimit > 3 ||
		c.TimeoutMinutes < 1 ||
		c.TimeoutMinutes > 30 {
		return problem(
			422,
			"INVALID_EXECUTION_SETTINGS",
			"请选择已发布 Agent 及版本，并设置基准分支、修改范围和必过检查；修复次数为 1–3，执行时间为 1–30 分钟",
		)
	}
	for _, p := range c.AllowedPaths {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) == "." {
			return problem(422, "INVALID_PATH", "修改范围必须是仓库内的相对文件或目录")
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return problem(422, "INVALID_PATH", "修改范围不能包含上级目录")
			}
		}
	}
	for _, cmd := range c.Checks {
		if strings.TrimSpace(cmd) == "" || len(cmd) > 500 || strings.ContainsAny(cmd, "\r\n") {
			return problem(422, "INVALID_CHECK", "每行填写一条检查命令")
		}
	}
	return nil
}

func (a *App) getCodingSettings(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	c, e := a.codingSettings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(gc, e)
		return
	}
	id, e := a.taskProject(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(gc, e)
		return
	}
	branches, e := a.GitLab.Branches(r.Context(), id)
	if e != nil {
		fail(gc, e)
		return
	}
	runtime := Object{"available": a.runtimeReady(r.Context()), "service": "agentic-kit"}
	agents := []AgentChoice{}
	if level >= 40 && c.AgentID != "" {
		var agent AgentChoice
		e = client.Request(
			r.Context(),
			a.HTTP,
			"GET",
			a.Config.RuntimeURL+"/v1/agents/"+url.PathEscape(c.AgentID),
			a.Config.RuntimeToken,
			nil,
			&agent,
		)
		agents = append(agents, agent)
		if e != nil {
			fail(gc, e)
			return
		}
	}
	gc.JSON(
		200,
		Object{
			"settings": c,
			"branches": branches,
			"runtime":  runtime,
			"agents":   agents,
			"permissions": Object{
				"readCode":           level >= 20,
				"createTask":         level >= 30,
				"configureExecution": level >= 40,
			},
		},
	)
}

func (a *App) saveCodingSettings(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	if level < 40 {
		fail(gc, problem(403, "PROJECT_ADMIN_REQUIRED", "需要项目管理员权限"))
		return
	}
	var c CodingSettings
	if e = readJSON(gc, &c); e != nil {
		fail(gc, e)
		return
	}
	current, err := a.codingSettings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if err != nil {
		fail(gc, err)
		return
	}
	if current.Version == 0 && (c.AgentID != "" || c.AgentVersion != 0) {
		fail(gc, problem(409, "EVALUATED_RELEASE_REQUIRED", "请先保存执行参数，再从通过的评测报告启用候选"))
		return
	}
	if e = c.validateParameters(); e != nil {
		fail(gc, e)
		return
	}
	if c.AgentID != "" {
		if e = a.checkAgentBinding(r.Context(), r.PathValue("tid"), r.PathValue("pid"), &c); e != nil {
			fail(gc, e)
			return
		}
	} else {
		c.Model = ""
	}
	if current.Version > 0 && (c.AgentID != current.AgentID || c.AgentVersion != current.AgentVersion) {
		fail(gc, problem(409, "EVALUATED_RELEASE_REQUIRED", "请通过评测报告启用候选，或从项目启用记录回退"))
		return
	}
	if current.ReleaseID != "" {
		if !sameReleaseConfiguration(current, c) || (c.ReleaseID != "" && c.ReleaseID != current.ReleaseID) {
			fail(gc, problem(409, "EVALUATED_RELEASE_REQUIRED", "当前绑定来自评测启用。修改 Agent、基准分支、范围或检查须重新评测并启用"))
			return
		}
		c.ReleaseID = current.ReleaseID
	} else if c.ReleaseID != "" {
		fail(gc, problem(422, "INVALID_RELEASE", "请通过评测报告启用版本"))
		return
	}
	c.CheckedAt = nil
	raw, _ := json.Marshal(c)
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(gc, e)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(
		r.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"coding-settings:"+r.PathValue("pid"),
	)
	if e != nil {
		fail(gc, e)
		return
	}
	var version int64
	e = tx.QueryRow(r.Context(), `SELECT version FROM coding_project_settings WHERE tenant_id=$1 AND project_id=$2 FOR UPDATE`, r.PathValue("tid"), r.PathValue("pid")).
		Scan(&version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		fail(gc, e)
		return
	}
	if version != c.Version {
		fail(gc, problem(412, "STALE_CONFIG", "项目配置已变化，请刷新后保存"))
		return
	}
	_, e = tx.Exec(
		r.Context(),
		`INSERT INTO coding_project_settings(tenant_id,project_id,version,settings) VALUES($1,$2,1,$3) ON CONFLICT(project_id) DO UPDATE SET version=coding_project_settings.version+1,settings=EXCLUDED.settings,checked_at=NULL`,
		r.PathValue("tid"),
		r.PathValue("pid"),
		raw,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(gc, e)
		return
	}
	c.Version++
	gc.JSON(200, c)
}

func (a *App) modelNames(ctx context.Context) ([]string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(a.Config.ModelURL, "/")+"/api/tags", nil)
	if e != nil {
		return nil, e
	}
	response, e := a.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, problem(502, "MODEL_UNAVAILABLE", "内网模型服务不可用")
	}
	var v struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if e = json.NewDecoder(response.Body).Decode(&v); e != nil {
		return nil, e
	}
	names := []string{}
	for _, m := range v.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

func (a *App) checkCodingSettings(gc *core.Context, u User) {
	r := gc.Request
	level, e := a.taskRead(r, u)
	if e != nil {
		fail(gc, e)
		return
	}
	if level < 40 {
		fail(gc, problem(403, "PROJECT_ADMIN_REQUIRED", "需要项目管理员权限"))
		return
	}
	c, e := a.codingSettings(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(gc, e)
		return
	}
	if e = c.validate(); e != nil {
		fail(gc, e)
		return
	}
	id, e := a.taskProject(r.Context(), r.PathValue("tid"), r.PathValue("pid"))
	if e != nil {
		fail(gc, e)
		return
	}
	b, e := a.GitLab.Branch(r.Context(), id, c.BaseBranch)
	if e != nil {
		fail(gc, e)
		return
	}
	if b.Commit.ID == "" {
		fail(gc, problem(409, "EMPTY_BASE", "基准分支没有可用提交"))
		return
	}
	if e = a.checkAgentBinding(r.Context(), r.PathValue("tid"), r.PathValue("pid"), &c); e != nil {
		fail(gc, e)
		return
	}
	now := time.Now().UTC()
	tag, e := a.DB.Exec(
		r.Context(),
		`UPDATE coding_project_settings SET checked_at=$4 WHERE tenant_id=$1 AND project_id=$2 AND version=$3`,
		r.PathValue("tid"),
		r.PathValue("pid"),
		c.Version,
		now,
	)
	if e != nil {
		fail(gc, e)
		return
	}
	if tag.RowsAffected() != 1 {
		fail(gc, problem(412, "STALE_CONFIG", "检查期间配置已变化，请重新检查"))
		return
	}
	c.CheckedAt = &now
	gc.JSON(200, Object{"settings": c, "baseHead": b.Commit.ID, "message": "仓库与 Agent 发布版本检查通过；必过检查会在每轮实际执行"})
}

func (a *App) codingConnections(gc *core.Context, u User) {
	r := gc.Request
	if !u.Operator {
		fail(gc, problem(403, "FORBIDDEN", "需要平台管理权限"))
		return
	}
	models, e := a.modelNames(r.Context())
	modelStatus := "available"
	if e != nil {
		modelStatus = "unavailable"
		models = []string{}
	}
	_, e = a.GitLab.Version(r.Context())
	gitStatus := "available"
	if e != nil {
		gitStatus = "unavailable"
	}
	identityStatus := "available"
	if client.Request(
		r.Context(),
		a.HTTP,
		"GET",
		a.Config.TenantURL+"/healthz",
		a.Config.TenantToken,
		nil,
		nil,
	) != nil {
		identityStatus = "unavailable"
	}
	gc.JSON(
		200,
		Object{
			"gitlab":   Object{"status": gitStatus, "url": a.Config.GitLabPublicURL},
			"model":    Object{"status": modelStatus, "models": models},
			"identity": Object{"status": identityStatus},
		},
	)
}
