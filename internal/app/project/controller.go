// Package project manages execution settings and evaluated candidate activation.
package project

import (
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type Controller struct{ service *Service }

func NewController(s *Service) *Controller { return &Controller{service: s} }

func (c *Controller) RegisterRoutes(m *core.RouterGroup) {
	authorized := c.service.Permissions.Authorized
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/coding-settings", authorized(c.GetSettings))
	m.Handle("PUT", "/api/v1/tenants/:tid/projects/:pid/coding-settings", authorized(c.SaveSettings))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/coding-settings/check", authorized(c.CheckSettings))
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/coding-releases", authorized(c.ListReleases))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/coding-releases", authorized(c.EnableRelease))
	m.Handle(
		"POST",
		"/api/v1/tenants/:tid/projects/:pid/coding-releases/:release/restore",
		authorized(c.RestoreRelease),
	)
}

// GetSettings godoc
// @Summary 获取项目执行配置及可用分支
// @Tags project
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Success 200 {object} codingv1.SettingsResponse
// @Failure 401 {object} httpapi.ErrorResponse
// @Failure 404 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-settings [get]
func (c *Controller) GetSettings(ctx *core.Context, u access.User) {
	out, err := c.service.GetSettings(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// SaveSettings godoc
// @Summary 保存项目执行参数
// @Tags project
// @Accept json
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param settings body codingv1.SaveSettingsRequest true "执行参数与当前配置版本"
// @Success 200 {object} codingv1.Settings
// @Failure 400 {object} httpapi.ErrorResponse
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 412 {object} httpapi.ErrorResponse
// @Failure 422 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-settings [put]
func (c *Controller) SaveSettings(ctx *core.Context, u access.User) {
	var req codingv1.SaveSettingsRequest
	if err := httpapi.ReadJSON(ctx, &req); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	out, err := c.service.SaveSettings(ctx, u, Settings(req))
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// CheckSettings godoc
// @Summary 检查仓库与 Agent 发布版本
// @Tags project
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Success 200 {object} codingv1.SettingsCheck
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 412 {object} httpapi.ErrorResponse
// @Failure 422 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-settings/check [post]
func (c *Controller) CheckSettings(ctx *core.Context, u access.User) {
	out, err := c.service.CheckSettings(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// ListReleases godoc
// @Summary 获取项目启用与回退记录
// @Tags project
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Success 200 {object} codingv1.ReleaseList
// @Failure 401 {object} httpapi.ErrorResponse
// @Failure 404 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-releases [get]
func (c *Controller) ListReleases(ctx *core.Context, u access.User) {
	out, err := c.service.ListReleases(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// EnableRelease godoc
// @Summary 从通过的评测启用候选版本
// @Tags project
// @Accept json
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param Idempotency-Key header string true "唯一请求标识，最多 100 字符"
// @Param request body codingv1.EnableReleaseRequest true "候选和评测身份"
// @Success 201 {object} codingv1.Release
// @Success 200 {object} codingv1.Release "幂等重放"
// @Failure 400 {object} httpapi.ErrorResponse
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 412 {object} httpapi.ErrorResponse
// @Failure 422 {object} httpapi.ErrorResponse
// @Failure 502 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-releases [post]
func (c *Controller) EnableRelease(ctx *core.Context, u access.User) { c.changeRelease(ctx, u, false) }

// RestoreRelease godoc
// @Summary 从启用历史回退项目配置
// @Tags project
// @Accept json
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param release path string true "启用记录 ID"
// @Param Idempotency-Key header string true "唯一请求标识，最多 100 字符"
// @Param request body codingv1.RestoreReleaseRequest true "当前配置版本"
// @Success 201 {object} codingv1.Release
// @Success 200 {object} codingv1.Release "幂等重放"
// @Failure 400 {object} httpapi.ErrorResponse
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 404 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 412 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/coding-releases/{release}/restore [post]
func (c *Controller) RestoreRelease(ctx *core.Context, u access.User) { c.changeRelease(ctx, u, true) }

func (c *Controller) changeRelease(ctx *core.Context, u access.User, restore bool) {
	var req codingv1.EnableReleaseRequest
	if err := httpapi.ReadJSON(ctx, &req); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	out, created, err := c.service.ChangeRelease(ctx, u, req, restore)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	ctx.JSON(status, out)
}
