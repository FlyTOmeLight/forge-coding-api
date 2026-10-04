package coding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"log/slog"
	"net/http"
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
func Hash(s string) string    { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string                    { return e.Code }
func problem(status int, code, message string) error { return &APIError{status, code, message} }
func fail(w http.ResponseWriter, e error) {
	var api *APIError
	if errors.As(e, &api) {
		JSON(w, api.Status, Object{"error": Object{"code": api.Code, "message": api.Message}})
		return
	}
	slog.Error("request failed", "error", e.Error())
	JSON(w, 500, Object{"error": Object{"code": "INTERNAL_ERROR", "message": "操作失败，请稍后重试或查看服务日志"}})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return readJSONLimit(w, r, v, 65536)
}
func readJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if e := d.Decode(v); e != nil {
		return problem(400, "INVALID_JSON", "请求内容无效")
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return problem(400, "INVALID_JSON", "仅接受一个 JSON 对象")
	}
	return nil
}
func collect(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, sql string, args ...any) ([]map[string]any, error) {
	rows, e := db.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	out, e := pgx.CollectRows(rows, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, e
}
