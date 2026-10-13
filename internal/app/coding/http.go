package coding

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"
)

type Object = map[string]any

type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Operator bool   `json:"operator"`
}

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

func ID(prefix string) string { return prefix + "_" + Token()[:24] }

func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func problem(status int, code, message string) error { return serrors.NewError(status, code, message) }

func fail(c *gin.Context, e error) {
	var api *serrors.Error
	if errors.As(e, &api) {
		c.AbortWithStatusJSON(api.Status, Object{"error": Object{"code": api.Code, "message": api.Message}})
		return
	}
	slog.Error("request failed", "error", e.Error())
	c.AbortWithStatusJSON(500, Object{"error": Object{"code": "INTERNAL_ERROR", "message": "操作失败，请稍后重试或查看服务日志"}})
}

// Preserve Coding's business error envelope while sharing strict framework binding.
func readJSON(c *gin.Context, v any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	if err := binder.Bind(c, v).JSON().Error(); err != nil {
		var detail *serrors.Error
		if errors.As(err, &detail) && detail.Status == http.StatusRequestEntityTooLarge {
			return problem(413, "INVALID_JSON", "请求内容过大")
		}
		return problem(400, "INVALID_JSON", "请求内容无效")
	}
	return nil
}
