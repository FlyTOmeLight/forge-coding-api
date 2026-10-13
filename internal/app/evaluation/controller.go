package evaluation

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type Controller struct{ service *Service }

func NewController(service *Service) *Controller { return &Controller{service: service} }

func (c *Controller) RegisterRoutes(r *core.RouterGroup) {
	r.POST("/v1/evaluation-environments/:trial", c.environment)
	r.GET("/v1/evaluation-environments/:trial/sandbox/*path", c.sandbox)
	r.POST("/v1/evaluation-environments/:trial/sandbox/*path", c.sandbox)
	r.POST("/v1/evaluation-tools/finish", c.finish)
	r.POST("/v1/evaluation-tools/run_checks", c.checks)
}

// environment godoc
// @Summary Reconcile an isolated evaluation environment
// @Tags evaluation
// @Accept json
// @Produce json
// @Param trial path string true "Trial ID"
// @Param request body codingv1.EvaluationEnvironmentRequest true "Evaluation input and phase"
// @Success 200 {object} Evidence
// @Failure 400 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 422 {object} httpapi.ErrorResponse
// @Failure 503 {object} httpapi.ErrorResponse
// @Router /internal/v1/evaluation-environments/{trial} [post]
func (c *Controller) environment(gc *core.Context) {
	_ = http.NewResponseController(gc.Writer).SetWriteDeadline(time.Now().Add(190 * time.Second))
	ctx, cancel := context.WithTimeout(gc.Request.Context(), 180*time.Second)
	defer cancel()
	request := gc.Request
	gc.Request = request.WithContext(ctx)
	defer func() { gc.Request = request }()
	var req codingv1.EvaluationEnvironmentRequest
	if err := httpapi.ReadJSON(gc, &req); err != nil {
		httpapi.Failure(gc, err)
		return
	}
	out, err := c.service.Environment(gc, gc.Request.PathValue("trial"), req)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	gc.JSON(200, out)
}

// sandbox godoc
// @Summary Access evaluation sandbox tools
// @Tags evaluation
// @Param trial path string true "Trial ID"
// @Param path path string true "Sandbox tool path"
// @Param X-Agentic-Scope header string true "Execution scope"
// @Param X-Agentic-Run-ID header string true "Runtime run ID"
// @Param X-Agentic-Epoch header integer true "Execution epoch"
// @Success 200 {object} map[string]interface{}
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 503 {object} httpapi.ErrorResponse
// @Router /internal/v1/evaluation-environments/{trial}/sandbox/{path} [get]
// @Router /internal/v1/evaluation-environments/{trial}/sandbox/{path} [post]
func (c *Controller) sandbox(gc *core.Context) {
	w, r := gc.Writer, gc.Request
	endpoint, err := c.service.Sandbox(gc)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	p := r.PathValue("path")
	target, err := url.Parse(endpoint.URL)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if r.Method == "GET" && p == "/v1/sandbox" {
			httpapi.Failure(gc, serrors.NewError(503, "SANDBOX_PREPARING", "等待代码环境服务就绪"))
			return
		}
		httpapi.Failure(gc, serrors.NewError(502, "SANDBOX_UNAVAILABLE", "代码工具连接中断"))
	}
	original := proxy.Director
	proxy.Director = func(out *http.Request) {
		original(out)
		out.URL.Path = p
		out.URL.RawPath = ""
		out.Header.Set("Authorization", "Bearer "+endpoint.Token)
		out.Header.Del("X-Agentic-Scope")
		out.Header.Del("X-Agentic-Run-ID")
		out.Header.Del("X-Agentic-Epoch")
	}
	proxy.ServeHTTP(w, r)
}

// finish godoc
// @Summary Submit an evaluation candidate summary
// @Tags evaluation
// @Accept json
// @Produce json
// @Param request body codingv1.EvaluationFinishRequest true "Candidate summary"
// @Success 200 {object} codingv1.EvaluationFinishResponse
// @Failure 400 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Router /internal/v1/evaluation-tools/finish [post]
func (c *Controller) finish(gc *core.Context) {
	var req codingv1.EvaluationFinishRequest
	if err := httpapi.ReadJSON(gc, &req); err != nil {
		httpapi.Failure(gc, err)
		return
	}
	out, err := c.service.Finish(gc, req)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	gc.JSON(200, out)
}

// checks godoc
// @Summary Run configured evaluation candidate checks
// @Tags evaluation
// @Produce json
// @Success 200 {object} codingv1.ChecksResponse
// @Failure 403 {object} httpapi.ErrorResponse
// @Failure 409 {object} httpapi.ErrorResponse
// @Failure 503 {object} httpapi.ErrorResponse
// @Router /internal/v1/evaluation-tools/run_checks [post]
func (c *Controller) checks(gc *core.Context) {
	out, err := c.service.Checks(gc)
	if err != nil {
		httpapi.Failure(gc, err)
		return
	}
	gc.JSON(200, out)
}
