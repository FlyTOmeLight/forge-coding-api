package coding

import (
	"github.com/FlyTOmeLight/server-go/auth"
	core "github.com/FlyTOmeLight/server-go/pkg"
)

func (a *App) RegisterRoutes(m *core.RouterGroup) error {
	m.Use(pathValues)
	internal := m.Group("/internal", auth.BearerTokens(map[string]string{a.Config.ServiceToken: "coding-runtime"}))
	internal.Handle("POST", "/v1/rounds/:run/publication-permit", a.publicationPermit)
	internal.Handle("GET", "/v1/rounds/:run/sandbox/*path", a.sandboxProxy)
	internal.Handle("POST", "/v1/rounds/:run/sandbox/*path", a.sandboxProxy)
	internal.Handle("POST", "/v1/evaluation-environments/:trial", a.evaluationEnvironment)
	internal.Handle("GET", "/v1/evaluation-environments/:trial/sandbox/*path", a.evaluationSandbox)
	internal.Handle("POST", "/v1/evaluation-environments/:trial/sandbox/*path", a.evaluationSandbox)
	internal.Handle("POST", "/v1/evaluation-tools/finish", a.evaluationFinish)
	internal.Handle("POST", "/v1/evaluation-tools/run_checks", a.evaluationChecks)
	internal.Handle("POST", "/v1/tools/finish", a.submitFinish)
	internal.Handle("POST", "/v1/tools/run_checks", a.sandboxChecks)
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/tasks", a.authorized(a.listTasks))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/tasks", a.authorized(a.createTask))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/tasks/new", a.authorized(a.createProblemTask))
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/coding-releases", a.authorized(a.getCodingReleases))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/coding-releases", a.authorized(a.enableCodingRelease))
	m.Handle(
		"POST",
		"/api/v1/tenants/:tid/projects/:pid/coding-releases/:release/restore",
		a.authorized(a.restoreCodingRelease),
	)
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/coding-settings", a.authorized(a.getCodingSettings))
	m.Handle("PUT", "/api/v1/tenants/:tid/projects/:pid/coding-settings", a.authorized(a.saveCodingSettings))
	m.Handle(
		"POST",
		"/api/v1/tenants/:tid/projects/:pid/coding-settings/check",
		a.authorized(a.checkCodingSettings),
	)
	m.Handle("GET", "/api/v1/platform/connections", a.authorized(a.codingConnections))
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/tasks/:task", a.authorized(a.getTask))
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/diff", a.authorized(a.getTaskDiff))
	m.Handle("GET", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/runs/:run/trace", a.authorized(a.getRunTrace))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/actions", a.authorized(a.taskAction))
	m.Handle("POST", "/api/v1/tenants/:tid/projects/:pid/tasks/:task/sync", a.authorized(a.syncTask))
	return nil
}

// Path values keep the same tenant/project scope in HTTP authorization and proxies.
func pathValues(c *core.Context) {
	for _, param := range c.Params {
		c.Request.SetPathValue(param.Key, param.Value)
	}
	c.Next()
}
