# Forge Coding API

企业 Coding Agent 的业务后端，使用 Go / Gin。前端复用 [forge-tenant-web](https://github.com/FlyTOmeLight/forge-tenant-web)。租户、团队和项目权限由 [forge-tenant-api](https://github.com/FlyTOmeLight/forge-tenant-api) 管理。模型调用和持久化执行由 [agentic-kit-api](https://github.com/FlyTOmeLight/agentic-kit-api) 与 [agentic-kit-worker](https://github.com/FlyTOmeLight/agentic-kit-worker) 承担。

研发任务包含负责人、指定审查者、验收条件、分支、MR、人工介入和多轮执行。每轮保存配置快照并关联一个 Agentic `runtime_id`。执行完成不会直接完成研发任务；开放任务在同步到 MR 合入事实时完成。

## 目录

- `cmd/coding/`：API、dispatcher、migrate 入口。
- `cmd/import-tenant/`：停写后将旧租户库的任务表导入空 Coding 数据库。
- `internal/coding/`：任务业务、HTTP 接口、租户授权、执行提交和结果验证。
- `internal/remote/`：GitLab 仓库与 MR 协议。
- `internal/config/`、`configs/`：配置加载与配置样例。
- `tools/`：项目专用的仓库读取、编辑、检查和交付工具。这里不调用模型，也不领取执行队列。
- `charts/`：API/dispatcher、数据库和项目工具环境的 Helm Chart。

## 运行与部署

配置项见 [configs/env.example](configs/env.example)。凭据由 Kubernetes Secret 注入，不提交到仓库。`TOOLS_PROJECTS_JSON` 将 GitLab 数字项目 ID 映射到该项目的工具 Service；每个工具部署同时配置对应的 `GITLAB_PROJECT_ID` 和项目访问令牌。未配置工具环境的项目不能新建自动执行任务。

```sh
go build -o bin/coding ./cmd/coding
./bin/coding migrate
./bin/coding api
# 独立进程
./bin/coding dispatcher

docker build -t forge-coding-api:dev .
docker build -f tools/Dockerfile -t forge-coding-tools:dev .
helm upgrade --install forge-coding charts/forge-coding -n forge-tenant-split \
  --set image.repository=forge-coding-api,existingSecret=forge-coding-env,database.existingSecret=forge-coding-db
helm upgrade --install forge-coding-tools charts/forge-coding-tools -n forge-tenant-split \
  --set existingSecret=forge-coding-tools-env
```

先部署 Agentic API/Worker 各自仓库中的 Helm Chart。API 的 `API_TOKENS_JSON` 与 `CONFIG_OWNERS` 配置 Coding 服务身份；Worker 的 `CODING_TOOLS_TOKEN` 对应工具部署的 `TOOLS_TOKEN`。Coding 通过 HTTP 访问租户与运行时，不共享它们的数据库。Agentic API 与 Worker 继续共享运行时数据库。

当前工具环境用于明确授权的可信项目。每个项目独立部署，每轮使用独立目录；这不是运行不可信仓库代码的安全沙箱。项目检查命令最多执行 60 秒，初始读取范围限 12 个文件 / 96 KiB。工具执行副作用按 `Unsafe` 处理；结果不确定时不会自动重复推送。GitLab 已收到的在途操作不能承诺撤回。

## 验证

```sh
TEST_DATABASE_URL=postgres://... go test -race -count=1 ./...
go vet ./...
python3 -m unittest discover -s tools -p 'test_*.py'
helm lint charts/forge-coding --set existingSecret=test,database.existingSecret=test
helm lint charts/forge-coding-tools --set existingSecret=test
```

缺少 `TEST_DATABASE_URL` 时数据库用例会跳过，不代表集成验证通过。真实模型试点、迁移与控制验证见 [docs/verification.md](docs/verification.md)。业务契约见 [docs/coding-tasks.md](docs/coding-tasks.md)，迁移步骤见 [docs/migration.md](docs/migration.md)。
