# PulseMedia

Go 驱动的自托管影视自动化中枢：发现、订阅、下载编排、文件整理和媒体库同步，播放交给 Jellyfin 等媒体服务器。

开发任务、排期与当前进度统一见 **[开发计划与进度](docs/development-plan.md)**。

当前开发源码要求 **PostgreSQL schema145**；公开 `v0.1.0-beta.2` 产物仍绑定 schema126，不能用其验收结果代表当前源码。源码安装需先备份，再以独立迁移凭据执行所有追加迁移；订阅列表已改为分页摘要，外部客户端须按 OpenAPI 翻页，并通过详情或日历读取逐集数据。参见[部署与升级说明](docs/deployment.md)。

## 架构与技术栈

- **后端**: Go 1.26 模块化单体，原生 `net/http` 严格模式（Strict Server），`depguard` 强制依赖分层 DAG
- **数据库**: PostgreSQL 18 原生约束（无 SQLite 兼容层），UUIDv7、TIMESTAMPTZ、JSONB、BYTEA，ACID 窄事务
- **前端**: React 19 + TypeScript 严格模式 + Vite + React Context + Lucide React，单二进制产物 `go:embed` 内嵌
- **可靠性**: 数据库级持久任务认领（`SKIP LOCKED`）、单调租约令牌、Outbox 聚合保序投递、原子占位全有全无、丢响应对账恢复与硬链接崩溃观察恢复

## 文档索引

- [系统架构与实施基线](docs/architecture.md)
  - [整体架构与依赖](docs/architecture.md#architecture)
  - [领域与数据模型](docs/architecture.md#domain)
  - [可靠性与外部恢复](docs/architecture.md#reliability)
  - [数据库运行与备份](docs/architecture.md#operations)
- [开发计划与进度台账](docs/development-plan.md)
- [生产部署与运维指南](docs/deployment.md)
  - [加密备份与隔离恢复](docs/deployment.md#3-数据库备份与恢复)（`mise exec -- task build-backup`）
- [开发约定与贡献规范](AGENTS.md)
- [开源许可证](LICENSE)

## 快速启动

### 1. 本地构建并部署容器

```bash
mise exec -- task build-container -- localhost/pulsemedia:dev
```

镜像包含内嵌前端、离线迁移和加密备份工具、PG18 客户端，固定以 UID/GID 10001 运行。按[部署指南](docs/deployment.md#1-构建并启动容器)配置独立数据库密码、明确连接和卷权限，用镜像内 `pulsemigrate` 完成离线安装后再以 `podman compose` 启动；首次访问进入管理员初始化向导。已有库升级须先准备可验证的加密恢复点。正式Beta归档的下载、校验和加载方法见[部署指南](docs/deployment.md#beta-release)，当前交付与验收状态见开发计划。

### 2. 本地开发环境（一键启动与双热重载）

工具版本统一由 [.mise.toml](.mise.toml) 管理：

```bash
mise trust
mise install
```

#### 一键启动（自动起库 / 自动迁移 / 前后端热重载 / 监听 0.0.0.0）

```bash
mise exec -- task dev
```

一条命令完成以下准备：

1. **自动起库与迁移**：检查本机开发 PostgreSQL 18 并应用迁移；该本机辅助入口需要可用的本地 PostgreSQL 系统账号。已有独立数据库或容器部署按架构/部署指南操作。
2. **开发下载器**：有 Podman/Docker 时按 `.mise.toml` 固定镜像创建本次 qBittorrent/Transmission，管理端口仅绑定 `127.0.0.1`，默认分别为 `8085`/`9091`。在应用设置中添加连接，密码由正式配置接口加密保存；脚本不直接改写配置表。
3. **前端与后端热重载**：默认前端 `http://0.0.0.0:3000`，后端 **`http://0.0.0.0:8088`**；前端端口占用直接失败。可通过 `VITE_HOST`、`VITE_PORT`、`PULSE_HTTP_ADDR` 和 `PULSE_BACKEND_URL` 明确配置；修改后端地址时同时指定代理目标。
4. **退出与失败清理**：Ctrl+C、终止信号或任一热重载进程退出，只停止本次进程组，并按容器ID/归属标签移除本次下载器；不按端口杀进程或删除既有同名容器。数据库和下载文件保留，临时配置与构建输出在 `/data/temp/pulse-dev.*` 清理；归属不符或清理失败会报错并保留该目录供核对。终端不输出数据库连接串或密码。

自定义环境变量放在 `mise exec` 之后，避免被 `.mise.toml` 的开发默认值覆盖，例如：

```bash
mise exec -- env PULSE_HTTP_ADDR=127.0.0.1:18088 PULSE_BACKEND_URL=http://127.0.0.1:18088 task dev
```

开发进程生命周期回归：`mise exec -- task test-dev`；真实固定镜像、随机回环端口与临时下载目录验收：`mise exec -- task test-dev-services`。两入口都校验本次资源清理。集成支持范围见[能力矩阵](docs/architecture.md#integration-capabilities)，各集成复验和真实环境限制见[部署指南](docs/deployment.md#integration-verification)。

#### Podman 真实服务联调环境

```bash
mise exec -- task m4-services-up
# 使用启动输出的目录查看状态或停止，停止后保留数据库和配置：
mise exec -- task m4-services-status -- /data/temp/pulse-m4-services.XXXXXX
mise exec -- task m4-services-check -- /data/temp/pulse-m4-services.XXXXXX
mise exec -- task m4-r3-faults -- /data/temp/pulse-m4-services.XXXXXX
mise exec -- task m4-nfs-preflight
mise exec -- task m4-services-stop -- /data/temp/pulse-m4-services.XXXXXX
```

入口直接调用 `podman compose`，镜像摘要在 `.mise.toml` 锁定；只管理 `pulsemedia-m4` 项目，配置位于独立的 `/data/temp/pulse-m4-services.*` 目录。服务持续运行供联调，停止命令保留数据；不加载仓库 `.env`。

`m4-services-check` 用自建私有种子验证 qBittorrent/Transmission 双向传输，并验证 Jellyfin 的初始化、索引和重复刷新；FFmpeg 版本由 `.mise.toml` 固定。检查结束会清理测试任务及临时文件，保留服务配置、媒体库和凭据。Prowlarr 未配置索引源时，Torznab 子项会明确跳过；该命令不覆盖 NAS 故障或 M4-01 完整验收。

`m4-r3-faults` 为两个真实下载器分别注入七个提交故障边界，验证原任务观察恢复、占位保留和 Add 不重发；数据库使用独立无网络 Podman PG18.6，测试结束销毁。`m4-nfs-preflight` 检查私有客户端挂载条件，返回 77 表示环境阻断；NAS 兼容性与故障验证见 [NFS 验证入口](deploy/integration/nfs.md)。

| 服务 | 本机入口 |
|---|---|
| qBittorrent | `http://127.0.0.1:18085` |
| Transmission | `http://127.0.0.1:19091` |
| Jellyfin | `http://127.0.0.1:18096` |
| Prowlarr / Torznab 管理 | `http://127.0.0.1:19696` |
| PostgreSQL 18 | `127.0.0.1:15432` |

下载器在该隔离网络内免登录，发布端口仅绑定回环地址；Jellyfin 首次进入初始化向导，Prowlarr 需配置授权索引源后才能提供搜索结果。PG 密码在环境的 `secrets/postgres-password` 文件中生成，权限为 `0600`，以 secret 文件挂入容器。下载文件在环境的 `downloads/`，媒体库在 `media/`，PG18 卷挂载至 `/var/lib/postgresql`。服务启动不替代 M4 的授权内容、完整调用链及真实 NAS 故障验收。

## 已知限制与发布范围

- **V1 单实例部署**：采用单应用进程 + PostgreSQL 18，不引入 Redis 或分布式消息队列，不支持多应用实例主主集群。
- **扩展性保留**：V1 专注于核心影视管理闭环，不内置动态插件运行时或 LLM Agent 引擎，保留明确类型化接口边界。
- **存储操作**：同一文件系统首选瞬时硬链接（Hardlink）；跨设备提供带全量 SHA-256 完整性哈希校验与原子暂存刷盘的复制（Copy）与移动（Move）。

## 开源协议

本项目采用 [MIT 许可证](LICENSE) 开源。
