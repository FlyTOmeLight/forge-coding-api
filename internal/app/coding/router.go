package coding

import "github.com/gin-gonic/gin"

// Path values keep the same tenant/project scope in HTTP authorization and proxies.
func pathValues(c *gin.Context) {
	for _, param := range c.Params {
		c.Request.SetPathValue(param.Key, param.Value)
	}
	c.Next()
}
