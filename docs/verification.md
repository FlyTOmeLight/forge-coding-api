## 2026-10-14：按业务职责与表名完成布局重构

上一轮仅完成框架装配与生命周期，此轮补齐业务职责分层。`internal/app/coding` 已删除，拆为 task、project、evaluation、platform、access。Controller 负责请求绑定、响应、代理与 Swagger，Service 负责业务规则和事务；access 的 HTTP 鉴权放 middleware.go。生产 app 不含 SQL，业务 Service 不写响应。

七张业务表分别在 `coding_tasks.go`、`coding_runs.go`、`coding_feedback.go`、`coding_events.go`、`coding_project_settings.go`、`coding_project_releases.go`、`coding_evaluation_environments.go` 中维护模型、具名查询与行映射。删除通用 QueryMaps，Task 持久化模型与 SDK 类型独立；列表查询补齐稳定排序。

租户、Agentic、AIO、模型及 GitLab 的外部协议各归对应 Client。任务 dispatcher 与评测过期回收由公开 service 统一启动、取消和等待。SDK 仍属于根模块，按整仓版本发布。

原 46 个业务测试全部保留，跨业务 HTTP 测试迁至 task 测试包并组合真实业务模块；新增鉴权先于绑定且单次查询权限的回归。隔离 PostgreSQL 的 `make integration build`（全量 race、vet、两个入口构建）、Helm lint、Docker 构建与职责检查通过。三个子代理分别完成任务、项目、评测模块及独立审查。未执行真实 GitLab MR 写入、模型调用或业务集群部署。

## 2026-10-14：MVP 统一仓库版本

按当前 MVP 发布约定，SDK 删除独立 `go.mod` / `go.sum`，归入根模块 `github.com/FlyTOmeLight/forge-coding-api`，跟随整仓 `v0.17.0` 发布。SDK 的包路径保持 `github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1`。旧调用方需删除独立 SDK 模块依赖后改用根模块版本，避免导入歧义；后续需要独立发布时再拆分模块。

隔离 PostgreSQL 下的 `make integration build` 通过，统一包含后端与 SDK 的竞态测试、vet 和两个入口构建。Docker 与 CI 改为编译仓库内的 SDK，不再配置本仓库 SDK 下载凭据。正式 Dockerfile 的无凭据镜像构建通过。

## 2026-10-14：server-go v0.9.0 重构与 SDK v0.17.0

公开 `service/` 提供共享资源构造，API 改用 `app.Run` 和一次类型化配置解码。HTTP 使用框架 core 类型及 context logger；dispatcher 保持独立、无 HTTP，退出先取消并等待所属任务。现有路由、业务错误 envelope、严格 JSON、任务与交付协议保留。

已验证：

- 独立 PostgreSQL 17.6 容器中的根模块完整 `go test -race -count=1 ./...`；SDK 独立 race 测试；两个模块的 `go vet`。
- 新生命周期测试验证装配失败清理、真实 HTTP 启动/取消、初始化 context 结束后仍可运行，以及组合服务停止后不关闭共享连接。
- 两个程序入口构建、Helm lint、Docker 镜像构建。实际 `migrate` 在没有 Kubernetes 配置时完成数据库迁移。
- 测试镜像 API 的 healthz、readyz、401、404 请求 ID、405 和尾斜杠契约；API 与 dispatcher 收到 SIGTERM 后均退出码 0。
- SDK 覆盖 4/32 MiB 响应上限、业务错误解码、不重试变更请求、请求身份隔离和流式取消。

SDK v0.17.0 已发布，根模块已下载并引用该正式版本。SDK 适配 Resty v3.0.0-rc.4，最低 Go 版本升至 1.27。最终隔离数据库下的 `make integration build` 使用 `GOWORK=off` 和正式依赖通过，根模块和 SDK 均无本地替换；正式 Dockerfile 使用发布依赖的镜像构建通过。临时联调 modfile 已删除。

验证只使用本次隔离数据库和假 Kubernetes 连接配置，未连接业务集群、调用模型或执行真实 GitLab MR 用例。测试容器、网络和镜像已清理。

## 2026-10-13 GitLab 官方 SDK 与 Coding Go SDK

GitLab 分支、MR、评论、版本和 PAT 接入改用官方 `client-go v1.46.0`，删除自定义 HTTP 请求与通用响应解析。业务只保留类型转换、按请求读取 Secret、Draft 规则及 4 MiB 原始 diff 上限；404 映射 SDK 的 ErrNotFound，写操作不自动重试，PAT 错误不回显令牌。

新增独立模块 `github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1 v0.16.1`，最低 Go 1.24。后端与 Agentic API 正式引用发布版本，关闭 workspace、没有本地 replace。SDK 复用 server-go 传输，覆盖所有业务及内部工具路由，支持请求 context、两种实际错误格式、幂等键、独立会话身份和沙箱流的读取与关闭。公开请求类型和领域数据与 Controller、Table 共享。

SDK 自身的竞态/传输测试通过；后端通过 SDK 调用真实 Controller 与隔离 PostgreSQL，验证任务创建/重试、项目隔离及内部服务鉴权。后端全量竞态、vet、两个入口构建，Agentic API 的隔离 PostgreSQL/Redis 全量竞态、vet 和两个入口构建均通过。正式版本依赖完成两份 Docker 镜像构建；私有模块凭据使用 BuildKit secret，未写入源码或镜像。GitLab 分页、取消、禁止写重试、Secret 热更新和 diff 限额回归通过；真实 MR 写入验收未执行。

发布后 Coding API、dispatcher 和接入 SDK 的 Agentic API 均 Ready；健康、数据库就绪接口返回 200，官方 GitLab SDK 成功读取线上 PAT 元数据。发布镜像为 `forge-coding-api:0.16.1-go-sdks` 和 `agentic-kit-api:0.16.1-coding-sdk`。

## 2026-10-13 server-go 重构与发版

Coding API 与独立 dispatcher 迁移到正式依赖 server-go v0.3.1，按 `cmd/server`、`internal/service`、`internal/app/coding`、`internal/table`、`internal/client`、`conf/server` 与 `deploy` 组织。Bootstrap 显式管理资源，HTTPHost 先排空请求再停止服务；dispatcher 取消后等待所属后台任务结束。业务错误 envelope 和现有路由保留，JSON 通过框架绑定并拒绝未知字段、多文档；64 KiB 上限返回 413。

在独立 PostgreSQL 17.6 容器中执行全部数据库用例与 `GOWORK=off go test -race -count=1 ./...`，以及 `go vet`、两个入口构建、Helm lint 和实际 Docker 镜像构建，全部通过。验证覆盖框架配置、启动取消、真实 HTTP 请求、数据库就绪探针、业务错误、404 请求 ID、资源关闭和 dispatcher 退出。现有任务、权限、交付、评测环境及候选启用/回退回归一并通过。真实 GitLab 的两个可选 MR 验收用例本次未配置；未运行真实模型试点。

发布镜像为 `forge-coding-api:0.16.0-server-go`，API/dispatcher 均 Ready。八项真实 HTTP 冒烟验证通过：健康与数据库就绪检查、业务及服务鉴权、404/405、尾斜杠和业务参数校验。独立 API 冒烟进程收到 SIGTERM 后退出码为 0；GitLab 凭据状态命令正常。脱敏测试与部署证据保存在本地 `.local/server-go/`。

## 2026-10-05 精确编辑失败不再中断整轮

## 2026-10-08：Eino + AIO 替换完成

当前部署为 Agentic API `0.11.0-aio`、Worker `0.11.1-aio`、Coding API/dispatcher `0.11.2-aio`。项目绑定 Coding Agent 第 4 版，使用 Eino 原生 filesystem 工具和 AIO Go SDK。`tools/server.py`、旧工具 Chart、线上 Deployment/Service 及专用 Secret 已删除。旧工作区 PVC 作为历史产物保留。

独立真实模型任务 `task_2ec6697abd7cc5c9b94856e2` 完成新增 multiply、浮点测试、文档修正和双负数测试。平台准备仓库、冻结候选、独立验收、推送并创建 Draft MR，后续两轮反馈更新同一个 [MR !10](http://127.0.0.1:18080/coding-pilot-muu03pvs/coding-fixture/-/merge_requests/10)。最终提交 `3ccedb7bf09d54e70fd5ab0cb4ce1589f3b08b66`，8 个测试通过，只修改两个 src 文件。MR 保持 Draft，任务为 Review。

Worker 在保存工具结果后被强杀。运行 `4d66d4ddd6bc185f2c5e9aeb7cb6fc01` 由 epoch 2 接管，新 AIO 实例复用原持久卷，已保存工具结果未重复提交，运行最终 Completed。反馈轮次还实际验证了缺少 finish.summary 时返回参数纠错，模型修正后继续完成。

API、Worker、Coding 均通过隔离 PostgreSQL/Redis 的 make integration（含 race 和 vet）。Worker 包含跨进程恢复测试和真实 AIO SDK/原生工具测试。实际交付发现并修复了 Python 字节码缓存混入候选、发布失败且远端分支不存在时未收尾，以及 Kubernetes 挂载权限触发内容误报的问题。文件校验保留内容与 Git 可执行位检查。

详细证据保存在设计工作区 `.local/aio-validation/verification.json`、各轮 result.json 和 integration.log。临时验证命名空间与数据库容器已清理；线上历史轮次与 PVC 保留供审查，PVC 自动回收尚未实现。

任务 `task_21b111a3224bb5de4c4bf06e` 的后续轮次已按评论写入两处注释。模型随后将 before 中的缩进误写为五个空格，当前源码为四个空格，精确替换被拒绝。原 HTTP 409 被 Worker 作为 Unsafe 结果不明处理，整轮停止，错误详情也被丢弃。

确定未写入的匹配失败现在返回 EDIT_MATCH_FAILED 工具结果，包含路径、匹配次数和重新读取要求。真实失败仍保持错误路径；错误详情保留工具名与结构化原因。新增测试覆盖文件未改动、重新读取后可正确编辑、真实 HTTP 响应及重复操作结果一致。八项工具测试通过；独立 PostgreSQL 下 Worker 的完整竞态、强杀恢复与 go vet 检查通过。

使用这轮真实模型消息和失败编辑参数，在临时仓库及受控交付接口重放，本机 qwen3.8:27b-mlx 收到匹配错误后调用 run_checks、deliver，三个检查通过。未修改真实 GitLab MR。重放证据位于设计工作区 `.local/deployment-20261005/edit-error-replay-result.json`。Worker 与工具服务已部署为 `0.8.2-edit-feedback`；用户任务保持人工控制，原工作目录中的注释修改保留。

## 2026-10-05 正式反馈上下文与恢复

用户任务 `task_21b111a3224bb5de4c4bf06e` 的上一轮只收到“就按评论来就行”，缺少 GitLab 评论正文。模型未修改代码，deliver 拒绝空改动后运行时进入 NeedsReview；dispatcher 未处理该状态，直到十分钟期限到期才停止。新反馈因任务处于 Human 状态而正确入队，但页面错误地显示“等待本轮结束”。

修复后，执行快照带入任务与非系统 GitLab 评论上下文；已有 MR 的无改动交付保存当前提交的检查，不创建空提交或推送。NeedsReview 立即请求停止并保留错误。页面区分排队原因，新增显式恢复并消费已保存反馈的操作，继续校验反馈提交和停止确认。

独立 PostgreSQL 下完整 Go race 测试与 go vet 通过；六项 Python 工具测试通过。 Vue 类型检查、构建和格式检查通过。已部署为 `0.8.1-feedback-context`。真实登录后的反馈界面回归使用模拟动作接口，不启动用户任务；同一页面用例在修复前失败、修复后通过。用户任务仍为 Human，反馈保持 Pending。新增用例先复现缺少评论、空改动交付失败、NeedsReview 未对账和恢复不消费反馈，修复后通过。

## 2026-10-05 通用 Agent 发布与 Coding 应用接入

Coding 后端不再拼接提示词和工具定义，也不再由运行时为每轮创建临时 Agent。项目绑定明确的 Agent ID / 发布版本；运行时从发布版本生成不可变快照。同一应用任务跨轮复用会话，Writing 与 Coding 通过同一接口和 Worker 执行。Agent 定义位于 agents/coding.json 与 Agentic API 的 configs/writing-agent.example.json，通过平台标准接口发布。

仓库工具拆为 checkout、list_files、search、read_file、edit_file、run_checks、get_diff、deliver。初始化保证基线检查，模型按需要定位、阅读、修改、验证。模型不会一次取得全部文件，去除了 12 文件 / 96 KiB 的项目限制。读取支持分页；写入仍按项目范围与当前执行权校验。原任务审查、人工停止和交付规则保留。

实际执行的验证：

- API、Worker、Coding 三个 Go 仓库在独立 PostgreSQL/Redis 环境通过必要的竞态测试与 go vet。Worker 原有强杀进程接管测试通过。
- 新测试覆盖引用已发布版本、拒绝内联 Agent 定义、发布后的幂等重试、跨轮历史、并发轮次互斥、跨项目与 owner 隔离，以及应用不再创建临时 Agent。
- Coding 测试覆盖正确版本引用、项目工具绑定与已有任务禁止静默更换 Agent。Python 五项测试通过，包含大于原项目限制的仓库搜索、失败检查、实际编辑、检查通过和路径逃逸拒绝。
- Vue 类型检查、构建和格式检查通过。Chrome 原型 13 项流程检查通过，增加 Agent/版本选择和模型只读验证；修正表单标签后可按标签操作控件。
- 本机真实 Ollama qwen3.8:27b-mlx：Writing 连续两轮完成并保留相同会话与项目名称；Coding 从初始化的失败堆栈定位文件，读取实现和测试、编辑、检查与交付成功。检查绑定临时 Git 提交 `d8b33da71124a9a9f91dc7a9e156acf46c4e5ee5`。失败基线与通过结果分别保存。

真实模型验收使用临时本地 Git remote 和受控业务授权接口，未创建真实 GitLab MR，不计入真实 bug 试点。原始结果位于本地 `.local/published-agent/result.json`。已将 Agentic API、Worker、Coding API/dispatcher、仓库工具和 Web 升级为 `0.8.0-published-agents`，部署在 `forge-tenant-split`。体验入口为 `http://127.0.0.1:8483`。`Coding Agent 联调企业` 的 `Coding 链路验收` 项目已绑定 `Coding Agent v1`（Agent ID `0e0b482e47f6bd2fa3f63813081a70ad`），使用 qwen3.8:27b-mlx。真实 Chrome 登录、保存绑定、接入检查和页面 Agent/版本字段验证通过；集群内模型接口返回 200，组件全部 Ready。本次部署未新建或恢复 Coding 任务，也未修改 GitLab 分支或 MR。部署证据位于设计工作区 `.local/deployment-20261005/verification.json`。同步升级及显式绑定步骤见 coding-tasks.md。

原系统的 Sandbox Manager、Skill、知识、Memory 和上下文压缩仍需沿原接口迁移；本轮没有把可信项目工具环境称为通用沙箱。

## 2026-10-05 GitLab 状态自动同步

原问题：浏览器轮询只读取 Forge 数据，MR 合并/关闭需要手动调用同步接口。后台 dispatcher 现在独立每 5 秒读取未结束任务的 MR 与评论，并复用手动同步的状态规则。相同事实不重复增加版本；单项失败下轮重试；同步不阻塞执行租约对账。MR 合并后的执行停止或模型失败保留任务的 Completed 状态。

验证：真实 PostgreSQL 的全套 `go test -race -count=1 ./...`、`go vet ./...`、Python 工具测试与 Helm lint 通过。覆盖后台自动完成、关闭、取消保持、无 MR 跳过、提交变化清除认可、评论幂等、故障隔离与重试、执行停止期间保留已完成。

真实 GitLab 验收 `TestGitLabMergeAutomaticallyCompletesTask` 使用项目 17 内独立源/目标分支；临时 MR !8 合并后 4.369 秒自动 Completed，并写入 `gitlab-sync` 合并事件，全程没有调用 `/sync`。临时 MR、两个分支和测试 schema 已清理；默认分支及用户体验 MR !5 不变。该测试与 `TestTaskApprovalWithGitLab` 使用同一组 TEST_GITLAB 环境参数。

## 2026-10-05 审查认可同步 GitLab Ready

原问题：执行器固定创建 Draft MR，而 `Approve` 只写本地 `approval_head`，GitLab 草稿状态一直阻止合并。现由指定审查者认可当前提交时取消 Draft；新一轮修改开始前恢复 Draft。校验开放状态、源/目标分支和 GitLab 实际提交；更新失败、执行未停止或提交已变化时不保存认可、不错误启动下一轮。

验证：真实 PostgreSQL `go test -race -count=1 ./...`、`go vet ./...`、Python 工具测试及 Helm lint 通过。回归覆盖重复认可、远端提交变化、关闭、无 MR、执行中、停止未确认、非审查者、GitLab 更新失败及状态未生效。

额外执行 `TestTaskApprovalWithGitLab`，明确指定本地测试项目 17，临时 MR !4 验证 Draft → Ready → Draft；随后删除临时 MR 和分支，目标分支不变。复现需提供 `TEST_DATABASE_URL`、`TEST_GITLAB_URL`、`TEST_GITLAB_TOKEN`、`TEST_GITLAB_PROJECT_ID`，使用隔离数据库 schema；不得指向未经授权的项目。

## 2026-10-05：未发布分支的 GitLab 链接修复

用户报告任务 `task_95cfed62c2056e901ca33f25` 的工作分支链接返回 404。真实 GitLab API 确认项目存在、目标分支不存在。此前任务详情仅凭预定分支名生成链接，误将未发布的分支当成可访问产物。

任务详情新增 `gitlabTarget`，区分 MR、已发布分支和项目。无 MR 时查询真实分支；404 返回项目入口和未发布说明，其他查询失败明确报错。已有 MR 不依赖分支查询。

- 旧实现的回归测试失败；修复后真实 PostgreSQL 全部 Go race 测试、Go vet、Helm lint 通过，数据库测试无跳过。
- Vue 类型检查、构建和 Prettier 检查通过。
- 真实浏览器使用上述任务，确认不存在“打开工作分支”链接，展示“打开 GitLab 项目”和未发布说明；点击后到达真实项目。已有 MR !3 的入口也通过回归。两种场景均只在 Forge 提交一次密码，GitLab 身份一致。
- Coding API 与 Web 已部署 `0.7.1-delivery-links` 到 `forge-tenant-split`，未恢复任务执行或修改 GitLab 代码。

复现：前端仓库 `SSO_TASK_ID=task_95cfed62c2056e901ca33f25 SSO_EXPECT_TARGET=project npm run test:sso`；已有 MR 使用 `npm run test:sso`。脱敏结果与截图见前端 `.local/sso-evidence/result-project.json`、`result-mergeRequest.json`、`forge-project.png`。

# 验证记录：Coding 独立后端与 Agentic Kit

验证时间：2026-10-05。环境：OrbStack Kubernetes，namespace `forge-tenant-split`，应用均通过 Helm 部署。

- 旧租户库迁入 9 个任务、9 个轮次、41 条事件、3 份项目配置。原 ID 与 MR 关联保留。历史中 1 个“停止未确认”仍保持该状态，没有自动恢复。
- 四个 Go 后端在真实 PostgreSQL / Redis 下执行竞态与集成测试。Coding 覆盖任务控制、租户项目隔离、撤权后不自动恢复、旧执行权拒绝、提交快照重试与重复迁移。Agentic Worker 覆盖跨进程故障接管及已完成工具不重复执行；该恢复测试使用受控模型/工具服务。
- 工具测试覆盖路径逃逸拒绝、检查进程不继承令牌、不执行 shell 拼接以及停止后禁止启动命令。
- 真实 OIDC / Casdoor / GitLab CE 浏览器回归 13 项通过，包含团队角色、跨企业隔离、同步 Worker 暂停与恢复、真实 GitLab 登录、CSRF 和撤权事件。公共网关禁止 `/internal/`；服务验收通过独立后端通道。
- Vue 类型检查、构建、格式、独立原型交互和响应式检查通过；npm audit 无已报告漏洞。

## 真实模型交付

独立测试项目预置 `ratio(0,0)` 的错误和失败测试。模型为本机 `qwen3.8:27b-mlx`，由 Agentic Worker 调用，未使用固定答案模拟。

任务：`task_613ff7c5f39400d2f24e1468`。运行时：`9a78b74d3ea33460a7d7fe2167b3bd11`。
交付 Draft MR：`!2`；提交：`f4cbeb45fa430c1151fe1ddec2792f733aeff626`。
补丁只涉及 `src/calc.py`。基线测试失败，交付版本 `python3 -m unittest discover -s src` 通过；检查记录绑定上述 commit。补丁保存在业务轮次输出，可由现有页面下载。模型给出 `Met` 建议，任务状态仍为 Review，未提前标记 Completed，也未合并 MR。

运行中人工介入约 992 ms 收到停止确认；撤销项目权限约 918 ms 收到停止确认。撤权期间任务读取被拒绝，恢复授权后保持人工控制。这是本机两次测试结果，不是服务延迟承诺。

## 真实 Worker 故障接管

在准备工具结果已持久化、后续模型调用进行时强制删除 Worker Pod。替代 Worker 继续同一运行 `b74239c8115725f70935d1f0282c2827`，epoch 从 1 增至 2；准备工具结果只记录 1 次。检查通过并交付 Draft MR !3，任务仍为 Review。该项使用真实本机模型、项目工具与 GitLab。

## 范围

工具环境目前只配置独立联调项目，使用该项目访问令牌。每个项目独立部署、每轮独立目录，面向可信项目；未完成恶意代码隔离、跨租户负载压测或生产高可用验收。GitLab MR 事实仍显式同步，未增加 Webhook。外部操作结果不明时保留人工确认要求，不能把 Worker 取消等同于所有外部副作用撤销。


## AIO 任务级复用验收（2026-10-08）

Coding API 与 dispatcher 部署 forge-coding-api:0.12.0-aio-reuse。用户明确批准平台在 forge-tenant-split 命名空间的 pods/exec 权限；模型 Pod 不挂载 ServiceAccount。

独立合成任务 task_c7f0298f5e0a4583822b48e0 由受信任数据库夹具入队。真实 Eino、AIO 和 GitLab 完成三轮交付，HTTP 任务动作另由完整集成测试验证。每轮分别通过 10、12、14 项测试，检查记录绑定对应交付提交。Task 保持 Review，Draft [MR !11](http://127.0.0.1:18080/coding-pilot-muu03pvs/coding-fixture/-/merge_requests/11) 保持 opened，未合并。最终提交 8d4a554ec3bb30a073f94af24b4e7bb5e263c03d。改动仅包含 src/calc.py 和 src/test_calc.py。

首次与反馈轮使用同一 Pod UID a74d37d7-9f91-4def-bf91-a0e98b494014。后台 PID 655 保持不变，内存计数从 2427 增长到 3726，Python 环境保留。只将该验收任务的更新时间调到 31 分钟前，验证空闲回收删除计算 Pod、保留 PVC。随后恢复轮创建新 Pod 37cee0e5-60a1-4b1f-9228-447df167b560，原代码与 Python 环境可继续运行，Git HEAD 对齐最终交付提交。

隔离 AIO 容器验证暂停期间后台写入停止，恢复后 PID 和内存状态保留。完整 make integration 使用隔离 PostgreSQL 执行，竞态检测与 go vet 通过；Helm lint 与 Docker 构建通过。新增测试覆盖反馈轮复用、旧轮次拒绝、更高 epoch 停止旧计算、人工分支更新、快照卷隔离、RWO 节点调度、交付保留、空闲回收、活动任务不回收及取消确认。

本轮只修改 forge-coding-api 的代码和部署 Chart。验收证据位于 coding-agent-mvp-design/.local/aio-reuse/verification.json。
