# 研发任务与执行契约

浏览器继续使用 `/api/v1/tenants/{tenant}/projects/{project}/tasks` 和 `coding-settings`。同源网关将这些路径转发到 Coding API。所有浏览器请求先通过租户服务的 `/internal/v1/session/check` 验证会话和写请求的 Origin / CSRF，再检查有效项目权限。平台运营身份不隐含代码权限。

任务规则从原 Forge 后端迁入：负责人转交、指定审查者、版本化更新、人工介入、取消、继续、修改要求排队、配置快照、GitLab 行级评论同步、检查结果与人工认可分离。同步评论不会自动发起执行；MR 重开或重新授权不会自动继续任务。

## 业务轮次与运行时

`coding_runs` 保存业务轮次、不可变配置、不可变提交快照及 `runtime_id`。dispatcher 将快照提交到 Agentic `POST /internal/v1/executions`，幂等键为业务轮次 ID。网络失败后使用同一快照重试，避免重复执行。运行时记录冻结 tenant/project/actor/task/round 上下文，只有受信任配置身份能使用该入口。

Agentic Worker 独占执行领取、执行租约、步骤恢复与模型决策。原 `/internal/v1/tasks/claim` 已删除。Coding dispatcher 只负责业务提交、权限收紧和状态对账，不另建执行租约。

模型使用三个工具：`prepare` 读取仓库规则和基线检查；`edit_and_check` 应用范围内的局部修改并运行配置检查；`deliver` 再次检查、提交代码、条件推送和创建 Draft MR。工具参数无法更换轮次或项目。Worker 注入真实 runtime ID 和 epoch，Coding 通过运行时验证其当前执行权。

工具开始调用 `/internal/v1/rounds/{round}/start`；写入与检查前调用 `/internal/v1/tasks/{task}/gate`；结果由 `/internal/v1/tasks/{task}/finish` 提交。服务凭据只在后端。业务控制版本、当前项目写权限、运行时执行权都必须有效。交付再次核对 GitLab 当前 commit、分支和 MR 状态，拒绝迟到结果。

## 停止与审查

人工介入在任务事务中先关闭自动写入资格。dispatcher 取消运行时；工具监视执行资格并终止活动进程组，确认本地进程停止后报告 Stopped。租约过期先停止旧执行者；新 Worker 通过更高 epoch 验证后可接续已保存步骤。仅取消运行时或租约过期不等于业务任务结束或外部进程已停止。未收到确认时保留“停止未确认”，禁止恢复。历史迁入的未知停止状态原样保留。

dispatcher 对活动轮次持续查询权限，每次工具操作也重新检查。授权服务不可用时敏感操作失败关闭。当前控制台轮询业务任务，没有把运行时 SSE 直接暴露给浏览器。运行时自带的 SSE 仍由其服务身份保护。

自动检查 Passed 只表示交付版本检查通过。Agent 判断、人工审查和任务完成分别记录。工具交付保存模型的 Met / Unmet / NeedsHuman 建议；缺少判断时为 NeedsHuman。模型判断不作为人工验收。任务完成依据显式 GitLab 同步发现的 MR 合入事实。

工具工作目录不包含其他项目的仓库；检查子进程环境不注入服务令牌。当前部署面向可信项目，不能据此声称具备恶意代码隔离。工具 Pod 重启或外部操作结果不明时，需人工核对；不会假装已停止、自动重跑 Git 写入或清理未交付目录。
