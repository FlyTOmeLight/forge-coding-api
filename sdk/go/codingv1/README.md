# Forge Coding Go SDK

公开模块：`github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1`。v0.17.0 使用 server-go v0.9.0，要求 Go 1.27 或更新版本；SDK 独立发布，不依赖后端的 pgx、Kubernetes 或 GitLab SDK。已发布的 v0.16.1 使用 server-go v0.3.1，最低 Go 版本仍为 1.24。

```sh
go get github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1@v0.17.0
```

```go
client, err := codingv1.NewClient("https://coding.example.com", "", nil)
if err != nil { return err }
defer client.Close()

// 使用 Tenant 服务提供的有效会话和 CSRF 令牌。
scope := codingv1.ProjectScope{
    TenantID: tenantID, ProjectID: projectID,
    Session: session, CSRFToken: csrf,
}
tasks, err := client.ListTasks(ctx, scope)
```

任务、执行配置、候选启用/回退使用项目会话，仍由服务端校验实时权限。`ProjectScope` 的会话与 CSRF 只写入本次请求，不进入请求 JSON，不修改共享客户端。

评测环境、工具和 publication permit 使用构造函数传入的服务令牌。`AdvanceEvaluation` 使用 `EvaluationEnvironmentRequest`；工具调用还需要 `ExecutionIdentity` 的 scope、运行时 ID 和 epoch。服务令牌不能替代浏览器会话。

- `ListTasks`、`GetTask`、`AssociateTask`、`CreateTask`、`ActOnTask`、`SyncTask`、`GetTaskDiff`、`GetRunTrace`。
- `GetSettings`、`SaveSettings`、`CheckSettings`、`Connections`、`ListReleases`、`EnableRelease`、`RestoreRelease`。
- `AdvanceEvaluation`、`SubmitFinish`、`RunChecks`、`EvaluationFinish`、`EvaluationChecks`、`PublicationPermit`、`Sandbox`。

复用 server-go 的 SDK 传输，不自动重试。创建任务、启用和回退由调用方提供稳定幂等键。网络错误保留 `errors.Is` 的取消/超时身份；HTTP 错误返回 `*server-go/errors.Error`，`Status` 是实际 HTTP 状态，`Code` 是业务或框架错误码。支持现有嵌套业务错误与框架平铺错误。

普通调用默认 20 秒；评测环境协调为 200 秒，检查为 130 秒，调用方更短的 context 截止时间仍有效。普通 JSON 响应上限 4 MiB，差异和 trace 上限 32 MiB。trace 保持 `after`/`nextSeq` 页协议，不隐式读取后续页。

JSON 请求保留有上限的响应读取，再将业务错误 envelope 交给 `sdk.DecodeError`。`sdk.Complete` 面向 Resty 已缓冲、已解码的响应，仅识别平铺框架错误，因此本 SDK 不直接使用它替换上述处理。

`Sandbox` 返回流式 `SandboxResponse`；调用方负责关闭 `Body`，并通过 context 控制取消。SDK 不预先读取完整沙箱响应。没有原生 SSE 接口。

验证：`GOWORK=off go test -race ./...`、`GOWORK=off go vet ./...`。后端仓库的 SDK 集成测试通过真实 HTTP Controller 和隔离 PostgreSQL 验证任务幂等、项目隔离与评测服务鉴权。
