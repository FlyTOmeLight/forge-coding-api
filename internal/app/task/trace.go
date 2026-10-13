package task

import (
	"encoding/json"
	"strconv"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type RuntimeTrace = codingv1.RuntimeTrace

func (a *Service) getRunTrace(gc *core.Context, u access.User) (RuntimeTrace, error) {
	r := gc.Request
	if _, err := a.Permissions.Read(r, u); err != nil {
		return RuntimeTrace{}, err
	}
	after := int64(0)
	if text := r.URL.Query().Get("after"); text != "" {
		var err error
		after, err = strconv.ParseInt(text, 10, 64)
		if err != nil || after < 0 {
			return RuntimeTrace{}, serrors.NewError(400, "INVALID_CURSOR", "事件序号无效")
		}
	}
	var runtimeID *string
	runtimeID, err := table.ScopedRuntimeID(
		r.Context(),
		a.DB,
		r.PathValue("run"),
		r.PathValue("task"),
		r.PathValue("tid"),
		r.PathValue("pid"),
	)
	if err == pgx.ErrNoRows {
		return RuntimeTrace{}, serrors.NewError(404, "NOT_FOUND", "执行轮次不存在")
	}
	if err != nil {
		return RuntimeTrace{}, err
	}
	trace := RuntimeTrace{Events: []json.RawMessage{}, NextSeq: after}
	if runtimeID != nil {
		trace, err = a.Runtime.Trace(r.Context(), *runtimeID, after)
		if err != nil {
			return RuntimeTrace{}, err
		}
		if trace.Run == nil || trace.Run.ID != *runtimeID || trace.Run.Scope == nil ||
			trace.Run.Scope.TenantID != r.PathValue("tid") ||
			trace.Run.Scope.ProjectID != r.PathValue("pid") ||
			trace.Run.Scope.TaskID != r.PathValue("task") ||
			trace.Run.Scope.RoundID != r.PathValue("run") {
			return RuntimeTrace{}, serrors.NewError(502, "RUNTIME_SCOPE_MISMATCH", "执行记录与当前任务不匹配")
		}
	}
	return trace, nil
}
