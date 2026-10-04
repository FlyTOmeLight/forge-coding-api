package coding

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// route keeps HTTP handlers framework independent while Gin owns routing.
// SetPathValue preserves the same tenant/project identity in all authorization checks.
func route(engine *gin.Engine, method, path string, handler http.HandlerFunc) {
	engine.Handle(method, path, func(c *gin.Context) {
		for _, param := range c.Params {
			c.Request.SetPathValue(param.Key, param.Value)
		}
		handler(c.Writer, c.Request)
	})
}
