# Forge Coding API

企业 Coding Agent 的业务后端，使用 Go / [server-go v0.3.1](https://github.com/FlyTOmeLight/server-go/releases/tag/v0.3.1) / Gin。前端复用 [forge-tenant-web](https://github.com/FlyTOmeLight/forge-tenant-web)。租户、团队和项目权限由 [forge-tenant-api](https://github.com/FlyTOmeLight/forge-tenant-api) 管理。模型调用和持久化执行由 [agentic-kit-api](https://github.com/FlyTOmeLight/agentic-kit-api) 与 [agentic-kit-worker](https://github.com/FlyTOmeLight/agentic-kit-worker) 承担。

研发任务包含负责人、指定审查者、验收条件、分支、MR、人工介入和多轮执行。每轮保存项目配置与 Agent 发布版本，并关联一个 Agentic `runtime_id`；同一任务跨轮复用同一个 Agent 会话。执行完成不会直接完成研发任务；开放任务在同步到 MR 合入事实时完成。

## 目录

- `cmd/server/`：API、dispatcher、migrate 入口。
- `cmd/import-tenant/`：停写后将旧租户库的任务表导入空 Coding 数据库。
- `internal/service/`：Bootstrap 资源装配、HTTPHost 服务生命周期和独立 dispatcher。
- `internal/table/`：pgx 数据库连接、迁移、任务模型与事务内持久化。
- `internal/app/coding/`：任务业务、HTTP 接口、租户授权、执行提交和结果验证。
- `internal/client/`：租户与运行时 HTTP 协议；`gitlab/` 使用官方 Go SDK，保留业务类型与 Secret 轮换映射。
- `sdk/go/codingv1/`：独立发布的公开 Go SDK，任务、配置、候选发布、评测和工具契约。
- `internal/config/`、`conf/server/`：配置加载与配置样例。
- `internal/workspace/`：任务级 AIO Pod/PVC、仓库准备、候选快照与受控 GitLab 推送。
- `deploy/docker/`：服务镜像；镜像内自带 `/conf/server/config.toml`。
- `deploy/charts/`：API/dispatcher、数据库和沙箱资源权限的 Helm Chart。

## 运行与部署

框架从 [conf/server/config.toml](conf/server/config.toml) 加载配置，使用 `${env:NAME:default}` 读取已有环境变量；`SERVER_CONFIG_PATH` 可指定其他文件。配置项见 [conf/server/env.example](conf/server/env.example)。凭据由 Kubernetes Secret 注入，不提交到仓库。`CODING_PROJECTS_JSON` 显式启用 GitLab 项目 ID。`WORKSPACE_NAMESPACE` 指定 AIO 资源命名空间，`SANDBOX_GATEWAY_URL` 是 Worker 访问的 Coding API 地址，`WORKSPACE_JOB_IMAGE` 使用当前 Coding 镜像。Helm 自动注入后三项并配置 ServiceAccount/Role。

```sh
go build -o bin/coding ./cmd/server
./bin/coding migrate
./bin/coding api
# 独立进程
./bin/coding dispatcher

docker build -f deploy/docker/Dockerfile --secret id=github_netrc,src=/path/to/github.netrc -t forge-coding-api:dev .
helm upgrade --install forge-coding deploy/charts/forge-coding -n forge-tenant-split \
  --set image.repository=forge-coding-api,existingSecret=forge-coding-env,database.existingSecret=forge-coding-db
```

先部署 Agentic API/Worker 各自仓库中的 Helm Chart。API 的 `API_TOKENS_JSON` 与 `CONFIG_OWNERS` 配置 Coding 服务身份；Worker 的 `CODING_TOOLS_TOKEN` 对应 Coding API 的 `SERVICE_TOKEN`。Coding 通过 HTTP 访问租户与运行时，不共享它们的数据库。Agentic API 与 Worker 继续共享运行时数据库。

每个任务创建独立 AIO Pod 和持久卷。反馈轮复用同一个 Pod、代码目录、Python 环境和后台进程。仓库按冻结版本准备后，Eino 原生文件工具和 Shell 通过官方 AIO Go SDK 操作沙箱。模型调用 `finish` 后，平台短暂暂停沙箱进程，将候选复制到本轮独立持久卷后恢复进程，启动无 GitLab 凭据的验收环境，再校验范围、文件内容和分支版本，推送并创建或更新 Draft MR。发布期间再次暂停进程，更新沙箱内受控 Git 元数据，使下一轮 HEAD 对齐已发布提交。GitLab 凭据只供固定的准备与发布程序使用。`allowedPaths` 是允许交付的路径，Shell 的临时文件不受其限制。

工具写入和命令执行按 Unsafe 处理；结果不明时进入人工检查。Worker 接管前停止旧沙箱计算实例，PVC 保留代码。取消、人工介入和故障停止需要确认相关 Pod 已停止。正常交付保留任务沙箱；任务结束或无活动轮次且闲置 30 分钟后停止计算。工作卷保留，下一轮可重新创建 Pod。人工移动分支时刷新仓库，工作目录外的依赖缓存继续保留。已完成轮次的 PVC 保留用于检查，当前需由管理员按保留策略清理。

server-go 负责配置快照、资源初始化与关闭、请求 ID、请求日志、Recovery 和 404/405。业务错误继续返回 `{error:{code,message}}`，框架的通用错误返回平铺 `{code,message,requestId,statusCode}`。JSON 接口拒绝未知字段及多个文档，沿用 64 KiB 业务请求上限，超限返回 413。`/healthz` 表示框架服务状态，`/readyz` 检查数据库；Helm 使用后者作为 API 就绪探针。运行配置在启动时冻结，修改配置后重启对应进程；GitLab Secret 文件仍按请求读取。

新增依赖为官方 AIO Go SDK（对接实际文件和命令协议）与 Kubernetes client-go（资源创建、原生 exec 暂停/恢复、状态与停止确认）。不再部署自定义 Python 工具服务。

## 验证

```sh
TEST_DATABASE_URL=postgres://... go test -race -count=1 ./...
go vet ./...
helm lint deploy/charts/forge-coding --set existingSecret=test,database.existingSecret=test
```

缺少 `TEST_DATABASE_URL` 时数据库用例会跳过，不代表集成验证通过。真实模型试点、迁移与控制验证见 [docs/verification.md](docs/verification.md)。业务契约见 [docs/coding-tasks.md](docs/coding-tasks.md)，迁移步骤见 [docs/migration.md](docs/migration.md)。

## 配置 Coding Agent

`agents/coding.json` 维护 Agent 提示词、模型和工具定义。先调整模型地址与Coding API 地址，再使用 Agentic API 仓库的 `scripts/publish-agent.py` 发布。该路径供平台管理员维护模板。项目用户从通过评测的报告启用候选；执行配置页不直接选择运行时 Agent。业务后端只提交版本引用和任务输入；Worker 统一执行 Writing、Coding 等 Agent。

首次切换需要显式绑定发布版本，不能直接沿用只有模型名称的旧配置。切换步骤与当前迁移边界见 [执行契约](docs/coding-tasks.md)。

### 执行过程

`GET /api/v1/tenants/:tid/projects/:pid/tasks/:task/runs/:run/trace?after=0` 只读返回运行时公共事件页。先验证项目读权限及轮次归属，再校验运行时 scope；浏览器不持有运行时凭据。未分配运行时 ID 时返回 `run:null` 与空事件。按 `nextSeq` 翻页，`hasMore:false` 后轮询；错误明确返回，刷新不恢复任务。工具参数与结果是有截断标记的预览，来源为公共消息和持久化事件，不读取执行器私有 checkpoint。

## GitLab 服务令牌自动更新

`gitlabCredentials.enabled=true` 启用 Kubernetes CronJob。默认每天北京时间 02:15 检查，在到期前 14 天通过 GitLab 管理员 API 创建同账号、同 scope 的 90 天 PAT。源令牌需要实例管理员的 `api` scope。配置 `gitlabCredentials.url`、`otherNamespaces` 和 `otherTargets`，覆盖共享这枚令牌的服务。配置文件不填写令牌明文。

任务只替换受管命名空间内与旧值相同的 `GITLAB_TOKEN` / `git-token`。应用通过 Secret 目录挂载与 `GITLAB_TOKEN_FILE` 按请求读取，无需重启 Pod。每个 `otherTargets` 项包含 `namespace`、`deployment` 和 `container`。目标应用需支持 `/forge gitlab-credential-status`，该命令只输出令牌元数据。

新旧令牌默认重叠至少 24 小时。所有目标 Pod 确认加载新令牌，且仓库准备、快照和发布进程结束后，才撤销旧令牌。未完成的切换保存在专用 Secret 中，失败 Job 可恢复。原生 Lease 将定时与手动 Job 串行执行。日志不输出令牌。

```sh
kubectl -n forge-tenant-split get cronjob forge-coding-gitlab-credentials
kubectl -n forge-tenant-split create job --from=cronjob/forge-coding-gitlab-credentials gitlab-credentials-check
kubectl -n forge-tenant-split logs job/gitlab-credentials-check
```

定时任务需在原令牌仍有效时运行。集群长时间停机导致令牌已过期时，需要重新提供有效的源令牌。官方 API：[创建 PAT](https://docs.gitlab.com/api/user_tokens/)、[查询和撤销 PAT](https://docs.gitlab.com/api/personal_access_tokens/)。


## Coding 评测环境

`POST /internal/v1/evaluation-environments/:trial` 接受受信任平台的冻结 scope、scenario 和 prepare/verify/stop 阶段。复用 AIO 工作区、候选快照与独立 verifier；每个 Trial 独立存储、计算和执行权，不创建业务任务、远端分支或 MR。服务身份与实时项目权限共同约束执行。filesystem、finish、run_checks 使用 `/internal/v1/evaluation-*` 路由，并检查 runtime Trial lease。

`coding_evaluation_environments` 保存不可变输入、补丁及实际检查结果。完成后清理环境的 Pod、Service、PVC 和 Secret；检查结果先持久化再删除来源。协调器重试沿用同一 Trial，孤立环境在 25 小时后回收。独立检查总预算 120 秒，验证接口请求预算 180 秒。现有业务任务的发布与 MR 流程不使用这些接口。


## 通过评测启用项目候选

项目新增 `GET/POST /api/v1/tenants/:tid/projects/:pid/coding-releases` 和 `POST /coding-releases/:release/restore`。查看需要项目读权限，启用和回退需要项目管理权限。写入包含 `settingsVersion` 和 `Idempotency-Key`；启用还包含 `experimentID,agentID,agentVersion`。历史、项目绑定和修订在同一事务保存。旧配置在首次启用时记录为可恢复的基线。

启用由 Agentic API 审核精确候选、实验和独立检查门槛。Coding 核对实验的基准 commit 仍等于项目基准分支，并使用该实验冻结的修改范围和检查命令。业务版本只能使用业务工具网关。回退读取本项目的历史快照，重新核对执行绑定和基准分支。

已有绑定不能通过普通 coding-settings 保存更换 Agent 或发布版本。经评测启用的修改范围、检查和基准分支也通过重新评测启用变更。运行参数仍可在执行配置页面调整。新任务冻结当前生效配置；已有任务的后续轮次沿用首轮快照，不因项目启用或回退改变 Agent、检查或修改范围。


新建项目先保存基准分支、修改范围、检查和运行参数，再从通过的报告启用候选。普通配置保存不接受初始运行时 Agent 绑定。执行配置只返回当前绑定的 Agent 元数据，不返回其他项目的业务版本。没有旧绑定的项目首次启用不创建空白回退记录。


## Go SDK 与 GitLab 接入

公开模块 `github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1` 独立发布，版本 `v0.16.1`，要求 Go 1.24 或更新版本。安装与调用示例见 [SDK README](sdk/go/codingv1/README.md)。SDK 复用 server-go 的连接、context、超时与错误解码，支持项目会话、服务令牌、稳定幂等键和流式沙箱响应；服务端复用公开请求类型和领域数据。Agentic API 的 Coding 评测环境调用使用同一 SDK，保留其领域类型转换。

GitLab 接入使用官方 `gitlab.com/gitlab-org/api/client-go v1.46.0`。分支、MR、评论、版本和 PAT 查询/创建/撤销由 SDK 处理。应用只保留业务类型转换、Draft 标题规则、按请求读取 Secret 和 4 MiB 原始 diff 上限。原始 diff 使用 SDK 的 Request/Do 与有上限的 writer，避免官方 byte-slice 方法读取整个无限制响应。写操作不自动重试；401/403 使用 SDK 错误，404 映射 SDK 的 `ErrNotFound`。凭据更新错误不回显令牌正文。

SDK 位于私有仓库，下载需要当前账号具备该仓库的读取权限。设置 `GOPRIVATE=github.com/FlyTOmeLight/forge-coding-api`，使用 GitHub HTTPS 凭据；SDK tag 为 `sdk/go/codingv1/v0.16.1`。镜像构建通过 BuildKit 的 `github_netrc` secret 挂载 netrc（`machine github.com`、`login x-access-token`、`password <token>`），构建阶段只用于获取发布依赖，不进入源码、镜像层或最终容器。`make image GITHUB_NETRC=/path/to/github.netrc` 使用同一方式。
