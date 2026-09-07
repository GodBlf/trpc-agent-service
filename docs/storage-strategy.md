# 数据同步与多后端策略

## 端口划分

平台把数据能力拆为 Control Plane Store、Session Store、Memory Store、Knowledge Store、Artifact Store 与 Governance/Audit Store。Control Plane 同时保存 Governance Policy 与 Backend Selection；确认、预算计数、Audit、Trace 等治理运行态仍由 Governance/Audit Store 管理。`DataStore` 只是当前租户 Storage Router 组合 Session 和 Memory 的便利接口，不表示这些数据必须落在同一产品。所有端口方法都要求 Tenant 作用域，Backend Selection 由服务端目录解析，不能接受客户端提交任意 DSN。

| 存储类型 | 适合数据 | 一致性 | 当前状态 |
| --- | --- | --- | --- |
| PostgreSQL | 控制面、Session Event、Lease、Memory、Artifact/Knowledge 元数据 | 事务强一致，支持 fencing | 已实现参考适配器，Compose 默认 |
| SQLite | 单节点控制面、Session/Memory 开发数据 | 单进程/单文件强一致 | 已实现开发适配器，不用于多 Gateway |
| Redis | 热 Session/Memory、短期去重、限流计数 | 单 key 原子，持久性取决于配置 | 已实现 Session/Memory 参考适配器 |
| InMemory | 单元测试、fixture | 仅进程内 | 已实现，仅测试 |
| Qdrant/Milvus | Knowledge/Memory 向量索引 | 最终一致派生索引 | 仅设计适配边界 |
| S3 | Artifact 大对象、附件、知识源文件 | 对象写强一致，元数据发布需协调 | 仅设计适配边界 |

## 写入顺序与同步

同一 Session 的写入顺序是：获得 Lease，持久化 input，写 started，执行 Runner，追加 delta/completed 或失败事件，发布 Artifact，写唯一运行终态，最后释放 Lease。每一步使用 request_id 派生的幂等 key。State 和 Summary 从事件 sequence 1 开始连续投影，不允许跳过；投影失败保留 checkpoint，恢复后从下一条继续。

Memory 的权威写先进入 SQL/Redis，再发布索引任务。成功执行会在 IM 回复前更新 `latest_agent_reply`，PostgreSQL 事务同时校验 Session 当前 fencing token；失败时本次执行不能报告成功。读取 Agent 上下文时先读权威 Memory；向量召回用于扩展候选，不得覆盖权威值。Knowledge 同样先提交来源和内容引用，再异步分块、embedding、upsert 向量，最后推进 index checkpoint。向量服务中断时状态为 retry_pending，指数退避并限制最大并发；删除或重建索引不影响权威内容。

Artifact 采用内容与元数据两阶段发布。S3 设计先把内容写到带 request_id 的临时 key，校验 checksum 后在 SQL 事务发布 metadata，成功后再把对象标记为可读。内容成功而 SQL 失败会留下可扫描的临时对象；SQL 成功而对象不可读时将 status 改为 recovery_required，API 不返回虚假成功。当前参考实现的内容是已持久化 Session Event，因而 metadata 引用可以通过事件事实源验证。

## 多节点与故障

PostgreSQL Control Plane 每次读操作刷新 revision，不依赖进程本地缓存作为正确性来源。共享范围包括 Tenant、Agent App、Deployment/Version、Channel Binding、Backend Selection、Governance Policy 和配置幂等状态。并发配置写使用 revision 检查；冲突请求失败并由上层按同一幂等 key 重试，不能覆盖另一个 Gateway 已提交的数据。数据库不可用时返回 `control_plane_unavailable`，不退回 InMemory 或旧快照。确认、执行中 Tool、预算计数、Audit 与 Trace 尚未实现多 Gateway 共享事务存储，Compose 将内部 Governance API 固定到 Gateway A；这是生产高可用扩展限制，不作为已完成能力声明。

Session Lease 使用数据库时间判断 expires_at。续租失败或 token 不再匹配时关闭 Lost channel，Gateway 取消 Runner；即使旧 goroutine 没有及时结束，Session Event、执行期 Memory 和 Artifact 写入也会在同一事务中比较当前最高 token 并拒绝旧 token。租约过期但尚未被重新授予时，原 token 可以完成有界收尾；新 owner 接管后会以当前 token 关闭事实源中遗留的未终结请求。不同 Session 使用不同 lease row，可以水平并行。sticky session 只能作为性能优化，不能承担正确性。

Redis 短暂不可用时 API 返回 `storage_unavailable`，不把本地缓存当作提交成功。PostgreSQL 超时与调用者取消分别映射为稳定公开错误。IM 重试沿用 provider message ID；已存在同内容 input 或终态直接返回已有状态，内容不一致则返回 idempotency conflict。

## 迁移

Redis 到 SQL 或 SQLite 到 PostgreSQL 使用 forward-only Migration Job：冻结目标 Tenant 的 backend 切换，按 Session 列表和 sequence 分批复制 Event，再复制 Memory；每批保存 Migration Checkpoint 和 checksum。dry-run 比较数量与摘要，不切换流量。正式迁移完成后双读校验源/目标，原子更新 Backend Selection，再保留源只读观察窗口。失败从 checkpoint 恢复，幂等 key 防止重复事件。Control Plane schema 由独立 `control-migrate` 执行 expand-contract：先加兼容字段/表，升级全部 Gateway，最后在后续维护窗口收缩；Gateway 只验证版本。

### 本地向量库到远端向量库

向量索引是 SQL/对象存储中 Knowledge 与 Memory 权威内容的可重建派生物，迁移不直接改变权威数据。Migration Job 以 Tenant、Agent App、索引类型和 index generation 为作用域；启动时固定 embedding model、向量维度、切片版本和源索引 watermark，并在远端创建隔离的新 generation。每条向量使用 `(tenant_id, app_id, source_type, source_id, chunk_id, embedding_version)` 作为确定性 ID，批量 upsert 可幂等重试；checkpoint 记录源 cursor、watermark、成功/失败数量和批次 checksum，进程中断后从最后完整批次恢复。Tenant ID 必须同时进入远端 collection/partition 路由与 metadata filter，迁移任务不能接受跨 Tenant 的源或目标范围。

初始回填优先从权威内容重新切片并生成 embedding；只有 embedding model、维度和切片版本完全一致时，才允许从本地向量库导出并复制已有向量。watermark 之后的新增、更新和删除由 SQL outbox 记录，迁移期间继续写源索引并异步双写远端 generation；删除以 tombstone 同步，避免旧 chunk 在目标残留。若本地向量库无法导出，则从权威内容重建远端索引，不把本地索引可用性作为迁移前提。

回填结束后先追平 outbox，再校验源/目标有效 ID 数量、缺失/多余 ID、metadata checksum、向量维度与 embedding 版本；对固定查询集执行 shadow read，比较过滤条件、Top-K 覆盖率和延迟，任一阈值不达标都不切流。校验通过后在 Control Plane 事务中把该 Tenant/App 的 active index generation 原子切到远端，Gateway 只读取已发布 generation。源索引进入只读观察窗口，回滚只需恢复 generation 指针并继续回放 outbox；观察期结束且审计确认后再异步删除源。失败任务保留 checkpoint、新 generation 和错误摘要，可限速重试或清理未发布 generation，不影响当前在线索引。

## 成本与运维取舍

PostgreSQL 提供最清晰的事务和审计语义，代价是连接数、热点 Session row 与维护成本；连接池和按 Session 分区可以缓解。Redis 延迟低但跨多 key 事务与持久恢复更复杂，适合作热数据而非唯一审计事实源。向量库改善语义检索但不可作为 Memory 权威来源。对象存储单价低、适合大内容，但必须处理跨 SQL 的发布与孤儿回收。

备份以 PostgreSQL PITR 和对象版本为主，Redis 按缓存/权威角色分别配置。监控至少覆盖控制面 revision 冲突、Lease 获取/续租失败、stale fencing 拒绝、投影落后量、索引 retry 队列、Artifact recovery_required、数据库延迟与 Tenant 维度错误率。恢复演练必须用注入的 request_id 检查精确终态，不能用“系统里出现过一次成功”代替目标请求断言。
