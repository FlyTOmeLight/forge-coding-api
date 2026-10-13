package task

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type Controller struct{ Service *Service }

func NewController(s *Service) *Controller { return &Controller{Service: s} }

// listTasks 列出项目任务。
// @Summary 列出项目任务
// @Tags tasks
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Success 200 {object} codingv1.TaskList
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks [get]
func (c *Controller) listTasks(ctx *core.Context, u access.User) {
	result, err := c.Service.listTasks(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, map[string]any{"items": result})
}

// getTask 读取任务详情。
// @Summary 读取任务详情
// @Tags tasks
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param task path string true "任务 ID"
// @Success 200 {object} codingv1.TaskDetail
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/{task} [get]
func (c *Controller) getTask(ctx *core.Context, u access.User) {
	result, err := c.Service.getTask(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// createTask 关联开放的 GitLab MR。
// @Summary 关联开放的 GitLab MR
// @Tags tasks
// @Produce json
// @Accept json
// @Param body body codingv1.AssociateTaskRequest true "请求内容"
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Success 201 {object} codingv1.Task
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks [post]
func (c *Controller) createTask(ctx *core.Context, u access.User) {
	var b codingv1.AssociateTaskRequest
	if err := httpapi.ReadJSON(ctx, &b); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	result, err := c.Service.createTask(ctx, u, b)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(201, result)
}

// taskAction 执行任务控制或审查操作。
// @Summary 执行任务控制或审查操作
// @Tags tasks
// @Produce json
// @Accept json
// @Param body body codingv1.TaskActionRequest true "请求内容"
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param task path string true "任务 ID"
// @Success 200 {object} codingv1.Task
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/{task}/actions [post]
func (c *Controller) taskAction(ctx *core.Context, u access.User) {
	var b codingv1.TaskActionRequest
	if err := httpapi.ReadJSON(ctx, &b); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	result, err := c.Service.taskAction(ctx, u, b)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// syncTask 同步 GitLab 任务事实。
// @Summary 同步 GitLab 任务事实
// @Tags tasks
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param task path string true "任务 ID"
// @Success 200 {object} codingv1.Task
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/{task}/sync [post]
func (c *Controller) syncTask(ctx *core.Context, u access.User) {
	result, err := c.Service.syncTask(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// createProblemTask 创建问题任务并排队执行。
// @Summary 创建问题任务并排队执行
// @Tags tasks
// @Produce json
// @Accept json
// @Param body body codingv1.CreateTaskRequest true "请求内容"
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param Idempotency-Key header string true "唯一请求标识" maxlength(100)
// @Success 200 {object} codingv1.Task "相同请求的已有任务"
// @Success 201 {object} codingv1.Task
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/new [post]
func (c *Controller) createProblemTask(ctx *core.Context, u access.User) {
	var b codingv1.CreateTaskRequest
	if err := httpapi.ReadJSON(ctx, &b); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	result, created, err := c.Service.createProblemTask(ctx, u, b)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	ctx.JSON(status, result)
}

// publicationPermit 检查执行轮次的代码发布权。
// @Summary 检查执行轮次的代码发布权
// @Tags task-tools
// @Produce json
// @Param run path string true "执行轮次 ID"
// @Success 200 {object} codingv1.PermitResponse
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /internal/v1/rounds/{run}/publication-permit [post]
func (c *Controller) publicationPermit(ctx *core.Context) {
	result, err := c.Service.publicationPermit(ctx)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// getTaskDiff 读取任务当前 MR 差异。
// @Summary 读取任务当前 MR 差异
// @Tags tasks
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param task path string true "任务 ID"
// @Success 200 {object} codingv1.TaskDiff
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/{task}/diff [get]
func (c *Controller) getTaskDiff(ctx *core.Context, u access.User) {
	result, err := c.Service.getTaskDiff(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(200, result)
}

// getRunTrace 读取任务执行事件。
// @Summary 读取任务执行事件
// @Tags tasks
// @Produce json
// @Param tid path string true "租户 ID"
// @Param pid path string true "项目 ID"
// @Param task path string true "任务 ID"
// @Param run path string true "执行轮次 ID"
// @Param after query integer false "最后读取的事件序号" minimum(0) default(0)
// @Success 200 {object} codingv1.RuntimeTrace
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /api/v1/tenants/{tid}/projects/{pid}/tasks/{task}/runs/{run}/trace [get]
func (c *Controller) getRunTrace(ctx *core.Context, u access.User) {
	result, err := c.Service.getRunTrace(ctx, u)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(200, result)
}

// submitFinish 登记 Agent 交付请求。
// @Summary 登记 Agent 交付请求
// @Tags task-tools
// @Produce json
// @Accept json
// @Param body body codingv1.FinishRequest true "请求内容"
// @Param Idempotency-Key header string true "工具操作标识"
// @Param X-Agentic-Scope header string true "执行归属 JSON"
// @Param X-Agentic-Run-ID header string true "运行时执行 ID"
// @Param X-Agentic-Epoch header integer true "运行时执行版本" minimum(1)
// @Success 200 {object} codingv1.FinishResponse
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /internal/v1/tools/finish [post]
func (c *Controller) submitFinish(ctx *core.Context) {
	var body codingv1.FinishRequest
	if err := httpapi.ReadJSON(ctx, &body); err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	result, err := c.Service.submitFinish(ctx, body)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// sandboxChecks 在当前沙箱执行项目检查。
// @Summary 在当前沙箱执行项目检查
// @Tags task-tools
// @Produce json
// @Param X-Agentic-Scope header string true "执行归属 JSON"
// @Param X-Agentic-Run-ID header string true "运行时执行 ID"
// @Param X-Agentic-Epoch header integer true "运行时执行版本" minimum(1)
// @Success 200 {object} codingv1.ChecksResponse
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /internal/v1/tools/run_checks [post]
func (c *Controller) sandboxChecks(ctx *core.Context) {
	result, err := c.Service.sandboxChecks(ctx)
	if err != nil {
		httpapi.Failure(ctx, err)
		return
	}
	ctx.JSON(200, result)
}

// sandboxProxy 访问执行轮次的沙箱能力。
// @Summary 访问执行轮次的沙箱能力
// @Tags task-tools
// @Produce json
// @Param run path string true "执行轮次 ID"
// @Param X-Agentic-Scope header string true "执行归属 JSON"
// @Param X-Agentic-Run-ID header string true "运行时执行 ID"
// @Param X-Agentic-Epoch header integer true "运行时执行版本" minimum(1)
// @Param path path string true "沙箱能力路径"
// @Description 支持 GET 和 POST；透传上游状态、Content-Type 和响应体。
// @Success 200 {object} object
// @Failure 400,401,403,404,409,412,422,500,502,503 {object} httpapi.ErrorResponse
// @Router /internal/v1/rounds/{run}/sandbox/{path} [get]
// @Router /internal/v1/rounds/{run}/sandbox/{path} [post]
func (c *Controller) sandboxProxy(gc *core.Context) {
	endpoint, apiPath, err := c.Service.sandboxEndpoint(gc)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	w, r := gc.Writer, gc.Request
	target, err := url.Parse(endpoint.URL)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path = apiPath
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+endpoint.Token)
		req.Header.Del("X-Agentic-Scope")
		req.Header.Del("X-Agentic-Run-ID")
		req.Header.Del("X-Agentic-Epoch")
	}
	proxy.Transport = c.Service.HTTP.Transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		httpapi.Failure(
			gc,
			serrors.NewError(503, "SANDBOX_UNAVAILABLE", "AIO request outcome requires inspection"),
		)
	}
	proxy.ServeHTTP(w, r)
}
