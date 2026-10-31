# PulseMedia 生产部署与运维指南

PulseMedia 采用单二进制轻量微单体架构，内嵌现代化 React 19 前端，专为 PostgreSQL 18 设计。容器镜像默认以非 root 用户安全运行。

当前开发源码要求 **schema145**。公开 `v0.1.0-beta.2` 产物仍为schema126，安装时应核对源码、二进制revision与迁移版本，不能将开发源码直接标注为旧正式产物。升级前完成数据库与主密钥的配套备份并验证可恢复；由DDL凭据执行全部追加迁移成功后，以DML凭据启动新源码构建。不要把schema145数据库交给只支持schema126的旧二进制，也不要自动执行回退迁移。

接口兼容变化：订阅列表默认返回50条摘要、最多100条，不再默认包含全部逐集记录；外部调用方按total/offset/limit翻页，逐集详情和最多42天的日历范围分别读取。旧扫描/签到占位入口返回410，改用OpenAPI登记的当前命令。当前源码验收结果和正式发布状态只在[开发台账](development-plan.md#progress)登记。

当前源码构建的运行镜像包含系统公共 CA 证书包，供 TMDB 等出站 HTTPS 校验服务器证书链。本地数据库的部署专用 CA 继续由连接中的 `sslrootcert` 显式指定。

<a id="integration-verification"></a>
## 集成能力与专项复验

业务允许范围以[架构中的能力矩阵](architecture.md#integration-capabilities)为准；下表用于选择安装后的诊断和回归入口。所有 `task` 命令通过 `mise exec -- task <名称>` 执行，命令参数放在 `--` 后。能力存在、协议测试通过、真实账号联调和实际送达是不同的证据，当前进度与历史结果只记在[开发计划](development-plan.md#progress)。

| 范围 | 安装后核对与限制 | 正式复验入口 |
|---|---|---|
| 普通下载与真实服务 | 核对路径映射、原始字节、原操作标签及同inode入库；rTorrent另需HTTP XML-RPC桥接 | `m4-services-check -- <自有state-dir>`、`m4-r3-faults -- <同目录>`；rTorrent使用`test-m11-rtorrent` |
| 自动TV、整季包、升级 | 自动标准单集允许qB/Transmission；手工单集、整包及升级不据普通接口推定通用支持；未知日期不补造 | `test-automatic-tv`、`test-m8-complete-tv-watch`、`test-m8-upgrade-watch`；M3首次发送/整理专项见开发计划 |
| 全局速率、分流与做种移除 | 速率需显式部署开关；仅qB/Transmission，移除固定保留文件且不覆盖专用任务 | `test-m9-download-routing`、`test-m9-global-rates -- all`、`test-m10-seeding-management` |
| 站点、CookieCloud与签到 | 模板不证明站点普适兼容；CookieCloud限明确Nexus站点；签到POST结果不明不自动重发，不用可能写入的GET恢复探针 | `test-sites-ui`、`test-m10-cookiecloud-sync`、`test-m8-site-checkin-runtime` |
| TMDB、豆瓣、Bangumi、人物关注 | 浏览身份不授获取；豆瓣使用部署者提供的凭据，失败不假空；Bangumi需显式TMDB候选选择 | `test-subscription-creation -- all`、`test-m7-douban`、`test-m10-bangumi-calendar -- live`、`test-m11-person-follow` |
| TVDB、Fanart与字幕 | TVDB/Fanart默认关闭并需自有账号；字幕仅Shooter，mpv证据限定`sub-auto=fuzzy`，SubHD未启用 | `test-m11-tvdb`、`test-m11-artwork`、`test-m8-shooter-live`、`test-m8-subtitle-player` |
| 媒体服务器与通知 | Jellyfin/Emby精确路径，Plex歧义不全库后备；通知至少一次，不将202或平台接收当实际送达 | `test-m10-emby`、`test-m10-notifications -- all`、`test-m11-web-push`、`test-m11-web-push-browser` |
| 文件系统与NAS | 本地事件仅提示；NFS远端回退周期，缺原子NoReplace能力的copy/move停止保源；reflink不可用不退化 | `m4-nfs-faults`、`test-m8-nfs-watch`、`test-m8-untracked-commit-recovery` |
| 正式镜像与恢复 | 选择确切镜像ID；空库安装、登录退出、82→当前升级、加密恢复隔离和PG18卷均独立验证 | `PULSE_CONTAINER_TEST_IMAGE=<镜像ID> mise exec -- task test-container` |

常规CI的PG/race分片、前端行为和认证浏览器是基础门禁。变更下载发送/整理/恢复时，必须补跑对应真实服务或NAS专项；变更权限、命令恢复或界面时，补跑对应真实Cookie/Chrome入口；变更外部协议时，补跑其原生和生产App专项；变更迁移/密钥/容器时，补跑安装升级与备份恢复。门控跳过不能代替专项执行。需要人工审图的入口须完成图审和清理回执才记通过；无授权私有账号时登记未验证，不能通过新增通用重试或绕过上游验证来补齐。

使用正式Beta试用时，先下载并校验第5节公开归档，在独立数据库和媒体根安装；明确版本、镜像ID、schema、设备架构和根能力后，再做登录/重启、一次授权内容获取入库及故障恢复。长期试用按实际运行窗口记录，不以短时容器冒烟或QEMU结果代替真实家庭负载和原生ARM性能。

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

### 首次管理员初始化

未配置部署令牌时，初始化提交只接受应用直接收到的回环连接及本地主机名。容器映射端口和局域网访问通常不满足此条件，即使页面本身可以打开。部署者应为应用进程配置随机的 `PULSE_SETUP_TOKEN`，用户在初始化页的“部署令牌”字段填写同一令牌；页面仅将其随初始化请求发送为 `X-Setup-Token`，不写入浏览器存储，也不带到登录请求。

令牌应通过受保护的运行配置或挂载文件注入进程环境，不写入镜像、URL或日志。没有令牌或令牌错误时初始化失败；首个管理员创建后，持久用户状态会关闭初始化入口，令牌不能再次创建管理员。

### 家庭账号与双因素认证

初始化管理员后，在账户页创建家庭成员、调整角色或停用账号；至少保留一个活动管理员。用户可在同页修改自己的密码、查看和撤销其它会话。改密会撤销其它会话；停用账号立即阻止其会话和Token继续访问。管理操作只接受Cookie会话，现有只读API Token不能代办。

成员忘记密码时，管理员可在家庭账户列表选择“重置密码”，输入管理员本人的当前密码、已启用的验证码或恢复码，以及成员的新密码。成功后成员的全部会话与API Token失效，需使用新密码和原有双因素重新登录；已停用的账户仍保持停用，未确认的双因素绑定会被清除。此入口不能重置管理员或本人，也不能解除成员已经启用的双因素。页面不保存密码或验证码；若连接中断，先“读取原命令回执”，未确认前不要另建命令，必要时重新填写原输入并显式使用原键。

需要集中退出其它浏览器时，在“账户安全”的会话列表选择“撤销其它会话…”，重新验证当前密码及已启用的第二因素后确认。当前会话和API令牌保留；需要停用API令牌时使用令牌管理。结果待确认时先读取原安全命令回执，按界面提示使用原键提交，不能换键或清除未决记录。回执仅证明原命令已执行，命令之后新登录的会话不会被旧回执重放撤销。

TOTP需先验证当前密码，选择“显示绑定二维码”用验证器扫描，或选择“显示绑定密钥”手动输入，再填写验证器的六位验证码确认启用。二维码由浏览器本地生成，未使用外部二维码服务；绑定有效期为十分钟，到期后二维码和手动密钥会隐藏，需要重新准备。恢复码只展示一次，应保存到受保护的离线位置；每条仅可用于一次登录或安全操作。启用后登录选择验证码或恢复码，不同时填写两者。恢复码耗尽或遗失时，可在账户安全中选择“重新生成恢复码”，验证当前密码与有效验证码或恢复码，并确认全部旧码失效；更新保留验证器和当前会话，撤销其它会话及API Token。新码仅在首次成功响应显示，请及时安全保存。结果待确认时先读取原回执，尚无回执时可重新填写凭据并选择“使用原键提交恢复码更新”，不要删除原命令记录；若已确认但未收到新码，可用原验证器验证码再次生成。遗失设置响应时先读取原命令结果，未完成的设置可以按界面提示重新准备。改密保留已启用TOTP，清除未确认的设置；禁用双因素需再次验证密码和有效因子。迁移与恢复必须保留数据库配对的原主密钥，不能通过初始化绕过已启用的双因素。

### Seerr 专用求片接入

管理员在设置中启用Seerr集成，绑定本地归属账号并设置专用凭据；首次保存后归属账号和来源身份固定，修改凭据或启用状态使用当前配置版本。将Seerr Webhook指向`POST /api/v1/integrations/seerr`，单值`Authorization`请求头填写该专用凭据原文（不添加Bearer）。可启用`MEDIA_APPROVED`（手动批准）和`MEDIA_AUTO_APPROVED`（自动批准）事件；剧集需提供明确的`Requested Seasons`，不从缺失字段推定整部授权。传输应使用受信HTTPS或受控内网。

外部批准只会创建本地待审批求片，管理员仍须在PulseMedia内审批。来源请求ID用于去重；相同ID改变通知类型、媒体、季集合或其它正文内容会冲突。接收记录可在设置页查看；关闭配置、归属账号停用或配置管理员撤权后拒绝继续接收。该接口不授予普通API Token写权限，Webhook凭据不要放进URL、日志或浏览器存储。

### 中文字幕候选比较

管理员查询Shooter字幕后，可按每份候选的字幕条数与精确到毫秒的时间范围比较。范围取全部字幕条目的最早开始和最晚结束，不代表影片时长、同步程度或翻译质量；缺少摘要会明确显示不可用。读取摘要只使用本地加密缓存，不重新访问字幕服务或写入文件。

新查询会在原有最多八份输入中去除规范化后完全相同的SRT，保持首次出现顺序；内容或时间不同仍保留。已有候选记录及确认不改写。选择后仍须显式确认安装，文件版本变化需重新查询，已有字幕保持保护；结果不明时先读取原状态，不重复安装。主密钥必须与缓存配对，解密失败不会当作零条字幕。

### 核心媒体流程与恢复

电影订阅启用“持续更优”或“达到目标停止”后，已满足的电影最多每六小时发现一次候选；订阅详情可刷新既有发现结果、比较候选，再进入原升级确认流程。发现结果不是下载授权，品质相同、证据过期或不完整分页都会保留相应提示。

外部文件整理先从已配置目录选择文件。连续编号或无法自动判断季集的剧集，可选择已有作品的真实规范集，预览后确认；规范集附有绝对集号属性也可按这一显式映射整理，程序不会从绝对集号推算季集。文件或元数据改变需重新检查。升级至schema138后启用该兼容；存在此类整理历史时，迁移会拒绝回退到137，保留原记录。视频入库后可在其结果中选择同目录、唯一匹配的SRT/ASS附件，显式复制或硬链接；源字幕保留，已有目标不覆盖。刷新后使用保存的整理定位读取结果，丢失响应时先恢复原命令回执。

文件识别纠正中，文件与媒体分别翻页；已有作品可按标题、类型或TMDB ID查找，也能保留原媒体选择，不再限于最新100项。单集升级先按订阅目标根分页选文件，再读取当前映射和订阅。读取或检索不会自动纠正映射、下载或整理；失败可重试，选择会保留。

自动TV、完整季包、电影升级和标准单集洗版的已确认下载，可在活动详情使用原生暂停/恢复；单集工作区也提供原任务控制，进入整理复核后不再新发启停。暂停订阅不等于暂停下载，暂停下载不会释放占位。结果不明时只观察原任务，勿换键重发。标准单集洗版只支持单标准集、单旧视频和唯一有效映射；候选发现和比较不授权下载，准备、发送、整理及退休仍逐步确认。

同目录的一季外部剧集可使用批量入口，每批最多20项。逐行选择真实规范集、动作与目标后读取预览；歧义、重复规范集、目标或文件别名需先修正。创建批次只是保存清单，各行仍须独立确认与整理；文件或元数据变化只使对应行失效。丢失响应先读取原批次和行回执，不自动重发。

媒体库文件详情的受控更新只处理可证明由应用生成、且当前内容未改动的规范NFO与poster.jpg。先读取预览，再明确确认；人工文件、旧版本缺少归属证据的内容和冲突文件保持不变。使用当前可信元数据，保留既有元数据缓存策略，可选Fanart产物不在该更新范围内。

文件详情可预览同根单视频的新相对路径，明确确认后重命名；目标不得已存在，附件不会随之搬移。回收区列出电影与单集升级退休文件；文件大小不代表可释放空间，需显式预览才观察分配字节和硬链接数。恢复保留当前新版本、只回原路径；清理须在保留期届满后再次预览并确认。不确定结果保留原命令，先读回执并人工核对；保留期限不会触发自动清扫。 可先按电影／剧集和「原路径关键词」筛选回收记录，再翻页；关键词按字面匹配，不展开通配符。改变筛选从首页读取，清除筛选恢复全部记录；读取失败可保持原条件重试。筛选不测量磁盘，也不确认恢复或清理。

这些入口需升级至schema144（追加迁移139–144）；有业务历史时迁移会拒绝破坏性降级。`mise exec -- task test-core-media`执行真实PG/race与Cookie/Chrome业务验收，`mise exec -- task test-core-media-ui`验证界面、恢复及完整前端回归。两者检查零跳过并清理自有资源；本地通过不等同远程CI已执行。

管理员可在设置的「元数据连接测试」中显式测试TMDB API、TMDB图片、Fanart API和TVDB API。测试使用当前已保存的配置；未配置或停用的补充来源不会发起请求。TVDB测试会进行一次原生登录并丢弃返回令牌；其他来源访问固定只读目标，不保存媒体或图片。仅主机有响应不代表API认证或图片内容验证成功，配置变更或撤权后的迟到结果会丢弃。

本批验收入口为`task test-core-media`（隔离PG、race、执行计数及零跳过）和`task test-core-media-ui`（受控API的Chrome页面与恢复验证）；前者不表示连接了真实外部下载器，后者不替代PG或真实手机操作验收。

### 容器运行验收

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

### 整理文件过滤

`organization.min_media_bytes`设置整理正片的最小实测字节数，也可用环境变量`PULSE_ORGANIZATION_MIN_MEDIA_BYTES`覆盖。两者只接受正整数，默认`1`字节，保留现有部署整理小文件的行为；例如`209715200`表示200MiB。修改后重启应用生效，零、负值和非整数字符串会在启动配置校验时拒绝。

```yaml
organization:
  min_media_bytes: 209715200
```

样片、预告、隐藏文件和非视频文件会被排除。大小恰好达到阈值可以整理，零字节或未测量的文件不可以；正常名字中的子串（例如Sampler）不会按sample词误杀。过滤不会删除来源文件、扫描库存或已有入库事实。整包有正片低于阈值时进入复核，保留完整覆盖与需求，不默默少导一集。提高阈值也不改变原键回执；文件已经发布但事务未完成时，仍按原许可和完整内容只读恢复，不重复发布。功能复跑使用`mise exec -- task test-m8-media-filter`，支持范围与当前验收状态见开发计划。

### 整理命名模板

管理员通过版本化设置接口保存`organization.naming_templates`，同时显式提供`movie_template`与`episode_template`；每个字段设为空字符串可恢复该媒体类型的默认命名。草稿与已存配置通过`POST /api/v1/organization/naming/preview`预览，样例不代表身份或文件执行证据。保存不发起整理或移动文件，已冻结计划、历史回执和恢复任务保留原目标。

```json
{
  "movie_template": "Films/{title} ({year})/{title}.{extension}",
  "episode_template": "Shows/{title}/Season {season}/{episode_key}.{extension}"
}
```

支持`title`、`year`、`extension`、`tmdb_id`、`imdb_id`、`resolution`、`source`、`codec`、`hdr`、`audio`、`edition`、`group`；剧集另有`season`、`episode`、`episode_key`。年份、身份及品质未知时，使用这些字段的自定义模板会拒绝生成；不要用未知值推断默认身份、年份或季集号。目录分隔符由模板字面部分给出，变量不能注入目录，输出须保留原视频扩展名。外部整理与跨根复用的明确人工目标优先；整包确认遇到模板版本变化需重新预览。正式复跑入口为`mise exec -- task test-m9-naming-templates`，当前验收状态以开发计划为准。

### 下载器自动分流

路由在应用启动时读取。默认无规则且未启用时沿用原行为；设置`download_routing.enabled: true`可启用单个合格下载器的自动回退，有多个合格下载器时须给出明确规则。`enabled: false`可显式关闭路由，即使仍保留规则；未指定开关而提供非空规则时自动启用。修改后重启生效，已有手选绑定和执行中的任务继续使用原下载器。

```yaml
download_routing:
  enabled: true
  rules:
    - media_type: movie
      downloader_id: 11111111-1111-4111-8111-111111111111
    - media_type: tv
      downloader_id: 22222222-2222-4222-8222-222222222222
    - indexer_id: 33333333-3333-4333-8333-333333333333
      downloader_id: 22222222-2222-4222-8222-222222222222
```

示例UUID须替换为本部署中已有的下载器和站点ID。环境变量`PULSE_DOWNLOAD_ROUTING_ENABLED`覆盖开关，`PULSE_DOWNLOAD_ROUTE_RULES`以JSON数组覆盖全部规则，`[]`清空规则。最多64条，媒体类型为`movie`或`tv`；媒体和站点都省略表示默认规则，同一媒体/站点作用域不能重复。规则优先级为“站点及媒体同时匹配、站点、媒体、默认”。下载器类型和路径映射必须支持目标根；命中目标不存在或不可用时停止，不切换到其他端点发送。

电影在新轮次冻结每个来源的选择，最终按胜出候选的来源发送。自动TV首次选择会原子保存为订阅绑定，并刷新意图与Wanted；其多个有效来源必须选择同一下载器，否则不自动绑定，需要明确手选。后续规则变化不改已保存的TV绑定；需要重新自动选择时，显式清除该绑定后才会在新的合格轮次重新决策。历史原键回执保持原值，当前状态应重新读取。

正式复跑入口为`mise exec -- task test-m9-download-routing`，当前验收状态以开发计划为准；受控协议验收不代表真实PT站点内容传输。

### 全局上下行速率看板

全局速率采集默认关闭。需要时设置下列配置，或用环境变量`PULSE_DOWNLOAD_GLOBAL_RATES_ENABLED=true`覆盖开关，重启应用生效：

```yaml
download_global_rates:
  enabled: true
```

开启后，应用通过qBittorrent的`transfer/info`、Transmission的`session-stats`及rTorrent的`throttle.global_down.rate`/`throttle.global_up.rate`只读统计全部传输的瞬时上下行速率。每个下载器同时最多一个采集任务，App持有3个消费者，正常采集间隔至少10秒；原生限流建议可延后下次采集。该开关不更改任务发送、限速设置或已有任务的进度观察。

首页卡片仅管理员会话可见，显示最近10分钟曲线和下载器明细。速率超过45秒、采集失败、方向缺失或配置变化时显示未知；已知零显示`0 B/s`。全局某一方向合计只在所有下载器的该方向观测有效时显示。最后成功值及时间单列，历史缺口不连线；读取卡片只查数据库，不触发下载器请求。关闭采集后，首页明确显示未开启，不把历史样本当作实时值。

历史清理随App受控运行，启动及每分钟各一批，不触发下载器请求。14天前已完成、租约已过期且无当前/最后成功指针的速率样本与原job原子回收，每批最多200组；abandoned及未决执行保留。30天前完全无查询和业务引用的候选每批最多200条，已有分页证明、授权与关键回执不清除。此机制不删除媒体文件，不需要额外部署开关；schema145追加两个索引，按原离线迁移流程应用。

### 做种管理与保留文件移除

做种配置缺省不存在，不会自动启用。管理员Cookie会话通过`GET/PUT /api/v1/download-clients/{id}/seeding/config`读取或保存；PUT带原键和当前`expected_version`、`expected_client_updated_at`，周期60–86400秒。规则的`mode`为`any`或`all`，至少一个阈值：原生`min_seeding_seconds`、`min_share_ratio`或`low_free_bytes`，并固定`delete_files=false`。保存不立即执行；App按到期任务排队。原键GET位于该路径下`/commands/{command_key}`，保存响应不明时先读取原回执；历史回执不表示当前配置仍启用。配置卡片属于M10-07。

只为有原端点绑定、全部原需求有效入库且无待处理工作的普通下载任务自动移除下载器记录。新发送保留原端点摘要，历史任务不补造；自动TV、整包、升级等使用专用入库账本的任务暂不自动移除，继续保持原保护。两下载器动作都保留下载文件；已有库文件、硬链接及入库事实不修改。qBittorrent的空间阈值只适用于已核实与默认保存目录一致的任务路径，其余路径容量未知，不能触发该阈值。

响应丢失或完成事务失败后永久许可保留，只读观察原任务，不重发移除；仍存在或无法确认的任务进入人工处理。停机等待实际I/O结束，不能因job租约过期强行释放物理锁。不要手动删除执行/许可历史来解除`review_required`，也不要用新配置或任务重试不明副作用。正常修改或禁用配置不取消已发出的请求。

正式复跑入口为`mise exec -- task test-m10-seeding-management`。App验收使用受控qBittorrent/Transmission协议服务及真实PG18.6与本地硬链接，不代表真实账户或生产下载器联调；65任务分页为独立数据库授权图的结构验收，不是65次真实下载或时延测量。

活动详情另提供“读取做种状态”的显式只读入口，支持普通任务的qBittorrent、Transmission和rTorrent。只有点击才查询原下载器，页面刷新不重查；原任务身份、路径、端点配置及当前对象权限在调用前后均需有效。未知时长或分享率显示未知；rTorrent仅提供原生分享率，不推算时长或目录可用空间。此读取不产生移除许可，不删除任务、文件或释放占位。

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

完整数据库归档包含账号安全回执、TOTP设置、恢复码消耗记录、Seerr配置/请求去重和加密字幕候选。导出与恢复都会使用原主密钥检查这些新增密文，即使库内没有下载器或站点凭据也不能接受错误密钥。恢复会保留已消耗恢复码与原请求身份，不重置双因素或允许重发历史请求；旧schema按实际存在的密文表校验后再迁移。

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

签到执行启用后，未决的每日发送许可也会阻止释放隔离，即使原job已dead。先逐项核对原始DayID、账号、冻结日期和实际外部状态；仍不能确认的许可只可明确保留未知。例如在上述JSON中另加`"CheckinDecisions": [{"DayID": "历史读取返回的真实日UUID", "Evidence": "原始许可及外部状态核对记录位置；结果未知，保留人工复核"}]`。这不是重试许可，不表示已签到，不会清除或重绑原prepared指针；任何其他未决工作仍会使整笔对账回滚。当前日已人工接管后不自动排入旧观察任务，不能利用修改配置或重启再次发送。运行中的相关job必须先按实际执行结果处理，不可用对账JSON强制越过。

恢复隔离期间App不启动；若没有先前导出的历史DayID，可在恢复库的只读管理会话核对以下安全字段，再将真实UUID填入逐项决定。该查询不请求PT，也不暴露Cookie或执行nonce：

```sql
SELECT d.id AS day_id, d.indexer_id, d.expected_account_id,
       d.calendar_day, d.timezone, d.day_start, d.day_end, d.status,
       d.prepared_execution_id IS NOT NULL AS prepared
FROM site_checkin_day d
WHERE d.status <> 'attended_observed' AND d.restore_id IS NULL
  AND (d.prepared_execution_id IS NOT NULL OR EXISTS (
      SELECT 1 FROM site_checkin_execution e
      WHERE e.day_id = d.id AND e.finished_at IS NULL))
ORDER BY d.calendar_day, d.id;
```

可复验：`mise exec -- task test-backup` 覆盖真实 PG18 TLS、错误身份/篡改/配对错误、恢复清理、隔离及大归档流式处理。已启动独立 M4 服务时，运行 `mise exec -- task m4-restore-drill -- /data/temp/pulse-m4-services.oKhyfu`，验证 qBittorrent/Transmission 恢复点后的真实外部孤儿、人工移除任务而保留文件、未决旧任务拒绝解除及最终审计。演练通过 SQL 准备任务/映射和记录离线完成决定，不覆盖获取、整理或缺失映射修复；真实传输另有服务联调证据。M4-01 与 M4-03 的前置及本地验收已收齐；当前 Beta 总验收状态见开发计划。

## 多通道通知（M10-04）

在设置的“通知与投递”中添加独立收件人。Webhook与Telegram保持原有合同；企业微信填写企业群机器人HTTPS地址，钉钉填写官方机器人地址及平台启用的签名密钥，飞书填写官方机器人hook及可选签名密钥，Bark填写自托管或公有HTTPS服务的单一设备键地址。凭据只加密保存，不回显；更换地址/密钥使用“停旧建新”，旧消息仍归旧通道，不自动改投新目的地。

企业微信、钉钉和Bark可配置不含查询或凭据的TMDB公开w342图片URL；飞书可填写已经上传的`img_`图片键。通知不会下载或上传图片，也不会请求元数据补图；不填图片时发送原有安全文字卡片。不能在封面中填写私有下载链接。

“发送测试通知”返回“已排队”，不代表平台已经接收；在投递记录核对实际送达或失败。测试会使用现有队列，和真实入库通知一样遵守单独渠道限流、重试、dead复核及至少一次语义，平台可能重复显示同一Event-ID。浏览器先保存不含秘密的原命令记录；响应丢失时显式“重试同一测试通知”恢复原回执，不能用新命令代替。浏览器存储不可用时测试按钮拒绝写入。配置保存本身不触发测试推送。

投递记录支持按状态查询历史并翻页，不再只限最新100条；收件人筛选仅作用于本页。切换状态从最新一页开始，读取失败可按原条件重试，较早投递更新后可能移回前页。找到旧的失败投递后仍须核对重复风险及原版本，翻页和列表变化不会恢复、重放或跳过投递；响应不明时保留原命令重试。

媒体通知批次封存生成通知事件后，可点「查看原投递记录」定位该批次的原事件，不需要在分页列表里手工比对事件ID；列表只读，不自动发送。定位后仍可按状态筛选、翻页与失败重读，点「显示全部投递」清除定位。待确认的重放/跳过命令禁止切换批次；列表不是命令回执，原键恢复不变。尚未封存的批次不显示此入口，批次状态不代表当前文件拥有或已送达。

可复验：`mise exec -- task test-m10-notifications -- backend`运行48项原生/PG/race及6项前端原键检查，`mise exec -- task test-m10-notifications -- browser`运行隔离Chrome/真实Cookie/PG界面验收；默认同时执行。浏览器验收会等待本地截图审阅，确认后写入其输出目录的`inspected`标记。验证使用自有TLS协议服务，并非真实企业机器人或个人手机送达证明。schema116保留旧Webhook/Telegram，已有机器人通道时拒绝回退到115以避免丢失历史。

## Bangumi发现页周历与追番（M10-06）

发现页选择「动漫新番周历」，按周一至周日或整周浏览，使用「中日名称筛选」匹配中文名或原名。放送星期来自Bangumi；页面的首播日期属于作品，不能用来判断当前单集已经播出。未知日期和评分保留未知，完整有效的空清单与读取失败分别显示。封面只使用上游校验的公开来源，缺图不影响浏览。

点击「追番 · 选择作品」只读取TMDB电视剧候选，不创建订阅或下载。请核对作品后进入「查看详情并追番」，选择季集、目标根、策略及需要的站点/下载器；保存使用既有订阅创建与原键恢复。Bangumi ID不是TMDB ID，标题相似也不建立可信跨源映射。候选不明时请在影视搜索尝试其他名称，不借周历编号发起下载。无站点绑定时仅保存订阅意图，何时自动获取遵循现有订阅合同。

正式复验入口：`mise exec -- task test-m10-bangumi-discovery -- all`，也可按`frontend`、`backend`、`browser`分组。浏览器组使用自有Bangumi/TMDB服务、真实Cookie、PostgreSQL18.6与Chrome，四宽六图审阅后才生成正式通过回执；不等同于所有真实Bangumi作品都有TMDB唯一对应。临时资源位于`/data/temp`并在结束时清理。

## Emby、Jellyfin与Plex精准刷新（M10-05）

在媒体服务器管理中选择实际协议、服务器地址、API密钥和本地到服务器的路径映射。Emby可保留`/emby`等反向代理前缀；探针使用认证的`/System/Info`，空响应或登录页不能证明连接成功。密钥沿用主密钥加密保存，不进入URL查询或公开响应。

新入库事件快照当前收件服务器，分别投递，不给后来添加的服务器补造旧投递。Emby/Jellyfin只提交映射后的具体文件路径；Plex只提交匹配库的所在目录，未匹配或歧义不会触发全库扫描。一个服务器离线、鉴权失败或限流不撤销本地入库事实，也不阻塞其他服务器；Emby的`Retry-After`保存在该投递记录。刷新回执只表示平台接收刷新，不代表已经索引该文件。可选文件确认还必须取得完整、唯一的精确路径证明；部分清单、坏包和无匹配不能确认，Emby清单超过8MiB时保留未知。

复验入口：`mise exec -- task test-m10-emby`。正式覆盖使用自有HTTP协议服务和PostgreSQL18.6，包含三原生协议、实际硬链接入库、映射路径、独立限流、原生精确确认与事务回滚；不等同于已验证用户自有的Emby账号、库规模或播放器。

## Fanart艺术画（M11-01）

通过现有管理员设置API保存`fanart.api_key`（`value`为`{"api_key":"用户自己的密钥"}`）及`fanart.artwork`（`value`为`{"enabled":true}`）。每次PUT都携带当前`expected_version`与独立`Idempotency-Key`；响应丢失时读取`/api/v1/settings/{key}/commands/{request_key}`恢复原结果。密钥加密存储，不从公开设置回读；配置响应禁止缓存。默认关闭，保存只是持久化，不代表已下载图片，也不会为既有文件自动补造任务。前端开关属于M11-07。

新入库后的侧写任务按原权限与目标目录证据处理艺术画。电影使用TMDB编号；剧集必须通过TMDB外部身份接口取得匹配的TVDB编号。无可靠映射时保留NFO与原海报能力。电影可写`logo.png`、`clearart.png`、`banner.jpg`、`disc.png`，剧集不提供`disc.png`。既有文件不会覆盖或改变权限。关闭开关、换密钥或配置不可读取会阻止剩余艺术画发布，但不撤销已完成入库或删除已发布文件；正在读取的请求仍等待实际结束。

访问仅涉及TMDB、Fanart API和固定图片主机；不会发送本地路径。每张图片限制8MiB、每边4096像素及总1600万像素，PNG透明度保留。网络失败、限流、坏图片和身份错配不视为成功下载，也不阻断原NFO流程。复验入口为`mise exec -- task test-m11-artwork -- pure`、`store`、`app`，或省略参数依次执行全部组。PG侧写按固定清单拆组并严格检查父测试数量与零跳过；电影和剧集验收使用自有TLS协议服务和真实硬链接入库，不代表用户自有Fanart账号或实际公网画报已验证。

### TVDB备用元数据（M11-02，本地验收通过）

使用现有管理员设置接口配置`tvdb.credentials`，其`value`为`{"api_key":"用户自己的TVDB API密钥","pin":"可选PIN"}`；密钥和PIN成对加密。另存`tvdb.fallback`的`{"enabled":true}`以明确启用。两项PUT各自携带当前`expected_version`和独立`Idempotency-Key`；回执丢失时按原键读取结果，不重复生成命令。默认关闭，保存不发起登录、抓取或刷新；公开响应只表示配置状态。关闭无需重新提交凭据。

公开剧集详情在主源明确缺少中文字段且有可靠作品映射时补译名，并返回`translation_source`标明来源和字段。原内容、作品编号及季集事实保留；备用失败退回主源，展示译名不改变订阅、现有文件命名或冻结计划。显式订阅元数据刷新可发现缺集，但必须由TMDB反查并读取真实单集证明规范身份；未匹配候选不进入Wanted，未知日期不被推造。补充结果不证明完整季，也不授权退休已有单元。测试目前使用自有TLS协议服务；公网账号与用户订阅可用性不属于已确认结果，完整验收进度见开发计划。

复验入口为`mise exec -- task test-m11-tvdb -- pure`、`store`、`app`，省略参数则依次运行全部组。固定清单共120个父测试，各组要求数量匹配、零跳过和race通过，PG18.6由自有隔离容器提供并检查数据库、容器及临时目录清理回执。真实Cookie/生产App测试分别验证展示只读与缺集事务/原键恢复；旧原型兼容测试单独标记，不用它替代生产接线证据。

### 系统审计查询（M11-03）

`GET /api/v1/system/audit`查询既有业务事务的审计摘要。需要当前活动管理员会话，或属于当前管理员、精确`read` scope的API Token；Token不能提升账号权限。每页重新检查当前角色和活动状态，所有响应均为`private, no-store`。审计看板安排在M11-07，本接口不增加后台执行开关。

筛选参数为`actor_type`（user/service/system）、`actor_id`、`action`、`target_type`、`target_id`及RFC3339时间`from`/`until`。字符串精确匹配，时间为左闭右开、最多微秒精度；未知参数、重复参数、空值和大小写别名均拒绝。`limit`默认50、上限200。续页使用响应的`next_cursor`并保留原筛选和当前账号；按时间与ID降序读取，不承诺跨页快照。游标行被历史保留策略清理后仍可继续，后来插入的较新记录需重新查询首页查看。

返回字段固定为`id/actor_type/actor_id/action/target_type/target_id/created_at`，不返回历史任意JSON payload或配置值。业务失败或事务回滚不留下虚假的成功审计，原键恢复不重复记账；查询本身不增加审计、事件或任务。存储不可用返回503而非成功空数组。schema118仅追加查询索引，不清理或改写历史记录。

正式复验为`mise exec -- task test-m11-audit`，可选`pure`（12项）、`store`（7项）或`app`（2项）；共21个父测试，要求固定数量、零跳过、race通过及独立PG18.6数据库/容器/临时目录清理回执。验收状态以开发计划为准。


### rTorrent / ruTorrent下载器（M11-04）

在下载器配置中选择`rTorrent / ruTorrent`，填写实际HTTP(S) XML-RPC入口，例如`https://seed.example/RPC2`，以及该入口的HTTP Basic认证用户名和密码。ruTorrent首页及仅支持SCGI的监听地址不是HTTP XML-RPC入口；需要部署者提供对应HTTP桥接。连接测试只调用`system.client_version`，保存配置仅持久化密文、版本和成功账本，不加载或删除种子。空密码编辑保留原密文；原有下载任务仍复用冻结的配置摘要和发送授权。

普通电影下载使用`load.raw_start`传送原始种子字节（暂停加载使用`load.raw`），或使用明确的磁力加载方法；目录和PulseMedia操作标签一并绑定。观察以完整`d.multicall2`结果验证hash、标签、原生字节、路径和完成状态；fault、截断和不完整响应均不是“任务不存在”。丢失加载回执进入现有unknown/只读协调，不自动重新加载。`d.erase`仅保留文件移除，删除文件请求明确拒绝；自动TV、整季、升级、全局速率及自动做种移除仍受各自支持能力限制，不能因为新客户端可创建而获得执行许可。

活动详情中的“暂停下载/恢复下载”支持rTorrent普通原任务，使用同一持久命令和共享需求授权。确认后仅向原hash发送一次；页面刷新或原键恢复不会重发。返回结果不明时，先读取原命令回执，再点击核对下载器当前状态；新命令在原结果未确认前保持阻止。下载器处于hash校验、启动排队或活动状态不一致时，核对继续显示未确认，不会把暂时不活动当作已暂停。此功能不撤回需求、不释放占位、不删除种子或文件。

schema119只追加第三下载器类型；存在rTorrent记录时禁止回退至118，以免丢失或重写配置。正式复验`mise exec -- task test-m11-rtorrent`支持`pure`（18项）、`store`（6项）、`app`（6项）和默认`all`（30项），固定父测试计数、零跳过、race及PG18.6资源清理回执。配置控件测试包含第三类型创建/编辑/显式探测；真实App包含正常加载和丢回执只读恢复后的硬链接入库，Cookie路由验收覆盖暂停/恢复、共享权限、丢失ACK阻止替代命令及显式只读确认。网络验收使用自有HTTP/TLS协议服务，未声称真实用户rTorrent/ruTorrent、反向代理或自定义事件脚本已验证；最终验收状态以开发计划为准。

### 浏览器原生通知（M11-05）

Web Push默认关闭。在部署的`web_push`配置中设置`enabled/public_key/private_key/subject`，或使用`PULSE_WEB_PUSH_ENABLED`、`PULSE_WEB_PUSH_PUBLIC_KEY`、`PULSE_WEB_PUSH_PRIVATE_KEY`、`PULSE_WEB_PUSH_SUBJECT`。使用匹配的P-256 VAPID公私钥，分别为65/32字节的无填充base64url；subject使用可联系的邮箱（可带`mailto:`）或HTTPS地址。启用前校验配对，缺失或不匹配会阻止启动。私钥只保存在部署秘密中；浏览器只获得公钥及其版本，切勿将私钥填写到普通设置或日志。

用户在设置页“浏览器通知”点击启用，使用HTTPS或浏览器认可的本机地址，并明确授予通知权限。保存只登记本账号设备，不立即发通知；新的真实入库事件才冻结收件人，后来注册不补发旧事件。成员只接收自己当前订阅/需求范围内的提醒。通知不含作品、路径或账号详情，点击打开本站查看。浏览器退出账号时移除本地订阅；服务器404/410确认过期后停用对应设备。隐身模式通常不支持Push订阅；首次申请也可能受推送服务网络时延影响，尚未返回时不会重复提交。iOS/Safari等平台可能要求先安装到主屏幕，实际可用性由平台决定。

端点及浏览器密钥在服务器加密，列表和历史回执不回显；浏览器localStorage只保存账号隔离的命令ID/摘要/版本和设备ID。存储失败会锁定写入，响应丢失后先查原请求回执，再明确同键重试。VAPID换代后旧投递不能改用新钥或新设备，需明确重新登记；已撤销/过期设备不能复活。推送采用至少一次语义，即使同事件使用固定通知tag，也不承诺跨设备或服务商严格去重。服务HTTP201只表示接收，不能替代实际通知显示。

正式后端复验`mise exec -- task test-m11-web-push`支持`pure/store/app/all`，固定32父项、12前端行为、零跳过、race与PG18.6清理。`mise exec -- task test-m11-web-push-browser`使用自有隔离普通Chrome配置，通过真实FCM订阅及原生VAPID网络发送核验浏览器通知，并检查回执恢复、退出退订、账号隔离、四宽度六截图和44px控件；截图需审阅后释放清理。其首订阅预算为90秒，不自动重试申请。Firefox/Apple/Windows的合法端点虽通过原生边界校验，本次真实网络实证仅Chrome/FCM；不代表用户个人设备或其它平台已联调。

### 演员与导演关注（M11-06）

当前账号通过`/api/v1/person-follows`列表及`/{id}`读写关注配置，历史回执使用`/receipts/{key}`。保存时需明确TMDB人物ID、actor/director角色、最早上映日期、15–10080分钟间隔、至少一个电影/TV目标根及质量策略版本。目标根须在线且类型匹配；绑定省略时保留、空列表清除，非空站点列表需下载器。保存只确认持久配置，后台独立巡检，不代表已创建订阅或完成下载；订阅页“演职员关注”Tab可创建和编辑配置，演员/导演及人物ID保存后固定。成员可保留或清除绑定；管理员读到站点、下载器选项后可指定新绑定。保存前持久化原键恢复元数据，响应丢失后先查询原回执，再明确重试。

设置页的“Fanart艺术画报”和“系统操作审计”仅管理员可见。密钥与画报开关分别使用原版本保存；读取失败清除旧值并锁定写入，保存不会立即抓图。审计使用服务器精确筛选与游标，不包含历史敏感payload。三面板相邻API/事务复验：`mise exec -- task test-m11-advanced-settings`（11父项、PG18.6/race、零跳过）；真实Cookie、Chrome与四宽度六图复验：`mise exec -- task test-m11-advanced-settings-browser`。截图就绪后按已输出的专属目录审阅六图并写入`inspected`标记，夹具才进行保护快照终检和清理；普通配置保存不构成执行成功。

Worker自动解释已启用配置，按每轮一次人物读取和64作品分页生成订阅；TV保留真实季集、空季及未知日期。无上映日期留待复核，超出起始日期或未选目标的候选不会建档；已有暂停订阅不会被启用。停用、失权或配置更新阻止旧轮次继续提交。恢复保留原任务和来源；物理调用失联且无释放证据需要人工核对，不能靠改版本或重新保存授权重读。

正式复验`mise exec -- task test-m11-person-follow`支持`pure/store/app/all`，固定127父项（79规则/原生/HTTP、42仓储与兼容、6公开App及兼容），要求零跳过、race及PG18.6资源清理回执。65配置/作品/恢复分页是结构证据，不代表65次真实下载或端到端时延。演员/导演电影和稀疏TV闭环使用自有TLS协议服务与真实Cookie/生产App验证，不能替代真实用户TMDB账号或外部站点联调。

## 4. 环境变量与安全

| 变量名 | 说明 | 默认值 |
|---|---|---|
| `PULSE_HTTP_ADDR` | HTTP 监听地址 | `127.0.0.1:8080` |
| `PULSE_DATABASE_URL` | PostgreSQL 18 连接串 | — |
| `PULSE_DATA_DIR` | 应用数据目录 (存放 secret.key 与 lockfile) | `/app/data` |
| `PULSE_DOUBAN_API_KEY` / `PULSE_DOUBAN_API_SECRET` | 豆瓣榜单的 API key 与签名 secret；两者都缺失时榜单接口返回 503。仓库不带默认值，凭据取自豆瓣 Android 客户端而非官方授权，使用者自担条款与失效风险 | — |
| `PULSE_LOG_LEVEL` | 日志级别 (debug, info, warn, error) | `info` |
| `PULSE_SECRET_KEY` | 32位主密钥 (未提供时自动在数据目录生成) | — |


## Prometheus 诊断采集

当前源码提供 `GET /api/v1/system/metrics`（Prometheus text 0.0.4）。在账户的 API Token 页面创建带 `read` scope 的管理员 Token，将其作为仅 Prometheus 可读的文件挂载；不把 Token 填入 URL、命令行、仓库或日志。成员的 `read` Token 无权读取全局指标，管理员撤权、scope 删除、Token 撤销或过期后下一次请求拒绝。成功与错误响应均不缓存。该接口随应用HTTP提供，不另开匿名监听端口。

```yaml
scrape_configs:
  - job_name: pulsemedia
    metrics_path: /api/v1/system/metrics
    scheme: https
    scrape_interval: 30s
    scrape_timeout: 5s
    authorization:
      type: Bearer
      credentials_file: /run/secrets/pulsemedia_metrics_token
    static_configs:
      - targets: ['pulsemedia.example:443']
```

将目标替换为部署的 HTTPS 入口，并配置其受信任 CA；同机受控网络也可使用现有内部 HTTP 地址。指标不需要新的应用配置开关或额外数据库权限。采集仅查询现有业务表和PostgreSQL系统观察视图，不启动 worker、不发下载器请求、不重试任务。

- `pulse_http_requests_total`、`pulse_http_response_bytes_total` 和 `pulse_http_request_duration_seconds` 使用注册路由模板、方法、状态码标签；未匹配为 `unmatched`，未知方法为 `OTHER`。不含账户、任务UUID、查询参数或原始URL。计数器随进程重启归零，使用 `rate()`；直方图包含流完整寿命，延迟告警应排除 SSE 路由。`pulse_http_requests_in_flight` 包括当前采集请求和开放中的流。
- `pulse_db_pool_*` 提供总量、空闲、已获取、配置容量及获取/空池等待/取消计数和秒数。它反映应用连接池，不代表 PostgreSQL 锁等待。
- `pulse_db_lock_waiters` 是当前数据库、应用登录角色的正在等锁连接数；`pulse_db_lock_wait_age_seconds` 从最老未授予锁的 `pg_locks.waitstart` 起算，等待消失后为0。无SQL/PID/对象标签，不需授予 `pg_read_all_stats`；其它角色/数据库不在统计范围。使用 `SET ROLE` 时仍按连接登录角色统计。统计是顺序采样，短等待可能落在采样间隔之外，不能据零值证明从未发生锁等待。
- `pulse_queue_tasks{queue,state}` 仅统计 job/outbox 的活动和失败状态，排除已成功历史；`pulse_queue_ready_age_seconds` 从可执行任务的 `not_before` 起算，未来任务为零贡献；`pulse_queue_expired_leases` 只提示过期逻辑租约，不能证明物理调用结束，不可用于自动清理占位或重发外部动作。
- 数据库采集限时两秒；失败返回503，Prometheus 的 `up` 会变为0，不能把这次失败当作队列清零。指标不写审计成功账本或命令回执；正常认证的凭据使用时间仍按既有认证合同更新。

当前出口还提供以下预算与阶段观察：

- `pulse_indexer_budget_endpoints{state}` 汇总当前配置的索引器时间门槛：`cooldown` 为 `rate_limit_until` 尚未到期，`interval` 为仅 `next_query_at` 尚未到期，`open` 为两者均已到期或未设置。冷却优先于间隔，三种数量互斥；离线/不可用的索引器仍按时间事实计数，`open` 不代表健康、权限、租约或物理锁检查通过。`pulse_indexer_budget_delay_seconds` 为所有索引器现有时间门槛的最大剩余秒数；配置为空或门槛均已到期时为0。指标不导出端点名称、地址、UUID或配置版本，不改变预算，也不请求集成。仅覆盖索引器共享持久预算，下载器并发槽和元数据内存预算尚未导出。
- `pulse_work_stage_in_flight{stage}` 与 `pulse_work_stage_duration_seconds{stage}` 覆盖 `download_submission`、`download_reconcile`、`download_observe`、`library_scan`、`download_organization`、`library_reuse` 六类生产worker调用，`download_observe`包括轮询和普通下载目录监听的观察回调（Kind=ordinary），监听排队和目录事件消费不计入。直方图桶为0.1/1/5/30/120/600/3600秒及+Inf，count是已返回调用数，sum是这些调用的累计耗时。包括调用内部预算等待、事务和物理I/O，不包括队列驻留、后续worker级job记账或轮询睡眠；不是纯网络耗时，也不是业务成功率，unknown/review/error返回均可能计入。扫描缺失确认、专用TV/升级worker、人工直调用例等路径不在覆盖范围。
- 阶段指标实例归App所有，重建App后归零；读取与并发更新使用短锁，不在I/O期间持指标锁。逻辑取消后物理调用未返回时仍计在途，返回后才增加耗时样本。长时间未返回的调用暂不进入直方图，需结合在途数与活动状态判断。阶段和DB观察为顺序快照，一次DB子采集失败仍整体返回503。
- 新增 `person_follow_schedule`、`douban_wish_schedule`、`cookiecloud_schedule` 三类调度阶段，采用同一在途和耗时指标。`pulse_work_stage_failures_total{stage}` 记录失败调度周期数，不是失败配置数量；调用取消不计失败，App重建清零。通过 `increase(...[5m])` 观察近期失败，不能直接把累计值当成当前故障数。
- 生产扫描整批最多30秒，单项事务最多15秒。失败配置按当前版本等待5/10/20/40/60秒后重试，最高1分钟；保存新版本可立即重新检查。实例内最多4096项，容量耗尽会淘汰最早可重试项；重启清空本地冷却，不能用重启代替数据库恢复流程。失败日志每个worker每分钟聚合一次；排查具体对象应使用现有配置与审计页面，日志和指标不携带账号、配置ID或密文。
- `PulseMediaSchedulingFailures` 示例在近期失败持续10分钟时提示检查配置、权限和数据库；采集不可用时抑制。`mise exec -- task test-controlled-mixed-load` 使用私有PG、本地协议服务和临时数据运行30分钟，覆盖池压力、持久投递、清理、指标和App重建；这是受控本地验收，不能替代真实下载器/NAS或多日生产观察。

例如按阶段查看最近10分钟P95：`histogram_quantile(0.95, sum by (le, stage) (rate(pulse_work_stage_duration_seconds_bucket{job="pulsemedia"}[10m])))`；按实例排查时将 `instance` 保留在聚合维度。无返回样本时分位数可能为空或NaN，应显示暂无样本，不当作0秒。预算计时门槛是正常节流机制，不直接作为故障告警。全端点并发/预算和全业务路径耗时尚未覆盖。

告警示例见 [pulsemedia.rules.yml](prometheus/pulsemedia.rules.yml)，复制到Prometheus可读目录后，在其配置中加入 `rule_files: ['/etc/prometheus/pulsemedia.rules.yml']`。示例匹配 `job="pulsemedia"`，改采集job名时同步修改规则。阈值是运维起点：采集失败持续2分钟、连接池占用超过90%持续5分钟、可运行任务最老延迟超过300秒持续5分钟、最老锁等待超过5秒持续1分钟、过期逻辑租约持续5分钟；按本机负载调整，不能视作产品SLO。

业务告警要求同一job/instance的 `up == 1`，采集失败时只触发不可用告警，避免沿用旧读数判断业务故障。完全移除采集target后不会继续产生 `up`，需要部署方对预期target缺失单独监控。过期租约告警只提示查看原任务与物理执行证据，不执行解锁、释放占位、重发、终止会话或其它自动修复。连接池占用包含采样波动，锁指标只观察应用登录角色，使用时结合日志核对。

运行 `mise exec -- task test-metrics-rules` 校验规则及触发/恢复行为。验收工具的官方镜像摘要固定在 `.mise.toml` 的 `PULSE_METRICS_PROMETHEUS_IMAGE`；仅测试容器使用，不加入应用镜像。测试使用禁网只读容器，首次需要拉取镜像；夹具覆盖持续异常、恢复归零、采集失败对旧业务指标的抑制、正常状态及其它job隔离。

## 5. Beta 候选与构建记录

历史`0.1.0-beta.1`范围仅为开发计划M0–M4；本节下方另记覆盖M0–M11的`0.1.0-beta.2`远程预发布。正式标签构建须来自已提交且无修改的源码，版本信息和OCI revision必须追溯到同一个提交。历史构建示例：

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


<a id="beta-release"></a>
### 0.1.0-beta.2公开预发布

覆盖M0–M11的[预发布v0.1.0-beta.2](https://github.com/dalamudx/PulseMedia/releases/tag/v0.1.0-beta.2)已于2026-10-24公开。标签和两个正式镜像固定源码`06ed3164f76c345e5c28cd4d4db186a6fadc88a5`；与CI通过的`28cc0fe`仅差验收文档，Go1.26.10、迁移目标schema126。amd64原生及arm64 QEMU分别完成安装、schema82→126升级、加密备份/恢复隔离、初始化登录、只读根和PG18卷重建；arm64原生硬件性能仍未量测。

无需注册表凭据可从Release下载本机架构的OCI归档及SHA256SUMS，再加载：

```bash
sha256sum --ignore-missing --check SHA256SUMS
podman load -i pulsemedia_0.1.0-beta.2_linux_amd64.oci.tar.gz
export PULSE_APP_IMAGE=localhost/pulsemedia:0.1.0-beta.2-amd64
```

arm64相应替换文件名和镜像后缀。加载不执行迁移或启动实例，后续安装/升级仍遵守第1–3节的独立DDL凭据、备份、空库与恢复点检查。归档SHA256：amd64为`fd2968ef8bf8432a1bd1143791c99dca7a456850de64c5c6e513905a205460c5`，arm64为`7094e3a7fbad35fb7ec92e9de7643f2a30b5f2ef97e518ce8fab4cec3eef4ef3`；已核对GitHub服务端摘要、归档重新加载身份、匿名校验文件与下载访问。

GHCR的`ghcr.io/dalamudx/pulsemedia:0.1.0-beta.2`也已推送，多架构索引为`sha256:cc486ac8e645d33b412e0fbd5e12be0f048effa044d2d3e2d2102232ef70d4ed`。该包目前为私有，需有包读取权限的GitHub身份登录；匿名用户应使用上述公开Release归档，不能将私有包描述成可匿名拉取。

## 6. M5 开发切片与站点管理验证

M5 从 schema84/85 开始引入完整执行 URL 加密、明确协议和版本化站点命令，这些能力包含在 beta.2 中；历史 beta.1 的 revision 和范围保持原记录。App 在开放 HTTP 与 worker 前完成旧站点配置转换，须使用原主密钥，错误密钥会阻止启动。离线升级与备份恢复继续遵守前述恢复点、凭据和隔离规则。

站点修改必须携带读取到的配置版本；省略 URL 或凭据表示保留，显式空凭据表示清空。列表只显示安全地址。删除会停用来源并保留下载、候选与升级历史。丢失响应时通过原键安全回执恢复；浏览器恢复记录仅含命令元数据，不含完整地址或凭据。

运行 `mise exec -- task test-sites-ui` 验证公开 Cookie、真实 Torznab、PG18.6 和 Chrome；可加 `-- http` 或 `-- browser` 只跑对应路径。脚本通过已有 Podman 包装器创建固定 PG18 测试环境，将构建、浏览器和测试临时文件放在独占 `/data/temp` 目录并在结束后清理，不读取个人部署配置或访问已配置的真实站点。


## 7. CookieCloud 源码配置与运行边界

CookieCloud 配置使用管理员 Cookie 会话访问 `/api/v1/integrations/cookiecloud/config`：GET读取安全配置，PUT携带原键、`expected_version`、`enabled`、`interval_seconds`及可选的`connection`元组（`server_url`、`user_key`、`password`）。首次保存使用版本0；省略连接表示保留现有密文，null不能代替保留。所有响应`private, no-store`，只返回服务origin、版本和连接已配置标记。丢失保存响应时GET `/api/v1/integrations/cookiecloud/config/commands/{command_key}`恢复原回执。M10-07在设置页提供管理员CookieCloud同步卡片：填写连接元组和60至86400秒间隔，保存并启用后由后台按计划执行；更换连接需显式勾选，保留连接时可以离线停用。已存连接不回显，浏览器恢复记录只留原请求键、版本和摘要，不含连接明文。保存回执不等于同步结果；结果不明时先查询原键回执，不自动再次保存。

保存配置只确认持久化，不立即联网或创建同步任务。启用配置到期后，由App的单个后台消费者从所配置URL前缀的`GET /get/{user_key}`读取一次密文；原始密码和派生口令只用于本地解密。支持官方legacy CryptoJS/OpenSSL带盐格式及`aes-128-cbc-fixed`，拒绝明文备份、未知格式、跳转和无界响应，不自动重试同一任务。连接信息及站点Cookie分别使用原应用主密钥信封加密，升级必须保留原主密钥。

自动关联限于既有、启用且明确为NexusPHP协议的站点；不会将Cookie覆盖到Torznab API key或RSS凭据，也不创建站点。Cookie必须满足目标域、主机限定、HTTPS、根路径和原生有效期等事实；路径专属、分区、事实缺失或同名冲突不扩大授权。同值保留原密文字节，零匹配和读取失败保留旧凭据。同步成功会推进实际变化站点的配置版本，因此尚未发送的旧授权需要按现有配置版本保护重新核对。

schema109保存原同步任务和执行证据，schema110追加与站点版本及凭据密文摘要绑定的有效期记录。原生执行拒绝已过期的同步凭据；证据读取失败返回不可用。session Cookie不编造截止时间，手工更新的新版本不继承旧版本期限。旧Cookie过期不会阻止合法的CookieCloud刷新。取消或续租失败会等待实际HTTP调用返回后才释放物理许可；结果无法确认或中断的任务进入`review_required`时，修改启停、周期或配置版本不会解除原执行保护。

运行 `mise exec -- task test-m10-cookiecloud-config`复验36项配置与协议基础检查，运行 `mise exec -- task test-m10-cookiecloud-sync`执行56项完整功能检查和另13项兼容检查；两者严格拒绝跳过并清理自有PG18.6与临时资源。当前原生App验证使用自有CookieCloud加密HTTP和NexusPHP服务，未访问真实私有PT账号；具体验收状态以开发计划为准，不改变已记录的V1 Beta镜像验收范围。

设置页另有管理员「做种策略」卡片，明确选择下载器后配置any/all、原生做种秒数、分享率或低剩余字节阈值。空间须绑定原任务下载路径；空白阈值不参与，至少填写一项有效正阈值。文件处理固定保留全部文件，历史缺证及专用任务保护不因界面配置而放宽。保存绑定下载器当前版本，不执行移除；下载器变化或版本冲突须重新读取并核对原回执。通知与投递沿用已有图文通道及测试按钮，202仅代表持久排队。

M10-07正式复验入口为`mise exec -- task test-m10-ecosystem-settings -- all`，支持`frontend`和`browser`分模式。前者检查原键恢复/持久化失败、读错误恢复、做种阈值与通知命令；后者用真实Cookie与PG18.6验证保存、丢回执重载、离线停用、端点CAS冲突、账号隔离和受保护表快照，四宽六图审阅后才放行清理。当前界面验收不启动后台worker，也不会请求配置的CookieCloud、站点或下载器；后台执行与送达证据分别见M10-01/03/04。

M10全量复验使用`mise exec -- task test-m10-regression -- base`及`usecase`；较长组建议分片运行：`runtime 1/16`至`runtime 16/16`、`http 1/16`至`http 16/16`、`postgres 1/24`至`postgres 24/24`。每片均从当前源码生成完整父测试清单，按固定余数分配；收齐同组全部片并校验清单摘要一致、无遗漏或重复、通过与门控跳过之和等于选中数，才算完整覆盖。门控跳过单列，不计执行成功。按每条任务一至两片设置外层时限，避免长组被执行器提前终止；超时或取消后须核对其子进程及自有PG容器清理，不能仅凭外层临时目录已删除判断清理成功。生态联动测试`TestM10EcosystemIngestionRefreshNotifySeedingPG`验证同一真实入库事实经批成员关联通知、Emby精确路径刷新和一次保留文件移除；原生服务为自有协议夹具，当前验收状态见开发计划。


M11全量复验使用`mise exec -- task test-m11-regression -- base`与`usecase`；运行时为`runtime 1/16`至`16/16`、HTTP为`http 1/16`至`16/16`、PG为`postgres 1/32`至`32/32`。按当前源码枚举完整清单，逐片核对同组摘要、选中数、父项结果和自有资源清理；跳过单列，不计通过。28cc0fe当前清单分别为runtime484、HTTP513和PG855父项，base904和usecase550父项，完整统计见开发计划。单包预算为10分钟；原HTTP第14片可拆成`http 14/32`与`http 30/32`，二者无交集且并集等于原`14/16`，此时不要再累计原片结果。超过预算的运行与详细回执被截断的运行不能按外层退出码记成功，须补齐清单并核实清理。并发幂等和过期边界测试的私有连接池可等待15秒锁，生产仍使用2秒锁等待；无需修改部署配置。原生联动的配置、权限、元数据身份、入库与恢复、个人推送证据见M11-01–07各正式入口，旧生态原型只作兼容检查。功能计划98项、源码托管验收与正式镜像发布分别记录，不能相互替代。


托管CI的质量检查与race回归分别执行。回归矩阵复用上述当前源码清单入口：base、usecase、runtime16片、HTTP17个执行片（原14片拆为两个子片）、PG32片，共67个任务，最多并行4个；每个任务使用自有PG18容器与/data/temp下独立路径。质量任务另执行前端行为测试、类型、生成、依赖/漏洞、构建与认证UI检查，二进制输出到任务临时目录并在结束时清理。源码`28cc0fe4fdb2e3f148933299319fea7c268df68f`的[托管运行40118090897](https://github.com/dalamudx/PulseMedia/actions/runs/40118090897)实际68/68任务成功，独立任务日志已核实3306父项（3128通过、178门控跳过）与清理回执；仅更新验收文档的后续提交不改变此源码绑定。若合并日志接口不完整，需按job独立下载，不能忽略缺失分片。镜像安装/升级冒烟的目标版本沿postgres.CurrentMigrationVersion，schema82只用于历史升级种子与旧恢复点拒绝验证，不再把当前镜像锁在schema83。正式发布仍须采用对应已提交源码和独立镜像验收，不将本次源码/CI通过当作已发布。
