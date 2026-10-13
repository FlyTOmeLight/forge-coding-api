package coding

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

func (a *App) authorized(fn func(*gin.Context, User)) gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		cookie, err := r.Cookie("forge_session")
		if err != nil {
			fail(c, problem(401, "AUTH_REQUIRED", "请先登录"))
			return
		}
		var u User
		err = client.Request(
			r.Context(),
			a.HTTP,
			"POST",
			a.Config.TenantURL+"/internal/v1/session/check",
			a.Config.TenantToken,
			Object{
				"session": cookie.Value,
				"method":  r.Method,
				"origin":  r.Header.Get("Origin"),
				"csrf":    r.Header.Get("X-CSRF-Token"),
			},
			&u,
		)
		if err != nil {
			fail(c, err)
			return
		}
		fn(c, u)
	}
}

func (a *App) access(ctx context.Context, uid, tid, pid string) (Object, error) {
	var v struct {
		Level int `json:"level"`
	}
	e := client.Request(
		ctx,
		a.HTTP,
		"POST",
		a.Config.TenantURL+"/internal/v1/access/check",
		a.Config.TenantToken,
		Object{"user_id": uid, "tenant_id": tid, "project_id": pid, "action": "read"},
		&v,
	)
	return Object{"level": v.Level}, e
}

type Project struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Path     string `json:"path"`
	GitLabID int64  `json:"gitlab_id"`
}

func (a *App) project(ctx context.Context, tid, pid string) (Project, error) {
	var p Project
	e := client.Request(
		ctx,
		a.HTTP,
		"POST",
		a.Config.TenantURL+"/internal/v1/projects/resolve",
		a.Config.TenantToken,
		Object{"tenant_id": tid, "project_id": pid},
		&p,
	)
	return p, e
}
