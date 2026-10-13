package task

import (
	"context"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func (a *Service) checkGate(ctx context.Context, task string, b taskRunRequest) (table.Task, error) {
	t, e := table.FindTask(ctx, a.DB, task)
	if e != nil {
		return t, serrors.NewError(404, "NOT_FOUND", "任务不存在")
	}
	level, e := a.Permissions.Level(ctx, t.OwnerID, t.TenantID, t.ProjectID)
	if e != nil {
		return t, e
	}
	if level < 30 || t.Control != "Agent" || t.Epoch != b.Epoch || t.ActiveRun == nil || *t.ActiveRun != b.RunID {
		return t, serrors.NewError(409, "EXECUTION_REVOKED", "人工介入或授权变化，禁止继续写入")
	}
	if b.Worker == "" || b.RuntimeEpoch < 1 {
		return t, serrors.NewError(409, "STALE_EXECUTION", "执行身份无效")
	}
	allowed, err := a.Runtime.Lease(ctx, b.Worker, b.RuntimeEpoch)
	if err != nil {
		return t, err
	}
	if !allowed {
		return t, serrors.NewError(409, "RUNTIME_LEASE_LOST", "当前执行者的执行权已失效")
	}
	var ok bool
	ok, e = table.RunLeaseMatches(ctx, a.DB, b.RunID, t.ID, b.Worker, b.Epoch)
	if e != nil {
		return t, e
	}
	if !ok {
		return t, serrors.NewError(409, "STALE_EXECUTION", "本轮执行身份失效")
	}
	return t, nil
}
