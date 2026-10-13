package task

import core "github.com/FlyTOmeLight/server-go/pkg"

func (c *Controller) RegisterRoutes(root, internal *core.RouterGroup) {
	internal.POST("/v1/rounds/:run/publication-permit", c.publicationPermit)
	internal.GET("/v1/rounds/:run/sandbox/*path", c.sandboxProxy)
	internal.POST("/v1/rounds/:run/sandbox/*path", c.sandboxProxy)
	internal.POST("/v1/tools/finish", c.submitFinish)
	internal.POST("/v1/tools/run_checks", c.sandboxChecks)
	tasks := root.Group("/api/v1/tenants/:tid/projects/:pid/tasks")
	tasks.GET("", c.Service.Permissions.Authorized(c.listTasks))
	tasks.POST("", c.Service.Permissions.Authorized(c.createTask))
	tasks.POST("/new", c.Service.Permissions.Authorized(c.createProblemTask))
	tasks.GET("/:task", c.Service.Permissions.Authorized(c.getTask))
	tasks.GET("/:task/diff", c.Service.Permissions.Authorized(c.getTaskDiff))
	tasks.GET("/:task/runs/:run/trace", c.Service.Permissions.Authorized(c.getRunTrace))
	tasks.POST("/:task/actions", c.Service.Permissions.Authorized(c.taskAction))
	tasks.POST("/:task/sync", c.Service.Permissions.Authorized(c.syncTask))
}
