package httpapi

import (
	"errors"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
)

func Failure(c *core.Context, e error) {
	var api *serrors.Error
	if errors.As(e, &api) {
		c.AbortWithStatusJSON(
			api.Status,
			map[string]any{"error": map[string]any{"code": api.Code, "message": api.Message}},
		)
		return
	}
	ctx := c.Request.Context()
	logger.FromContext(ctx).Errorf(ctx, "request failed: %v", e)
	c.AbortWithStatusJSON(
		500,
		map[string]any{"error": map[string]any{"code": "INTERNAL_ERROR", "message": "操作失败，请稍后重试或查看服务日志"}},
	)
}

// Preserve Coding's business error envelope while sharing strict framework binding.
func ReadJSON(c *core.Context, v any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	if err := binder.Bind(c, v).JSON().Error(); err != nil {
		var detail *serrors.Error
		if errors.As(err, &detail) && detail.Status == http.StatusRequestEntityTooLarge {
			return serrors.NewError(413, "INVALID_JSON", "请求内容过大")
		}
		return serrors.NewError(400, "INVALID_JSON", "请求内容无效")
	}
	return nil
}

// ErrorResponse documents Coding's business error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PathValues exposes Gin route parameters to request-based authorization and proxies.
func PathValues(c *core.Context) {
	for _, param := range c.Params {
		c.Request.SetPathValue(param.Key, param.Value)
	}
	c.Next()
}
