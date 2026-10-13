package platform

import (
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
)

type Controller struct {
	service     *Service
	permissions *access.Service
}

func NewController(s *Service, p *access.Service) *Controller {
	return &Controller{service: s, permissions: p}
}

func (c *Controller) RegisterRoutes(root *core.RouterGroup) {
	root.GET("/api/v1/platform/connections", c.permissions.Authorized(c.connections))
}

// connections reports configured integration availability.
// @Summary 查看平台连接状态
// @Tags Platform
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} httpapi.ErrorResponse
// @Failure 403 {object} httpapi.ErrorResponse
// @Router /api/v1/platform/connections [get]
func (c *Controller) connections(ctx *core.Context, user access.User) {
	result, err := c.service.Connections(ctx, user)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}
