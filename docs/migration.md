# 从 Forge 租户库迁移

目标库为空。迁移保留任务、轮次、事件、反馈和项目执行配置的原 ID，以及来源中的停止状态。源表保留作回退副本，升级后的租户服务不再读写这些表。

1. 备份旧表，暂停租户 API/worker 与 Coding API/dispatcher 的写入。停止并确认仍为 Queued/Running 的旧轮次；未知的 Stopping 历史保留。
2. 设置 `SOURCE_DATABASE_URL` 和目标 `DATABASE_URL`，执行 `go run ./cmd/import-tenant`。命令迁移目标结构、锁住源业务表，并在目标事务内导入。非空目标或活动旧轮次会使导入拒绝执行。
3. 核对五张表的数量、原任务 ID 和 MR 关联。恢复新版租户服务、Coding API/dispatcher。
4. 升级前端 Chart，设置 `coding.upstream=http://forge-coding`。旧浏览器接口路径不变。恢复控制台端口转发并验收登录、任务读取和权限。

切换后若已有新业务写入，不可直接回到旧库。必须先停写并处理新增数据，避免形成两份任务事实。
