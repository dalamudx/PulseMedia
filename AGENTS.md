# PulseMedia 开发约定

## 设计来源

- 唯一架构规范为 `docs/architecture.md`；任务、阶段与进度唯一来源为 `docs/development-plan.md`。开始任务前读取相关章节、任务依赖及当前实现，不依据历史草稿或假设编辑。
- 本文只约束开发行为，不重复产品/数据/可靠性/任务台账。架构变化更新规范原位置，计划与进度更新对应文档，不新增平行架构草案。
- API 字段维护 `api/openapi.yaml`，SQL 维护迁移与 sqlc 查询；当前这些属于待开发产物，不虚报已有实现。

## 必须遵守

1. 按架构依赖 DAG 开发：domain 为纯规则；usecase 依赖 domain/ports；store、adapter、platform 实现合同；app 唯一组装。HTTP 不写 SQL，不在 domain 导入其它 internal 包。
2. 每个业务边界使用窄 UoW，事务不暴露 pgx/sqlc 类型，不用 context 隐藏事务。业务、占位、job、事件、成功审计与幂等结果同事务；网络/文件 I/O 在外。
3. PostgreSQL 18 是唯一数据库。原生 UUID、TIMESTAMPTZ、JSONB、BYTEA；真实 PG 验证锁与事务，迁移只追加，生产 DDL 与 DML 凭据分开。
4. 质量属性是事实，rank 属于策略版本；文件多单元映射；拥有按目标根计算。下载前原子占位，unknown 不自动释放；失联/部分扫描不制造文件缺失。
5. 任务与投递写回校验 owner/token/state/有效租约。逻辑超时不释放仍在执行的物理独占；R3 先观察、无法确认转人工，不盲目重放。
6. 外部错误合同定义在 ports；区分失败、无结果及副作用结果不明。用例不导入 adapter；业务权限包含对象范围，不能用 Token scope 提权。
7. 依赖显式构造；Clock/ID 等注入；context 为阻塞操作首参数且不存入 struct。禁止全局单例、init 注册、反射总线和无 owner/取消/等待的业务 go func。
8. 工具链以 `.mise.toml` 为唯一版本来源；保留完整性校验，Go/前端依赖使用各自锁文件。生成代码提交并检查漂移。
9. 变更带最小充分测试与直接行为验证；涉及队列/占位/恢复需 PG 并发或故障测试，构建成功不等于完成。
10. 凭据、DSN、passkey、Cookie、下载链接不进入日志/响应/缓存明文。文件操作限配置根，防符号链接与路径逃逸；不读取或覆盖用户保护的 `.env`/密钥。

V1 不实现插件、Agent、Hook 链、工具目录或动态表单引擎；不以接口预留为由搭建空运行时。按 `docs/development-plan.md` 的 M0–M4 切片实施，完成后更新任务、阶段统计与实际验收证据。
