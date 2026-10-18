# PulseMedia 生产部署与运维指南

PulseMedia 采用单二进制轻量微单体架构，内嵌现代化 React 19 前端，专为 PostgreSQL 18 设计。容器镜像默认以非 root 用户安全运行。

## 1. 构建并启动容器

使用项目 `.mise.toml` 锁定的工具构建本地镜像；Podman 仅接收临时目录里的三个二进制和迁移，不把本机配置、密钥或媒体送入构建上下文：

```bash
mise exec -- task build-container -- localhost/pulsemedia:dev
```

默认构建本机架构；显式追加 `amd64` 或 `arm64` 可选择目标。跨架构执行与完整部署验收仍需在对应机器验证。运行基底固定到 `.mise.toml` 中的 PostgreSQL 18 摘要，包含匹配的 `pg_dump` / `pg_restore` 及其库；应用是唯一默认入口，容器不会自行启动 PostgreSQL 服务。此选择优先保证客户端与 TLS 依赖完整，尚未做镜像体积优化。

`PULSE_BUILD_VERSION` 默认是 `dev`：没有 Git 提交时 revision 为 `uncommitted`，已有提交但工作区有修改时标记为 `<commit>-dirty`。指定正式版本标签时，构建必须从干净、已提交的源码开始，并在发布本地镜像前再次确认源码提交未改变；否则立即失败，不以旧提交号标记新代码。这项门禁不会建立提交或推送仓库。独立行为检查使用临时 Git 仓库，可运行：

```bash
mise exec -- task build-provenance-check
```

仓库 `compose.yaml` 使用已构建的应用镜像，PostgreSQL 18 数据卷挂到 `/var/lib/postgresql`。为新部署准备唯一数据库密码与应用 DML 连接串，由受保护的部署配置提供 `PULSE_POSTGRES_PASSWORD`、`PULSE_DATABASE_URL`。镜像不自动迁移：用独立 DDL 账号的 0600 连接文件显式运行 `pulsemigrate`，成功后才启动应用。

连接文件写 PostgreSQL URI，必须明确 host、user、database 和 sslmode。生产远程连接使用 `verify-full`，同时配置数据库服务端 TLS，并向运行工具的容器只读挂载所需 CA/客户端证书；仓库 Compose 的 PostgreSQL 模板本身不启用 TLS，需按部署环境补齐服务端配置。本地开发可用 Unix socket 或回环地址配合 `sslmode=disable`。取消继承的 `PGSERVICE`。文件在容器内须由 UID 10001 可读、模式严格 0600，文件及父路径不得为符号链接。

```bash
# 数据库与应用凭据已从受保护配置注入；PULSE_APP_IMAGE 选择本次构建产物。
mise exec -- podman compose up -d postgres
# /secure/migration.conn 已准备为容器可达的 DDL 连接；TLS 文件另行只读挂载。
mise exec -- podman compose run --rm --no-deps \
  --entrypoint /app/pulsemigrate \
  --volume /secure/migration.conn:/run/pulse/migration.conn:ro \
  pulsemedia --data-dir /app/data \
  --connection-file /run/pulse/migration.conn --migrations /app/migrations
mise exec -- podman compose up -d pulsemedia
```

安装目标须为已经创建、没有用户对象的空数据库。工具持应用数据目录锁，并使用原迁移实现的单连接数据库锁；首次安装不创建 `secret.key`，主密钥由后续应用启动发布。开发 `.mise.toml` 的默认连接不会被该工具采用。旧部署若使用 `/var/lib/postgresql/data` 挂载，先停应用并确认所有 I/O 返回，盘点原命名卷及匿名卷中的实际 PGDATA，完成备份/新库恢复验证后再切换；不要直接更换卷路径或删除旧卷。

启动后访问配置的 HTTP 入口，首次访问进入 `/setup`。UID/GID、可写数据卷和端口由部署配置决定，当前应用固定以 10001:10001 运行。

显式选择本地候选镜像进行隔离运行检查：

```bash
PULSE_CONTAINER_TEST_IMAGE=localhost/pulsemedia:dev mise exec -- task test-container
```

该入口只操作自建容器和私有数据库，覆盖首次管理员初始化、重复 setup 拒绝、登出/再次登录、镜像内空库安装与恢复点保护升级、PG18卷删除容器后重建保留数据。最终 amd64 原生和 arm64 QEMU 镜像均已验证；真实硬件性能及阶段依赖按开发计划记录。

### 已有数据库离线升级

先停止应用并确认物理 I/O 返回，以第 3 节的备份命令从当前数据库导出配对恢复点，保留原数据目录及 `secret.key`。将数据库档案、分开保管的主密钥档案和 age identity 只读挂入工具容器；identity 和连接文件均为 0600。运行上述迁移命令时再加上：

```text
--archive /backup/pre-upgrade.db.age
--key-archive /offline/pre-upgrade.key.age
--identity-file /offline/restore.identity
```

已有 schema 的重复执行同样需要恢复点。原生构建使用 `mise exec -- task build-migrate`，运行 `bin/pulsemigrate`，参数与镜像一致；已有库验档需要 PG18 `pg_restore`。

工具在 DDL 前校验 age 认证 EOF、两个档案的配对及 schema、内容摘要与原主密钥，并离线完整解码 dump。操作人仍须选择来自当前数据库、时间合适的恢复点并完成恢复演练；验档不能证明来源和新鲜度。升级保留恢复隔离及不可变审计。多文件迁移可能在失败前已提交前序文件；失败后先核对实际 schema，再按第 3 节恢复到新库，保留原库及卷供排查，勿盲目重试或向下迁移。

## 2. 存储与权限规范 (UID/GID 10001)

容器运行时以无特权非 root 用户运行：
- **用户**: `pulsemedia` (UID: 10001)
- **用户组**: `pulsemedia` (GID: 10001)

### 挂载目录权限要求
宿主机上的媒体目录和数据目录必须允许 UID 10001 读取或写入：

```bash
# 本地持久化数据目录
sudo chown -R 10001:10001 /path/to/pulse_data
sudo chmod 700 /path/to/pulse_data

# 媒体库存储目录 (允许硬链接与读取)
sudo chown -R 10001:10001 /mnt/media
```

## 3. 数据库备份与恢复

先停止应用并确认所有物理 I/O 已返回，再运行离线工具。原生构建：

```bash
mise exec -- task build-backup
```

原生运行主机须提供 PostgreSQL 18 的 `pg_dump` / `pg_restore`，并为当前操作用户准备可写的 `/data/temp`；该目录用于 0600 临时归档和连接文件，需有足够磁盘空间。`build-container` 生成的镜像包含 `/app/pulsebackup`、PG18 客户端和 `/app/migrations`；容器运行备份时应离线挂载原数据目录、受限连接/identity文件及独立可写 `/data/temp`，并以 `--entrypoint /app/pulsebackup` 选择工具。下面以原生命令说明参数，文件路径对应实际运行环境。

准备 0600 普通文件：`source.conn` 为原库显式连接串，`admin.conn` 为有创建数据库权限的管理连接串。生产使用 `sslmode=verify-full` 并指定 CA，必要时指定客户端证书；Unix socket 也受支持。工具将连接配置转换为自有 0600 service/PGPASSFILE，不把密码传到命令行。原数据目录必须含可解密数据库凭据的原始 `secret.key`。

独立生成并安全保管 age X25519 identity；导出只需要其公开 recipient。备份库与主密钥档案分别保存在两个位置，至少保留七组配对备份；工具不自动清理历史文件。每次使用新文件名：

```bash
bin/pulsebackup export \
  --data-dir /srv/pulsemedia \
  --connection-file /secure/source.conn \
  --recipient age1_REPLACE_WITH_PUBLIC_RECIPIENT \
  --archive /backup/20261018.db.age \
  --key-archive /offline/20261018.key.age
```

导出持数据目录锁、验证原密钥能解密现有凭据，流式加密 custom dump。档案以恢复 UUID、schema 和内容摘要绑定，文件以 0600、原子无覆盖方式发布。身份私钥、主密钥档案与数据库档案应分开保管；数据库档案不包含媒体文件、全局角色或服务器配置。

恢复到尚不存在的显式数据库及不含 `secret.key` 的数据目录；`--target` 须以 `pulse_restore_` 开头、其后为 1–40 个小写字母、数字或下划线。不要提前创建目标库：

```bash
bin/pulsebackup restore \
  --data-dir /srv/pulsemedia-restored \
  --connection-file /secure/admin.conn \
  --identity-file /offline/restore.identity \
  --archive /backup/20261018.db.age \
  --key-archive /offline/20261018.key.age \
  --target pulse_restore_20261018 \
  --migrations ./migrations
```

恢复先验证两个 age 档案至认证 EOF，再创建新空库，执行恢复、schema 迁移、凭据解密验证及数据库隔离激活，最后才发布原主密钥。失败时删除本次创建的目标库；若清理失败，工具明确报告需隔离的库名，必须核查后再处理。已有数据库、主密钥或输出文件均不会被覆盖。

为目标库准备新的 0600 `restored.conn`，查看恢复 ID 和隔离状态：

```bash
bin/pulsebackup status \
  --data-dir /srv/pulsemedia-restored \
  --connection-file /secure/restored.conn
```

隔离期间应用拒绝启动。运维需离线逐项核对下载器任务、真实文件及外部孤儿，处理未决工作并保留原历史。写入 0600 对账 JSON，字段如下；使用 `status` 返回的真实 ID 和可追溯的实际证据，说明中不要放凭据或下载链接：

```json
{
  "RestoreID": "从状态输出复制的 UUID",
  "Operator": "负责此次恢复的操作人",
  "DownloaderEvidence": "下载器任务与原任务逐项对账记录位置",
  "FilesystemEvidence": "文件身份、完整性及未决操作核对记录位置",
  "OrphanEvidence": "外部孤儿处置记录位置"
}
```

```bash
bin/pulsebackup reconcile \
  --data-dir /srv/pulsemedia-restored \
  --connection-file /secure/restored.conn \
  --evidence-file /secure/reconciliation.json
```

存在可重启的 job、delivery、通知批次、下载任务或升级观察/跟踪工作时拒绝解除；不能只填写说明就清除旧任务或 unknown 保护。解除成功后再切换应用 DSN，使用恢复的数据目录启动。恢复历史与审计保留，不能通过降级移除。

可复验：`mise exec -- task test-backup` 覆盖真实 PG18 TLS、错误身份/篡改/配对错误、恢复清理、隔离及大归档流式处理。已启动独立 M4 服务时，运行 `mise exec -- task m4-restore-drill -- /data/temp/pulse-m4-services.oKhyfu`，验证 qBittorrent/Transmission 恢复点后的真实外部孤儿、人工移除任务而保留文件、未决旧任务拒绝解除及最终审计。演练通过 SQL 准备任务/映射和记录离线完成决定，不覆盖获取、整理或缺失映射修复；真实传输另有服务联调证据。M4-01 与 M4-03 的前置及本地验收已收齐；当前 Beta 总验收状态见开发计划。

## 4. 环境变量与安全

| 变量名 | 说明 | 默认值 |
|---|---|---|
| `PULSE_HTTP_ADDR` | HTTP 监听地址 | `127.0.0.1:8080` |
| `PULSE_DATABASE_URL` | PostgreSQL 18 连接串 | — |
| `PULSE_DATA_DIR` | 应用数据目录 (存放 secret.key 与 lockfile) | `/app/data` |
| `PULSE_LOG_LEVEL` | 日志级别 (debug, info, warn, error) | `info` |
| `PULSE_SECRET_KEY` | 32位主密钥 (未提供时自动在数据目录生成) | — |


## 5. Beta 候选与构建记录

V1 范围是开发计划的 M0–M4，后续 M5–M11 不随本次候选交付。正式标签构建须来自已提交且无修改的源码；版本信息和 OCI revision 必须能追溯到同一个提交。例如在最终回归通过后运行：

```bash
PULSE_BUILD_VERSION=0.1.0-beta.1 mise exec -- task build-container -- localhost/pulsemedia:0.1.0-beta.1-amd64 amd64
PULSE_BUILD_VERSION=0.1.0-beta.1 mise exec -- task build-container -- localhost/pulsemedia:0.1.0-beta.1-arm64 arm64
```

本地 Beta 验收已完成，版本为 `0.1.0-beta.1`。最终产物均绑定干净源码提交 `5b085978922e0d267e5c744ed9e62ce00ab575d9`；后续纯文档收口提交不改变这些产物的 revision。

| 本地镜像 | 镜像 ID | 已验证环境 |
|---|---|---|
| `localhost/pulsemedia:0.1.0-beta.1-amd64` | `e46c2a5de2e61bda72d5ca249f49b2b9cee88e601c5ce928527bc42695459d8a` | Linux amd64 原生 |
| `localhost/pulsemedia:0.1.0-beta.1-arm64` | `f627c3c47bd16a460907b9116d0545777c7a84e8cbe34599e34522d45bf5fb00` | Linux arm64 QEMU |

两个实际产物均通过非 root 初始化、登录、只读根目录、无 capabilities、优雅退出、空库安装、带恢复点的 schema82→83 升级及 PG18 持久卷重建。HTTP 常规 race 为 282 项通过、30 项门控跳过，PG 为 284 项通过、1 项门控跳过；浏览器和真实服务故障证据另列在开发计划中。

仓库内的 Git 提交、镜像构建和本地验收不会自动推送源码、镜像或创建发布。托管 CI 必须有实际远程执行回执；本地同等命令的成功记录分别登记。正式阶段是否放行，以 `docs/development-plan.md` 为准。

已验证 Linux amd64 原生与 arm64 QEMU 的启动、初始化、登录、离线安装升级、隔离恢复、只读根目录和 PG18 持久卷。原生 arm64 的硬件性能尚未量测。性能证据分为暂停电影订阅/小本地文件规模、十个合成活动端点，以及真实 NFS 故障，不能据此推算真实追更混合流量的容量；Go 堆内存尚未单独量测。

真实联调使用明确授权的服务和 CC BY 样本。公开索引源可能限流或超时，失败应保留原操作并等待/复核，不能盲目重复 Add。升级、跨根复用、copy/reflink/move 的确认和恢复边界沿用架构与操作页面提示。NAS 更换物理根时必须显式推进 generation 建立新基线，原文件事实不会随换挂载自动失效。


## 6. M5 开发切片与站点管理验证

M5 源码从 schema84/85 开始引入完整执行 URL 加密、明确协议和版本化站点命令；本节不改变上述 V1 Beta 镜像的 revision 或验收范围。源码 App 在开放 HTTP 与 worker 前完成旧站点配置转换，须使用原主密钥，错误密钥会阻止启动。离线升级与备份恢复继续遵守前述恢复点、凭据和隔离规则。

站点修改必须携带读取到的配置版本；省略 URL 或凭据表示保留，显式空凭据表示清空。列表只显示安全地址。删除会停用来源并保留下载、候选与升级历史。丢失响应时通过原键安全回执恢复；浏览器恢复记录仅含命令元数据，不含完整地址或凭据。

运行 `mise exec -- task test-sites-ui` 验证公开 Cookie、真实 Torznab、PG18.6 和 Chrome；可加 `-- http` 或 `-- browser` 只跑对应路径。脚本通过已有 Podman 包装器创建固定 PG18 测试环境，将构建、浏览器和测试临时文件放在独占 `/data/temp` 目录并在结束后清理，不读取个人部署配置或访问已配置的真实站点。
