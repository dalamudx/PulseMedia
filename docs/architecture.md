# PulseMedia 系统架构与实施基线

本文是项目唯一的架构设计规范，统一产品范围、分层、领域与数据约束、运行协议和接口。开发计划与进度单独维护在 [development-plan.md](development-plan.md)，不在本文重复记录。采用当前架构收敛方案；不保留平行草案、复审清单或旧 ADR 作为另一套技术规范。

**本文是目标实现合同，不是已交付功能清单。** 当前任务、阶段与验证进度见[开发计划](development-plan.md#progress)。代码落地后，SQL 细节以 `migrations/` 和 sqlc 查询为准，HTTP 字段以 `api/openapi.yaml` 为准；架构变化同步更新本文。

## 导航

- [1. 定位与范围](#scope)
- [2. 整体架构](#architecture)
- [3. 依赖与事务边界](#dependencies)
- [4. 领域模型与拥有语义](#domain)
- [5. 搜索与候选决策](#search)
- [6. 下载占位与共享获取](#acquisition)
- [7. 媒体库扫描与文件整理](#storage)
- [8. PostgreSQL 数据模型](#database)
- [9. 持久任务与 Outbox](#reliability)
- [10. 外部集成](#integrations)
- [11. 权限与安全](#security)
- [12. HTTP 与实时接口](#api)
- [13. 前端边界](#frontend)
- [14. 工程与测试](#engineering)
- [15. 运行、迁移与备份](#operations)
- [16. 实施边界与验收](#implementation)
- [17. 验证边界](#verification)

<a id="scope"></a>
## 1. 定位与范围

PulseMedia 是 Go 驱动的自托管影视自动化中枢，把用户想看的作品可靠地变成媒体库中的文件。发现、订阅、资源搜索、下载编排、文件整理和媒体库同步由它负责；播放、转码、流媒体分发与观看记录交给媒体服务器。

| 能力 | 内置支持范围（具体授权边界见第10节） |
|---|---|
| 媒体 | 电影、剧集；动漫按剧集处理 |
| 元数据与发现 | TMDB 主身份；TVDB 补充；Fanart 艺术画；豆瓣榜单/公开想看、Bangumi 周历 |
| 索引器 | Torznab（Prowlarr / Jackett）、明确支持的 NexusPHP 与 DMHY 协议；模板不等于站点通用适配 |
| 下载器 | qBittorrent、Transmission、rTorrent HTTP XML-RPC；各业务能力分别校验 |
| 存储 | 本地文件系统，含已挂载 NAS；动作按实际文件系统能力开放 |
| 媒体服务器 | Jellyfin、Emby 精确路径刷新/可选确认；Plex 局部库刷新 |
| 通知 | Webhook、Telegram、企业微信、钉钉、飞书、Bark；显式启用的个人 Web Push |
| 用户 | 共享家庭库，管理员、普通成员与受限 API Token |
| 部署 | 一个 Go 应用实例 + 独立 PostgreSQL 18 |

**非目标**：插件加载/市场、AI Agent/MCP、音乐、通用站点爬虫/动态规则包、云盘/对象存储、多应用实例，以及 Redis、外部消息队列和业务微服务。只借鉴 MoviePilot 系列的业务思路，不复制代码、资源文件或站点二进制。

原则：可信优先于自动；不确定状态明确展示；每个决策有原因；破坏性操作需确认；配置修改可预览；待处理事项优先于装饰性统计。

<a id="architecture"></a>
## 2. 整体架构

```text
浏览器 / API 客户端
        │ REST + SSE
┌───────┴──────────── 一个 Go 应用进程 ──────────────────┐
│ HTTP / 内嵌 React 19 前端 · 认证 · 权限 · 限流             │
│                    │                                 │
│ 类型化业务命令 / 查询服务（共享幂等与审计机制）          │
│                    │                                 │
│ 发现 · 订阅 · 搜索 · 下载 · 整理 · 媒体库 · 通知/系统    │
│                    │                                 │
│ 纯领域规则：身份、解析、质量、评估、命名、状态推导       │
│                                                      │
│ 调度器 → 持久任务 Runner → 对账/观察恢复                │
│ Outbox Dispatcher → 通知 / 媒体服务器刷新               │
│                                                      │
│ pgx/sqlc 仓储      外部协议适配器      根目录操作队列    │
└───────┬──────────────────┬─────────────────┬──────────┘
        │                  │                 │
 PostgreSQL 18       TMDB / TVDB / Fanart      磁盘 / 挂载 NAS
                     Torznab / NexusPHP / DMHY
                     qB / TR / rTorrent
                     Jellyfin / Emby / Plex
                     CookieCloud / WebPush / IM
```

这是运行结构，不意味着领域层可以导入数据库或平台实现。业务模块处于同一进程，数据库为独立必需服务；采用 PG 不等于已支持多实例。

| 模块 | 职责 |
|---|---|
| discover | 元数据搜索、统一身份、作品与季集维护 |
| subscription | 用户意图、目标展开、进度投影 |
| search | 轮次、端点预算、分页、候选落库与评估 |
| download | 原子占位、外部提交、对账、共享获取需求 |
| transfer | 冻结计划、路径预览、步骤执行与恢复 |
| library | 根身份、基线扫描、文件索引、拥有事实 |
| notify/system/auth | 通知、集成健康、配置策略、身份权限与审计 |

调度器只入队，不在定时回调里执行业务。Runner 分资源池；对账器补偿下载器状态、库观察和投影。SSE 仅负责通知客户端重新查询。

<a id="dependencies"></a>
## 3. 依赖与事务边界

### 3.1 包依赖

```text
httpapi ──→ usecase ──→ domain
   └───────────────→ ports
usecase / command ──→ ports
store / adapter / platform ──→ ports + domain
app ──→ 所有实现（唯一组合根）
```

- `domain` 只依赖标准库/纯算法库，不导入 ports、其他 internal 包、数据库、HTTP 或文件系统。
- `ports` 定义使用方驱动的合同：Clock、ID、Actor、结构化外部错误，以及按业务拆分的仓储/事务/适配器接口；不依赖实现或用例包。
- `usecase` 编排业务，只依赖 domain、ports 和轻量命令执行机制；不导入 store/adapter/platform 实现。
- store 内部封装 pgx/sqlc 类型。adapter 只实现协议与能力，不负责业务决策。

普通下载发送由 `internal/usecase/download` 编排：先调用窄 `DownloadSubmissionPreparer` 取得已冻结的任务、原客户端和 Add 参数，再执行唯一一次 Add，最后以原结果完成写回。存储侧负责来源读取、解密、持久准备与锁后重验；返回的 `PreparedDownload` 不含 pgx/sqlc 或 context，完成回调使用同一任务和租约，物理预算仅由用例在外部调用及写回均返回后释放。已有操作只返回原任务，不携带发送能力；准备失败不发送，回执丢失保留原任务与保护，仅走观察恢复。专用 TV/整包/升级的通用入口拒绝仍在准备前执行。整理执行的步骤顺序、完成重放、失败和人工复核分支由 `internal/usecase/transfer.ExecutePreparedPlan` 协调，App 显式注入各实际整理构造。`TransferExecutionPreparer` 在原锁序下冻结计划/步骤、取得根物理锁并认领原执行；`PreparedTransfer` 只提供状态、步骤执行、完成、复核和释放能力，不暴露 pgx/sqlc 或保存 context。每步内部仍由 store 执行租约续期、文件身份/权限重验和窄事务；用例等实际文件调用及结果写回返回后才释放根锁，context 取消不提前释放物理独占。电影、剧集、整包、升级、跨根复用和恢复使用同一显式注入；未追踪文件的独立永久许可执行仍保留其专用路径。该提取不宣称所有存储职责已迁出，也不改变文件动作和恢复授权。

媒体库维护命令由 `usecase/librarymaintenance.Service.Command` 协调准备、动作选择、观察与成功/未知写回。仓储的 `PrepareCommand` 继续在原锁序和短事务内冻结材料、永久许可与原键，返回不含pgx/sqlc或context的 `PreparedLibraryMaintenance`；恢复回执不携带执行能力，每个新准备能力最多调用一次文件动作。用例在原生调用及独立写回都实际返回后关闭能力，存储按原顺序释放根锁与数据库会话。失败、取消或成功提交不明均保留永久许可，不能重新授权文件调用。

从业务连接池借用的长持物理会话统一通过 `AcquirePhysicalConnection`：按当前池上限最多占用一半且不超过4条，其余容量供短事务、鉴权与续租使用；小于2条的池不能承载此类执行。独立双整数PG advisory namespace共享准入，无进程全局注册表；等候者不保留池连接。逻辑取消不释放已返回的会话；资源锁释放后才归还准入，解锁结果不明则关闭原后端。原来独立直连的物理执行合同保持。

普通任务的对账、进度轮询和目录监听观察也由下载用例协调：仓储以窄准备合同冻结当前任务、原端点和身份，提供一次只读原生观察及原任务完成回调；用例选择原哈希/唯一操作标签并等待网络与写回实际返回后释放物理预算。原生失败、无结果和身份矛盾分开，结果不明不重放Add。专用TV/整包/升级任务仍经过独立拒绝与证据入口，不借普通观察端口扩大权限。
**用例层静态边界收敛（2026-10-18，schema83）**：当前17个生产用例包经实际`go list`核对，依赖仅含标准库、domain/ports及UUID等轻量库；`.golangci.yml`对整个`internal/usecase/**`生产源统一禁止store、platform、adapter、pgx、HTTP/App及跨用例实现导入，外部`_test.go`继续显式组装真实实现。39个可编译负例及合法例验证门禁覆盖原有包与未枚举新包。认证、通知、媒体刷新、调度及订阅分别以窄合同注入PG仓储和原密码/密文codec；订阅以专属intent UoW在原事务内锁定当前账户、身份/策略/订阅、根与单元，保存媒体、范围、wanted、收件人快照、事件/投递及成功审计。纯DTO使用自然UUID/时间及指针表示可空值，公开HTTP字段保持原合同；重配置保留既有paused/archived和用户跳过来源。搜索HTTP/RSS网络、分页规则、四worker与取消等待仍由用例编排，专用会话物理许可、持久预算、锁后版本重验、候选/page proof提交及最终只读一致快照由store实现。实际行为与最终候选验收记录统一维护于开发计划。
**M5元数据浏览合同（2026-10-18）**：`MetadataProvider.SearchMedia`提供movie/tv/multi，旧all和缺省映射到multi；movie的year与TV的first_air_date_year按提供者合同发送，multi在已校验媒体结果中按年份过滤，person不进入媒体结果。完整有效的空results返回分配的空数组；缺失/null/损坏结果、错误ID/类型或季集材料不得当无匹配。浏览解码与电影身份搜索的完整性、缓存合同分离，不借浏览结果重签身份。电视剧详情校验请求ID与季列表；每季编号唯一、集数非负，season0特别篇保留在列表，`seasons_count`和`episodes_count`只汇总普通季并核对提供者摘要。公开读取只有用户认证失败返回401，真实作品不存在返回404，上游认证配置/限流/超时/坏响应返回安全503；无匹配200数组。未配置或上游凭据失效时保留本地目录回退，经`LocalMediaSearchReader`窄端口和sqlc在当前账户优先锁定后以RR只读快照筛选类型/年份/字面标题、确定排序并返回可信TMDB身份；HTTP不写此查询的SQL，读库失败不伪装空数组。新读取不创建任务、审计成功账本、媒体或占位，API类型和季集计数以OpenAPI维护。

**Bangumi每日放送元数据与发现页周历（M10）**：`BangumiCalendarProvider`只读官方`/calendar`，保留周一至周日分组、中文名称与原名、真实首播日期和可空评分。首播日期不当作单集播出日期，未知日期/未评分不编造值；Bangumi subject ID与TMDB身份隔离，`GET /api/v1/metadata/bangumi/calendar`固定`execution_allowed=false`，不创建订阅、任务、媒体或占位。客户端限制10秒与2MiB、不跟随跳转、不带Cookie或API口令，拒绝截断/重复JSON、无效日期/身份/类型和不完整周；完整有效空数组与失败分别处理。封面只保留官方lain.bgm.tv的cover路径并升级HTTPS。应用维护5分钟单份有界缓存，返回深复制；过期上游失败不回退过期成功，并发共享一次读取，等待者取消不取消或重复其它调用的原读取。星期与中日标题筛选在缓存上做，标题匹配使用NFKC和Unicode case fold。正式入口复用当前账户的metadata读取鉴权，缓存不能绕过当前撤权；query严格拒绝重复/未知字段，所有成功及错误响应private/no-store。M10-06在现有发现页提供独立周历来源，七日/整周及本地中日名称筛选不发命令；失败或刷新先撤旧周历与候选。追番仅发起既有只读TMDB电视剧搜索，真实首播年份可作为搜索条件，未知年份省略；Bangumi编号从不填入TMDB字段，搜索候选不声称跨源身份绑定。用户显式选择TMDB作品后，既有详情/稀疏季集/目标根/策略/端点审核与订阅创建事务重新验证权威资料和权限。切换账户/角色/路由卸载旧面板，关闭候选或切换星期/名称取消旧搜索并拒绝迟到回调；未确认作品、候选错误或读取失败不提交订阅。保存沿用原命令持久化、同键回执与恢复合同，不由周历另造自动订阅入口。

**M5站点配置合同（schema84/85，进度以开发计划为准）**：完整执行URL和凭据分别使用原主密钥信封加密，`indexer.url`只保留scheme/host安全投影；协议显式持久化为Torznab、NexusPHP或RSS，不以凭据语法选择新配置协议。schema84扩展保持旧Indexer行形状，既有来源逐行锁定转换，原配置版本、时间、健康/预算、凭据密文字节不变；旧行为版本只在一次迁移时分类。冻结摘要按原逻辑URL还原，不修改既有authority、回执和受保护业务图。App在开放HTTP/worker前完成转换；错误或缺失解密器、坏密文在创建网络客户端前拒绝。schema85站点管理以当前管理员会话优先锁定、原键和必填配置版本控制窄事务；配置、审计、事件及稳定回执同事务。更新URL/凭据缺省是保留，显式空凭据是清空；类别为明确数组。删除递增版本并停用来源，保留原来源、候选、下载与升级历史，执行读取及健康排队拒绝墓碑来源，历史安全回执仍可恢复。手工探测在持有物理许可后的各预算事务内再次锁定当前账号，再锁端点；后台探测仍以原job owner/token/租约授权。页面只缓存actor/key/action等命令元数据，通过只读安全回执接口恢复，不缓存完整URL或凭据；健康标签来自持久观测。RSS总配置版本可以推进而来源内容版本保持原值，解密读取允许内容版本不大于总版本，后续管理更新仍要求当前总版本；URL与凭据未替换时保留原密文。成功回执确认后立即刷新列表，在新列表提交前保持操作锁，避免使用旧版本继续修改或删除。

**CookieCloud端到端协议与凭据边界（M10）**：生产读取从配置服务的`GET /get/{key}`取得加密备份，密码和派生口令只用于本机解密，不发送到服务端。缺省或`legacy`为CryptoJS/OpenSSL `Salted__`加8字节salt封装，口令为MD5(`key-password`)前16个十六进制字符，按上游EVP_BytesToKey/MD5派生独立AES256 key与IV；`aes-128-cbc-fixed`使用该16字节口令和16字节零IV解密原始AES128-CBC。此MD5仅为兼容上游协议，不替代本地应用主密钥信封。两模式均严格校验有界base64、分组长度、完整PKCS7和UTF8，未知算法及明文cookie_data拒绝为有效加密包，错误不包含密码、密文或Cookie。解密完成不授予站点更新权限：只关联既有明确站点，保留Cookie的domain/path/secure/hostOnly/过期作用域，更新仍需当前账号权限、站点版本与配置/租约围栏，配置、成功审计/事件和原键结果同事务；未匹配或未返回Cookie不能当作清空凭据授权。网络I/O在事务外；本机解密和纯作用域筛选不产生外部I/O，写回事务使用数据库时钟重验作用域与有效期；不将明文凭据写入日志、响应、缓存或浏览器恢复记录。

**CookieCloud配置与共享凭据（schema108）**：`/api/v1/integrations/cookiecloud/config`的GET/PUT及`/commands/{command_key}`原回执GET仅允许当前管理员Cookie会话，配置按owner隔离，API Token scope不能替代该权限。连接URL前缀、user key和密码作为完整元组用应用主密钥加密，响应仅给origin/配置版本/已配置标记；保存缺省连接保留原密文字节，不能用null或缺失结果清空。当前账号、原键与配置版本在窄事务内锁定复核，配置、成功审计/事件与回执原子提交，强制延迟约束后重查权限/完整新行/原回执；配置保存本身不排队或访问网络，所有响应private/no-store。历史原键先恢复，不依赖当前配置表可读或重新加密连接。同步的站点凭据会被多个原生端点共享，因此只选对站点根路径有效的Cookie，不将某单路径授权放大；保留已选持久Cookie最早到期时间供写回的数据库时钟复核，session记录不编造期限。失败或零匹配保留旧凭据，不创建站点、订阅或下载。

**M10设置页配置与恢复边界（M10-07）**：CookieCloud、做种策略与通知投递共用现有设置页导航，只对当前管理员挂载管理卡片。CookieCloud连接只在表单和待确认原命令的内存中存在；localStorage仅保存原键、owner、scope、版本与输入SHA256摘要，查询结果不回显连接，关闭/切账号卸载旧表单并取消迟到回调。保存成功后重新读取权威配置，省略连接保留原密文，停用不依赖远端可达。做种卡片必须显式选择下载器，以其当前更新时间提交CAS；规则只有any/all和有效正阈值，delete_files固定false，文件处理不能由界面改为删除。下载器列表重读期间卸载旧规则卡片，端点变更后旧键仍只恢复原回执，不补造新许可。恢复信息先落浏览器存储再PUT；存储失败不发送，未知响应不自动重试，先GET原键回执，确认缺失后才能明确同键同输入重放，400/403/404/409/412保留原请求并进入只读核对。读失败撤就绪并锁写，重读成功仅清除已失效的读错误，不覆盖原键回执反馈。通知测试继续复用M10-04的202排队回执与原事件恢复，不将配置保存或测试排队显示为同步/移除/送达成功。

**CookieCloud调度、写回与有效期（schema109/110）**：配置按owner持久调度，使用既有job队列，单次任务只读取一份加密备份。job/attempt/schedule、成功审计与事件原子排队；执行证据绑定原job的owner/token/backend PID，物理gate未释放时不能因逻辑租约失效或编辑配置重新读取。无法确认既有执行时保留review_required。App单消费循环复用续租、取消和实际I/O等待，退出前等待该循环结束。写回事务按站点ID顺序取得共享indexer物理门禁，仅修改明确NexusPHP站点的Cookie密文和来源/配置版本，保留URL密文、分类和历史行为；同值不重写原凭据，未匹配或作用域冲突保留原值。站点凭据、逐站成功审计/事件和同步任务完成同事务，强制延迟约束后再次校验当前管理员、配置、完整来源绑定、物理gate、租约与最早过期时间。
CookieCloud调度逐账号独立窄事务，账号锁内复核配置和到期，job/attempt/schedule及成功审计/事件同事务；单项解密或尾写失败不回滚已提交健康账号，也不阻断后续账号。每项事务最多15秒；保留原到期排序，以(扫描时间,owner_id)实例内游标循环，每次最多64项、最多两次候选读取，忙扫描让出，取消不推进未完成项，重建从起点重扫。失败排队仍返回首错供脱敏告警，但恢复成功后可认领既有任务；恢复失败或取消仍禁止认领。数据库去重、当前权限、原物理gate及review_required保护保持权威，游标不携带执行授权。

schema110按原job、站点、来源版本和密文SHA-256追加不可变有效期证据，不保存明文Cookie；session事实不编造过期时间。原生执行解析仅使用与同一来源版本及密文摘要匹配的证据，过期返回需重新鉴权，当前合同下证据表故障返回不可用，不当作无限有效。历史schema无该合同及未经同步的手工版本保留原行为；手工更新的新版本不继承旧版本有效期。CookieCloud刷新仍经过生命周期、协议、权限、配置和许可校验，但不因旧Cookie过期而禁止替换它。证据提交绑定原同步任务、当前物理门禁和有效租约，非空历史禁止降级、更新、删除或截断。实现与验收状态以开发计划为准。

**M5交互聚合搜索合同（schema86，进度以开发计划为准）**：发现页和原订阅审核通过显式POST创建账号所有的观察会话，冻结查询、可选media/subscription范围、质量策略、全部启用且未删除来源的配置版本及30秒截止时间；未知/离线健康不排除交互来源，自动补缺/RSS仍保留原健康门槛。每会话最多64来源、每账号最多4个活动会话，原键命令、来源job、成功审计与事件原子提交。App持有四个受控worker的取消与等待，复用端点物理许可、持久预算、候选及分页证明，每次外部同步请求限2秒，续页窗口8秒、最多4页；真实调用未返回时，取消、过期或失租约只结束逻辑状态，不释放物理独占。每页开始与提交末尾核对当前账号/对象、原job owner/token/有效租约、来源版本和会话截止；迟到结果整体回滚。GET仅以当前账号、对象范围和所有权读取RR一致快照及稳定原键回执，不联网、不产生任务或预算；旧GET搜索入口返回400且零执行。候选按冻结策略从有效持久质量事实评分，质量分降序、最新持久做种数降序、UUID升序，全局排序后最多展示500项，明确partial/truncated和latest_persisted；未知/不完整事实不推断高质量。前端保留已返回候选并显示剩余来源，读取失败标旧并暂停新获取，失权清空；命令恢复只缓存账号/作用域/原键等元数据，不存查询正文或秘密，未知回执404不自动重建POST。电影仍进入原根/下载器/策略、预览/确认的获取合同，交互候选和观察会话不授予下载或文件执行权限。

**M5发现页候选交接合同（进度以开发计划为准）**：本地订阅列表只在实际movie/tv对应的精确TMDB命名空间恰有一个身份时提供canonical、JavaScript安全整数范围内的`tmdb_id`；缺失、跨类型或歧义身份不猜测。发现页按类型、TMDB身份和当前用户匹配TV订阅，多根明确选择，重新读取详情/状态后只导航到订阅UUID。候选引用绑定当前账号、原搜索和本人订阅，仅在React上下文内保留五分钟；账号/角色变化、关闭、消费或到期清除，不将候选、查询正文或许可写入URL/history/浏览器存储。进入原编号/候选审核只预选UUID，既有未确认搜索、单集或完整覆盖命令优先，不自动解释、勾选、审核或获取。实际单集/完整覆盖继续使用原持久审核、独立确认、原键和后台当前权限/来源/输入戳围栏。目录请求以分类/位置/账号和单次请求身份隔离，忽略取消的迟到响应不得覆盖当前结果；题材只取可信标签，不从标题或简介推断。订阅列表与选中详情在账号/角色变化时立即掩蔽旧值。
关闭资源观察弹窗或交接到订阅审核只停止本地轮询，不在路由卸载时另发取消命令；显式「取消原搜索需求」仍使用持久原键和回执恢复，未知结果不得自动重放。

**获取边界（2026-10-18，schema83）**：`ports.AcquisitionRepository`只暴露plain命令、结果、冻结轮次、job authority及typed errors，`usecase/acquisition.NewService(repo)`不接受实现或交易。App显式注入`postgres.NewAcquisitionStore(pool, ID, Clock, acquisition.SelectMovieRound)`；纯选择器实际执行候选识别、冻结策略评估和最高分选择，仅接受accept、严格大于才替换，同分保留输入首项。自动候选沿查询ID顺序、手工候选沿持久CandidateIds顺序，不增加cutoff门槛，未知质量继续hold。store在原交易内读取锁定材料及来源/阻断资格，将规则和身份复制后交给同步选择器，并以回调不可修改的可选ID集合复验返回值；nil选择器或越界结果失败关闭。原根/unit/wanted、策略/订阅/job/finalizer锁、胜出候选FOR SHARE及排序端点锁、版本重验、占位/需求/提交任务、收件人快照、aggregate seq、事件投递/成功审计/回执和执行job仍同事务提交。公开Acquire/Finalize及显式电影selection UoW保持，外部metadata网络在事务外，不以单独EnsureMedia提交替代获取原子性；无文件/网络I/O藏入选择回调。验证与当前候选镜像以开发计划记录为准。

**显式电影获取实际切片（2026-10-09，schema37）**：用户会话显式选择 TMDB movie ID、候选、电影根、下载器与策略，元数据网络读取在事务外；只读预览返回当前 input stamp。确认命令复核可信作品/年份、单 movie 覆盖、近期成功查询关联、来源配置、客户端 digest、根/基线/事实/策略；大小证据也进入冻结摘要。store 窄 UoW 同事务创建尚不存在的作品身份/单元、操作、占位、需求、任务/job、事件/审计和稳定幂等回执；失败整体回滚。锁序与轮次收口一致；缓存重放先复核用户/对象权限，不再访问元数据网络。此人工入口仅支持 user 会话，不接受带 scopes 的 API 凭据或内部 system actor；认证拒绝 null/非法 scopes，不能将其伪装为会话。发现页选择根/下载器并核对预览后确认；未确认命令按用户保存原请求与 key，读取不丢命令，未知清除旧允许预览。202 仅为持久任务，不代表下载器回执。真实 App.Run 单电影硬链接/通知闭环及浏览器提交后丢回执、同 key 恢复/唯一任务均已验证，端点为本地协议夹具，非真实 NAS/qBittorrent 授权联调。TV、包与 upgrade 仍拒绝，不以编号映射发布视为自动获取授权。

- platform/jobs 的处理函数由 app 显式注册，不导入用例。禁止 `init()` 自动注册和全局服务定位器。
- HTTP 不写 SQL。普通只读查询可使用窄投影仓储，不强迫加载完整聚合。

### 3.2 窄事务端口

每个业务边界使用自己的 UoW，不建能访问所有表的万能 Tx。以下为获取业务的合同示意，DTO 由 domain/ports 定义：

```go
type Tx interface {
    LockAndLoad(ctx context.Context, req Request) (Eligibility, error)
    CreateAcquisition(ctx context.Context, plan Plan) error
    ReserveUnits(ctx context.Context, plan Plan) error
    Enqueue(ctx context.Context, job Job) error
    Publish(ctx context.Context, event Event) error
    CompleteCommand(ctx context.Context, result CommandResult) error
}

type UnitOfWork interface {
    Within(ctx context.Context, fn func(Tx) error) error
}
```

LockAndLoad 按协议锁定有关对象并读取最新事实。store.Within 开启 pgx.Tx，内部绑定 sqlc.WithTx；回调失败整体回滚。不向用例暴露驱动对象，不通过 context 偷传事务，不依赖类型断言。

一次业务写入的边界为：

```text
当前权限/版本校验 → 最新事实与业务排他校验
  → 业务记录 + 占位 + job + 必要事件 + 成功审计 + 幂等结果
  → 同一短事务提交 → 后台执行外部动作
```

**网络和文件 I/O 均在事务外。** 跨行锁顺序为：策略 → 订阅 → 根 → 单元 → 获取，各组按 ID 排序。涉及已有执行权的 worker 事务先锁/校验 job 或 delivery，再按此顺序锁业务对象；取消既有任务也遵守顺序。不要先锁获取再回头锁单元。

40P01/40001 只对未执行外部动作的短数据库事务有界重试，不能自动重放网络或文件副作用。

### 3.3 类型化命令与扩展边界

采用 CreateSubscription、StartAcquisition、ApproveTransfer 等 service 方法，复用纯权限前置检查、绑定操作/对象的版本化摘要与结果合同；当前权限、回执、成功审计及业务写入由各业务窄仓库事务执行，不暴露万能 Tx 或返回 any 的万能 Dispatch。

V1 只构造 user/service/system Actor。未来调用方复用受限业务入口、查询与事件合同，不直接访问数据库或提升权限。不实现 Hook 链、工具/查询目录、插件 UI 路由、动态表单引擎或空扩展运行时；固定集成工厂在 app 显式组装。

### 3.4 目标目录结构

```text
cmd/pulsemedia/            main、参数、应用启动
api/openapi.yaml          HTTP 规范
migrations/               只追加的 goose SQL
internal/
  app/                    组合根与生命周期
  config/                 部署配置
  domain/                 media、subscription、quality、release、evaluate、naming、transfer
  ports/                  公共合同及 acquisition/library 等窄业务端口
  usecase/                业务编排
  command/                类型化调用的共享权限/幂等/审计机制
  platform/               jobs、outbox、scheduler、secrets、clock、idgen、logging
  store/                  postgres/db（sqlc）、queries、repo
  adapter/                metadata、indexer、downloader、mediaserver、notify、fs/local
  httpapi/                生成接口实现、中间件、SSE
web/                      React 19 前端；产物 go:embed
testdata/releases/        解析黄金样本
docs/architecture.md      本文
```

这些是目标目录，不表示当前已初始化。未来远端存储另定义能力合同，不强迫云盘模拟 POSIX。

<a id="domain"></a>
## 4. 领域模型与拥有语义

### 4.1 媒体身份与单元

外部身份始终为 `(namespace, external_id)`，例如 tmdb.movie:550、tmdb.tv:1399、imdb:tt0137523。禁止裸数字 ID 或标题作为跨来源身份；每个外部身份只能对应一个内部 media，身份合并是显式管理命令。公开订阅创建使用原生 TMDB 详情核对 ID 与类型，浏览器标题/年份只作显示提示；movie/tv 的同一数字分别建档。POST `/subscriptions` 必须携带原幂等键，短事务先复核当前账号、实际对象/媒体类型、根与已有回执，再进行事务外元数据读取；GET `/subscription-commands/{key}` 为当前账号和对象授权的纯读历史回执，不启动元数据读取或新工作。未传根时仅从同类型在线根中唯一推导，离线根不造成歧义，多在线根必须明确选择，数据库故障不得伪装成没有根。TV 初建及既有 TMDB TV 增量使用完整真实季集记录，保持稀疏集号、provider episode ID、nullable 日期/绝对号；零集预告季不合成单元或完整季证明。逐季读取最多4路并发，统一55秒预算，取消后等待全部物理调用返回。发布窄事务按当前账号、policy/subscription/root/单元及媒体锁序重新核对原观察，原子提交媒体与身份、真实单元、按各用户范围展开的 Wanted、成功审计/事件和稳定原回执；仅完整非空季可退休缺失逻辑单元，历史引用、文件事实、用户跳过及 paused/archived 保持。等价重订阅保持意图与策略版本，实际重配置必须带匹配的 expected_intent_version；任一尾部失败整体回滚。直接选择既有 media UUID 按实际类型复核对象与根，不在 HTTP 提交半成品媒体。Wanted 提交后立即到期只提供持久评估需求；TV 自动搜索、候选决策与下载须另经独立的版本化自动授权边界，不能借用管理员手工 review 或绕过电影 guard。

media_unit 在同一作品内以 unit_key 唯一：电影为 movie，剧集为 S01E03；特别季为 S00，动漫可附绝对集号。新增集追加，消失的元数据单元标记 retired，保留历史引用。默认排除 retired 与特别季，用户可显式选入特别季；未知播出日期/未播出目标保守等待，不立即补缺。

**范围/逐集元数据实际切片（2026-10-08，schema28）**：`00028_subscription_scope_provenance.sql` 持久 `selected_unit_ids`、`user_skipped` 与 `scope_excluded`，有效 skipped 为两者 OR；旧 skipped 保守转用户跳过，不能猜只是范围排除。新 scope 动作沿 versioned control UoW，明确 all/selected、季列表、specials、selected UUID；收窄保留历史，重扩不清用户跳过、不恢复 paused。重订阅也共 provenance SQL；旧 UpdateTVScope 必须 expectedVersion/key，不再弱写覆盖。GET 详情返回来源范围、逐单元 nullable air_date/absolute_number、retired/metadata_version；前端 CurrentStatus 以完整 wanted ID/元数据/跳过指纹立即失效旧 report，新集不会沿用旧总数/绿色。
逐集控制沿既有版本化control窄事务：`skip`保存明确的用户跳过并推进意图版本，`recheck`仅接受有效单元UUID和匹配的expected_version，在当前账号/对象、active订阅、未退休且仍在范围内的TV Wanted锁定复核后，仅将所选Wanted标记为到期；它不改变意图版本、范围、其它集的期限、重试冷却或任何下载保护，也不直接创建下载任务。后续自动调度仍复核播出日期、品质、来源预算、在途执行和全部当前事实。命令、审计、事件和稳定原回执原子提交，未知结果仅由用户显式重试原PUT/key/body；详情GET不清除未确认命令。只有当前PUT明确返回409后，用户才可显式请求再次读取当前授权详情并结束该冲突命令；读取失败、存储变化或未知结果均不清除原记录，结束冲突本身不重发PUT或改写业务。UI在最新详情提交前锁住写操作，并主动废弃旧状态报告重读；角色/账号/订阅变化拒用迟到响应。

逐集刷新由管理员 POST subscriptions/{id}/metadata/refresh 明确 1–5 季/expectedVersion/key。窄 TVEpisodeProvider 的 TMDB season 读取严格完整结构/同季/唯一集号与日期，v3 query key、v4 Bearer，禁止重定向、响应5MiB限制和原URL错误；未知日期/绝对号不合成。网络在事务外受55s/HTTP超时与request context取消；ReadTVRefreshContext在短事务锁定当前活动管理员并核对实际role后才读取对象/receipt，网络后Apply再次取得当前用户锁，再按既有policy→subscription→root→unit顺序复核意图/身份/媒体版本/相关订阅集合。Token/system与失效账户不能重放或启动网络，网络中权限变化不能发布结果。完整非空季快照只退休该季未列出的逻辑单元，不删除文件/事实/历史；partial/空响应保持保护不退休。元数据、新单元、按各订阅实际范围 Wanted 展开/用户跳过保持、audit/event/稳定receipt同事务；迟到callback拒写，末receipt失败全回滚。管理UI按用户/订阅保存未确认的原季列表、版本和key，丢回执后刷新或GET重读不清原命令；恢复确认后保持结果可见，确认后读取失败只读；本地HTTP provider→PG→浏览器已验证，仍不是授权TMDB外网、定期TVmetadata job或剧集自动获取放行。

**用户批准的动漫编号方案与来源切片（2026-10-09，schema36）**：采用「TMDB剧集组优先、人工映射补缺、TVDB后续可选」，不是强制收费提供者，也不按季累计集数猜绝对号。canonical单元UUID/SxxExx保持；组只是来源编号视图。`media_unit_identity` 保存 `tmdb.episode`、实际externalID和所属tmdb.tv seriesID，不回填/合成旧ID；每源ID唯一且每unit同namespace唯一，关联不可原地改/删，有证据拒退。TV刷新在既有root→unit→media窄UoW绑定identity、推进unit版本（即时失效投影），episodeID或parent冲突转409且全rollback；unknown旧source0仍无映射权限。
TMDB season真实响应现在要求positive/unique ID；组窄provider先GET作品所属group列表再GET选定详情，验证parent membership/ID/type/总数/requiredorder/唯一source与canonical坐标、bounded5000episode/500group、沿用v3query/v4Bearer、redirect拒和5MiB上限。保留原source order/season/episode，不按数组位置重编。REPEATABLE READ事务内先锁定并校验当前活动会话管理员的EpisodeSourceContext含全部unit源ID与版本，网络后重读fingerprint拒迟到观察。管理员GET `/subscriptions/{id}/episode-groups` 与 `/episode-groups/preview` 已explicit App/strictAPI注册；源失败不假空、foreign404、member403零网络、当前metadata变化409/no-store。纯BuildEpisodeNumbering以实际episode identity join canonicalUUID，未知/retired/坐标冲突给hold且不部分授权；只有type2/单序列/连续明确order能标can_use_absolute，预览仍非发布或下载权限。
**映射审核发布实际切片（2026-10-09，schema37）**：新增不可变 `episode_mapping_revision` 与订阅revision指针；标准解除也发布新revision，不能回退旧指针/删历史。mode为standard/group/absolute/manual，完整来源安全快照/规范化source hash/完整canonicalUUID+unitVersion行保存；group/absolute来源有效期为提交时数据库时间后6h，manual需当前标准元数据/单元版本、退休即失效，不借来源制造物理文件事实。行政POST `/subscriptions/{id}/episode-mapping/preview` 只观测、POST `/episode-mapping` 需完整input/source指纹、confirmed与key，GET `/episode-mapping` 是当前只读一致快照（含intent/time）；这些符号已App/strictAPI/Go/TS完整注册。
冻结单集提交与每个整理事务同样先读取执行凭证并锁定当前账户，再取策略/订阅/根/单元与业务锁，不能在业务锁内等待账户而形成反向锁环。窄publication UoW 当前用户锁→actor/key锁→policy→subscription→root→sorted units→media，锁后重建sourcectx完全指纹（含当前mappingrevision）、复核target/source identity，映射记录、binding/intent++、Wanted投影失效（保UserSkip/范围/paused）、audit/event/稳定receipt同事务。网络在外；丢回执重放先验证当前账户活动/真实role及会话身份，再读cache且不重新GET来源；候选、审核回执与获取重放也沿共享TV身份门禁，账户锁与回执读取在同一短事务。全量并发发现cache miss后同keypeer已提交，preview因此见新版本误409；已改失败前有界复读原receipt一次，权威是同fingerprint已提交结果，不循环/重放新写，确定性cache-miss/网络失败contract与实PG八竞争连续3次race覆盖。数据库限制原source/目标关系与unit版本，原ID/当前文件事实不改，有历史拒弱化降级。GET当前metadata/unit变化或TTL到期标Currentfalse，不把旧回执当当前有效。
订阅详情管理员面板明确选择TMDB组/绝对顺序或manual逐条/同季有界区间生成草稿，未知目标不追加部分行、从不预选S01目标；完整对应表+审核确认后才发布，不连续25/27不扩26。未确认锁action/key/body，GET仍需同key回执，确认后503只读，真实unitVersion变更409清旧preview/current。正常cookie→本地真实TMDB协议→PG→最新embed浏览器覆盖手工range否决、组来源、丢200/重读/同key、保存读失败、旧戳拒、pause/UUID保、keyboard与desktop/390px；临时Chrome/Go服务器/测试库已清。机械detector只报旧policy-preview边框（非本变更），未宣称零警告。
**编号解释实际切片（2026-10-10，schema37；不授予执行权限）**：解析 `episode.v8` 在标准季集分支之前检查绝对号与部分内容/其它覆盖证据，sample、半集、预告和绝对号+SxxExx/E号冲突不能进入可映射 reason，更不能高置信覆盖。管理员只读 POST `/subscriptions/{id}/episode-mapping/explain-release` 与现订阅管理面板接通；`ReadEpisodeMapping` 同 REPEATABLE READ 输出 media UUID、input stamp、revision/intent、数据库观测时间、来源 hash/TTL 与全表目标版本有效性。只有当前已批准 absolute/manual 映射能解释明确原编号；缺号、重复编号/单位、非法 key/version、退休或元数据变化均拒绝，不推相邻集。响应只表达 `coverage_known`，`execution_allowed` 恒 false，不改变原解析的作品身份/质量置信、不制造文件事实、占位或 job；输入不接受路径/链接/敏感名称且不保存原名。读取/解释失败或父版本变化会清旧结果，UI 精确比较实体/intent/revision/input stamp，提示不是执行许可。PG、真实 Strict HTTP、TMDB 协议源→PG→浏览器覆盖 25/27 精确对应、26 等待、片段/混合拒、503 清旧、旧标准版本/退休失效、暂停/UUID 保持和零下载 job；Go 全量、domain/postgres/subscription/httpapi/app race、生成漂移及前端检查通过。使用本地协议夹具，不算授权外网或 NAS 验证。
**完整覆盖门禁继续修复（2026-10-10，episode.v9 / movie-candidate-v2）**：独立反例确认 `[Team] Example - 25 [1080p] - 27` 及标题前 E26 曾被缩为绝对号25。v9 移除原编号时保留其它标题证据与分隔，额外 bare 编号/加号/第二集号统一 `conflicting_episode_coverage`，不展开中间集。实际管理员解释 API 同时拒额外覆盖、不生成 rows/执行权，现独立来源期望未改写、不计新反例为千条 oracle。另一纯反例确认明确电影 TMDB ID 曾在候选匹配遮住动漫绝对号；共享 `MatchesAutomaticMovie` 现在首先要求完整单电影覆盖、非 review，身份不能覆盖这一前置。候选规则 v2 的摘要明确包含当前 parser version，旧缓存不能继续显示允许。v9 源与此共享规则分别经过独立 PG18.6 Go 全量、app/transfer/download/postgres/subscription/httpapi/pgtest/search/acquisition race；正常电影取得/组织真实入口与原独立样本回归保持。TV 解释仍无执行许可，完整 TV 窄获取/整理依赖尚未放行。

**持久单集资源审核（2026-10-10，schema39）**：不可变 `episode_candidate_review` 冻结真实管理员用户、订阅/作品/单元/候选 UUID、完整候选输入摘要、标准 key 与媒体/单元/意图/映射/策略/root generation/source/parser 版本。POST `/subscriptions/{id}/episode-candidate-reviews` 要求 confirmed、expected_input_stamp、原 key 与理由；权限先于回执，Token scope 不提权。同命令锁下复核当前来源/目标和摘要，明确身份矛盾、多单元或未知覆盖不准靠确认绕过；审核/audit/event/receipt 同事务，缓存回执仍核对原不可变记录。状态 `reviewed_not_executable`，不创建下载、占位或文件事实。管理页从真实站点资源 UUID 读取独立来源/编号/身份/品质/目标证据；任意发布名实验不可审定。会话存储只保固定安全理由、UUID、摘要和原 key；丢回执后 reload/只读/同 key 恢复，成功保候选 UUID 再读不重复检索。真实本地 HTTP Torznab→生产检索→PG39→浏览器覆盖键盘、丢回执恢复、503 清旧、真实单元版本409、390px；一源请求/一审核/零下载任务。
**聚合查询完整性（2026-10-10）**：成功非空来源可保留，但公共响应明确 `partial` 表示部分在线端点构造/请求失败或未完成；不得视为完整查询。空成功混失败返回503，不误报无资源；完整成功空查询返回200。取消在数据库读取边界保留 context 错误；持久错误仍脱敏。审核页与发现页显示部分查询/失败，发现页关闭、新媒体请求和卸载取消旧请求。真实 PG 混合失败/构造失败与空成功、partial 新候选/取消及全量关键 race 通过。发现页独立浏览器故障探针已补：真实成功/503 两 HTTP 来源经生产 API 给非空 partial；显式传输503清旧候选并不显示完整空结果；关闭旧慢请求、切换其它作品后旧回复不覆盖新标题/结果，390px无溢出且PG零下载job。推荐元数据为明确UI夹具，晚回应/503为明确传输故障注入，不算外网TMDB验证。TV 执行仍关闭。

**受控TV标准单集执行（schema40–42，仅显式管理员确认）**：独立获取UoW拒Token提权，仅当前真实管理员审核的标准单集；冻结review/媒体与单元/provider episode/intent/mapping/parser/policy/root/source/client配置，authority、global占位、需求、专用job、audit/event/nonce同事务。八路原key唯一、末nonce故障全回滚、不可重签、旧回执恢复、旧版本拒写PG/race通过。专用job的最晚Prepare逐字段复核而非重算占位后的审核摘要，client变化/旧lease-token拒；submitting及未知结果写回保原任务和保护，末审计失败不留task。统一operation→media、indexer→candidate锁序，以真实锁等待+NOWAIT反例验证，schema40全量/关键race/生成通过。schema42将专用job认领/恢复接入App；schema43扩展到原批准映射的显式单单元获取，自动TV与整包仍未开放。
完整文件清单来自qBittorrent或Transmission窄只读合同，含unwanted，严格hash/字段/重复/path/bytes/progress、1MiB/1000条与redirect拒绝；实际HTTP/race覆盖。qbit登录也禁密码重定向且不修改借用HTTPclient；Transmission观察以haveValid实际64位字节与status，不再用float32推下载量，缺字段未知。schema41清单在两次同身份完成观测间读取，配置对原authority复核，清单/完成/组织job/审计同事务；混任务、半下载拒与末审计全回滚、重观测唯一和不可变PG/race通过。缺清单或完整性不明保tracking/保护，不产生入库事实。schema41最终生成漂移、全量与关键race已通过；TV目录/整包与自动执行仍未开放；标准及原批准单映射的受控执行验收见下文。

**schema42标准单集受控闭环验收**：私有TV计划只从不可变执行authority与完整唯一视频清单构造；公开MovieTransferRequest仍拒TV。canonical季集命名不默认S01，FD源/根identity与物理锁沿既有安全内核。policy/sub前置锁与控制命令统一；创建、首次写入和最终入库均核原版本/provider/source/client/需求/拥有。schema42数据库完成门禁同样绑定authority/review/manifest/目标版本，旧二进制无法凭普通TV映射完成写回；TV历史阻止降级。PG实际完整字节/同inode/标准目标路径/入库事实/需求与全局保护消费通过；末成功审计故障整回滚、物理链接与保护保留，明确测试恢复决策复用原plan，迟到单元版本拒入库不重签。POST `/subscriptions/{id}/episode-acquisitions` 管理员用户、原review/inputstamp/confirmed/key，额外字段/限长在生成解码前拒绝；成员不能重放回执，最后nonce失败不留任何获取，202只表示持久任务。真实本地Torznab/qbit HTTP→App.Run→一次查询/Add→完整清单→FD硬链接→通知HTTP回执通过。浏览器真来源/PG覆盖审核丢回执、获取提交丢202→reload/读取不丢原key→原命令恢复唯一job、读503清旧许可、真实元数据409、键盘和390px。手动候选摘要v2剔除自动查询预算的计算ready/retry，时钟跨界不无故失效；源健康/配置/成功观测/绑定与目标版本仍冻结，旧审核不能重签。最终生成、Go全量、关键race及前端类型/构建通过，owned测试资源退出清理。仅本地协议夹具，不等授权外网/真实NAS联调；TV媒体服务器亦已用本地Jellyfin协议端点单独验证正确路径/认证HTTP204与PG delivered；另实例503不阻在线刷新或通知。不是实际用户媒体服务器联调。

**schema43 原批准映射受控单单元执行**：只接受当前已审核 `absolute/manual` 唯一对应；完整批准表的provider episode、canonical UUID/key/version/退休状态全部校验，人工预览保留已有provider ID，未知不提升。原source_number、整rows、source_hash/inputstamp/revision/TTL冻结到不可变 `reviewed_mapped_episode` authority，不用新观测重签。获取、最晚Prepare、实际单视频文件编号、首次FD写入与最终事实复核同一封印；文件27不能代原25，无26不补。schema43数据库完成门禁同样对当前批准表/hash/有效期限/选中source号/完整canonical-provider关联验证，mapped历史拒绝降级。首次准备最后audit之后重查owner/token/state/有效lease，mapped时间亦在计划/步骤/入库事务最终SQL后复核；已发送unknown观察仍记原物理结果不套首次许可。真实PG标准/人工25/27/TMDB剧集组absolute覆盖publication→review→八路唯一获取→Prepare→清单→完整字节同inode入库；最后nonce/audit失败整体回滚、末audit租约过期0task、另一unit版本变化拒事实保链接/原plan/保护。真实App.Run标准与manual25链仅一次query/Add/task/manifest/unit，通知与Jellyfin delivered独立收据，离线server503不阻其他；浏览器mapped真来源/PG覆盖丢审核ACK、丢202→reload/只读保原key→唯一job、读503清许可/metadata409/键盘390px。最新生成、全量、关键race、非root PG/FD/App race通过，前端类型/构建通过。TMDB来源TTL为固定6小时，有效TTL保存及复核已验；追加真实PG事务局部时钟夹具在Prepare与硬链接后入库的末audit推进7小时，lease保持1天有效，最终TTL拒写并整体回滚/保原链接plan保护。测试钟仅随机测试库，不改生产或源证据；本地协议不算实际NAS/授权外网。

**原冻结 TV 硬链接计划的显式恢复**：GET/POST `/transfer-plans/{id}/recovery` 限原真实管理员用户（拒 Token scope 提权）。先获取原根和源物理槽位，事务外用 FD 观察原 source/target 完整 identity 必须一致及根 marker；缺链接、源替换、目标冲突、根/版本变化、活跃 job 或有效旧租约均 hold，不写新文件。短 UoW 锁内再核原 authority/manifest/媒体与单元/意图/品质/来源/客户端/根及原步骤摘要；仅原计划转 approved、原 task queued、新组织 job、requested 事件、审计与 nonce 同事务，owner/token/旧租约与冻结证据不清除、不重签、不重复 Add。202 仅排队，不能显示入库成功；真正事实/导入事件/通知仍由原执行围栏与入库事务产生。八路 samekey 只一组织 job，缓存未命中后拿物理槽位再读回执；末 nonce 故障全回滚，source inode 替换及目标冲突不覆盖。真实 HTTP admin/member、strict未知路径字段、no-store 与 PG/race/nonroot FD 验证通过；浏览器失202→reload/只读保原key/503清旧许可/同key重试、键盘390px后实际原 FD 入库通过。实际 App.Run 先失败退出并 join，再新连接、真实等待旧2分钟租约到期、确认原链接、排恢复 job并新 App 消费，仍一次query/Add/原plan/完整bytes同inode及独立送达回执，race通过（已退出 worker 的人工复核终态由测试明确模拟，不宣称自动复核全部失败）。最终 Go 全量/关键race、gen、前端type/build通过。审查另修 transfer plan claim/guard/finish/review 使用 MATERIALIZED 行锁后 clock_timestamp 校验；真实PG证明有效租约时开始但行锁等待到过期后不得续租/完成/转复核，原状态/租约保持。只支持可确认原硬链接的受控 TV 单单元；电影/升级、多步骤、不明后置状态与跨设备动作不借此开放。

自动 TV 补缺使用独立的标准单集身份规则、持久来源查询证明与不可变自动执行授权。默认未配置编号映射时使用 standard revision 0；显式 standard revision 仍须等于当前配置，手工和 absolute 映射沿用各自合同。原子决策冻结原轮次、真实单元和来源、当前账号/意图/策略/根/客户端版本，以及实际目标路径的 SHA-256 摘要。首次发送期限只限制新副作用的准备；过期或租约失效不能证明先前 Add 未发生，也不能释放结果不明的保护。发送准备须将原始 torrent 字节、完整清单和唯一真实普通季单集绑定到原授权，再复核当前事实及原 job/owner/token/有效租约。物理结果观察以原准备证据为依据，使用同一 operation 物理锁与独立持久观察许可；原发送调用仍在执行或持久 attempt 未确认结束时，不因逻辑租约失效或 PID 丢失接管。观察只执行原任务 A、完整文件清单、原任务 B 的只读请求，并以精确 hash、路径、完整操作标签和整数完成字节核对。最终入库另行校验当前业务事实与源、根目录身份；标准单视频硬链接的文件、映射、imported fact、需求及占位消费、事件、plan/task/job 完成在同一事务发布，并在延迟约束执行后复核租约和完成图。已有管理员单集/整包确认、编号映射及相应整理/入库路径继续复核各自 authority；元数据到期、交互搜索结果和身份建议分别作为观察材料。阶段进度、样本与环境验收以开发计划的当前证据为准。

### 4.2 意图、事实、投影

```text
Subscription（作品、范围、质量/过滤、目标根、模式、控制状态）
   └─ WantedItem（单元目标；可跳过；进度为可重建投影）
MediaUnit ← UnitFact → 明确的下载任务或文件来源
MediaUnit ← LibraryFileUnit → LibraryFile → LibraryRoot
```

- 订阅保存意图，不保存累计已下载/缺集数。控制状态只为 active/paused/archived；version 随控制或配置变更增加。
- wanted 唯一 `(subscription_id, unit_id)`；状态与 best/target rank 是投影，绑定订阅版本、质量/过滤策略 ID 与 revision、单元 metadata/facts 版本及目标根 generation/health_revision。
- 输入版本不匹配或 next_evaluate_at 到期则 stale，触发重算；日期/TTL 相关结果不能永远缓存。最终获取决策读取最新事实和占位，不能把投影当下载授权。
- acquire：目标根有任意符合允许规则的版本即可；upgrade：持续获取到 cutoff。同级优化默认关闭。
- 完成是推导结果，不把订阅永久关闭。文件丢失、范围扩大或策略改变后可再次缺失；空目标集/仍含未来集不能误报全剧完成。

**有限媒体分类**：发布规则按电影/剧集、题材名称、ISO原语言与国家代码匹配目标根。既有作品只使用绑定当前media.metadata_version的保存事实；新作品预览在事务外读取可信TMDB详情，再以当前账号、策略版本及根状态完成只读评估，不创建媒体、订阅或任务。缺失、过期、无匹配、多规则冲突和目标不可用分别返回原因，不猜测分类。策略以不可变版本和原发布键保存；匹配预览返回版本、目标根和绑定规范事实及根generation的摘要。创建必须显式提交classification_version、classification_preview_stamp及classification_preview_root_id，并省略root_id；准备重新读取可信元数据，事务重验策略、账号、事实及根类型/健康/generation并比较摘要，变化拒绝而不静默改向。选择及事实与新订阅同提交；原键恢复不重新取数。手动root_id保留原路径；旧订阅重配置不应用分类，不改已有任务或整理计划。

| fact stage | 创建条件 | 是否构成拥有 |
|---|---|---|
| submitted | 下载器确认接收，或按稳定标识查到 | 否 |
| downloaded | 下载器报告完成且文件可读 | 否 |
| imported | 指定根中已校验、已识别的有效文件 | 是，按目标根判断 |
| confirmed | 媒体服务器确认该有效文件 | 仅增强 imported，不独立证明存在 |

事实追加与失效，不原地改写观察内容。submitted/downloaded 必须有 expires_at，但到期不释放业务占位；活动下载可续期。失效原因与操作者可追溯。imported/confirmed 明确关联 file，下载阶段关联 task；数据库约束对应来源必填与单元映射。

### 4.3 质量与文件版本

**独立年份安全回归实际切片（2026-10-09，EpisodeParserVersion v7）**：固定 MIT 原始标签继续不改，新增显式年份 oracle（缺字段不作negative）捕获标题数字与发行年冲突。movie/TV 共纯 `recognizeYear`，先排除命名空间 ID，再归一分隔符；不同候选年为 `ambiguous_media_year`、0置信/需复核、不授权UnitKeys，不以第一/最后数字或ExplicitID解开。单独下划线年份和相同年重复可作为唯一证据；TMDB ID2005不是年份。`MatchesAutomaticMovie` 在ID匹配之前校验RequiresReview；实际 App.Run 真HTTP返回有匹配TMDB ID但不同年份的candidate→原qualified gate拒、轮次expired/no_qualified_candidate与finalizer成功、无operation/占位/download job。原4源683唯一只属部分字段oracle，未变成1000完整黄金，不开启TV/整包自动流程。

明确季集语法的集号支持1–4位，季号仍须明确；`S01E1056` 对应标准键 `S01E1056`，不截断、不改为绝对号、不默认季。链式/分隔显式列表只保留列出的集合，不填间隙；范围按明确两端展开，最多20单元，五位号、片段、重复列表和混合覆盖保持拒绝。解析并不生成 `media_unit`：缺少当前有效标准单元或身份证据时，解释不返回部分覆盖、审核和获取均不得取得权限。当前解析合同 `episode.v34` 纳入候选摘要、审核及执行冻结围栏；升级合同不重签旧预览或任务，不释放结果不明的物理保护。 发布组后缀的分隔连字符必须在完整成对括号之外；技术括号不提供发布组身份，未知标签保留原始字节，仅用于负向拒绝比较，不认证为中继或新事实。配对扫描不修复或删除损坏标记；旧品质记录不重签，旧候选/审核必须通过当前解析合同重新取得有效摘要。
缺少年份的作品标题只在完整 DVDRip/BDRip/BRRip/HDRip 技术 token 处截断；截断不认证 DVD/HDRip 来源或未测量属性。技术 rip 标记后的唯一年份不提供作品年；已有明确前缀年份保留，多个不同可能年份仍整体拒绝，不通过删除技术尾部选择某一年。
明确独立季范围（如 `S01`、`Season 1`）不证明单电影或完整剧集集合。剧集解释继续拒绝不完整覆盖；旧电影解析也必须零置信转复核并清除外部身份，不能因作品年份或电影ID绕过。季token不匹配完整 `S01E01`，不默认集号、不展开整季；旧记录、文件及结果不明的占位均不重签或释放。

无年份电影的发行语言尾标仅在明确质量边界存在时清理；German DL须成对，单独German、正文及带年份片名保留。解析v57进入当前缓存键，不改写旧来源/事实；无年份仍需复核且不授权单元覆盖。有字幕标题与唯一发行年份的电影片名中的Episode N只屏蔽松散TV提示，标准季集/日期/绝对号/部分内容及年份歧义仍阻自动执行。独立原标签不改写，可选literal_title仅允许原标签大小写差异且须逐字存在于原始输入，解析标题仍精确比较。v39仅清理已证实的网站/技术前缀与独立方括号，未知装饰和原发布组不猜测；Season后质量数字不充当季覆盖。明确标准集号后重复四位数字作品标题且紧接质量标记时不当作额外集数，不同数字/额外E标记继续复核。
v41仅保留独立标题证实的“Vol.”＋空白＋1–3位整数的标点；文件分隔Vol.3、嵌入词及年份数字不按该缩写处理。v42将明确EP/Eps缩写范围（如Ep. 1-43、EP1-EP43）作为完整输入中的竞争覆盖证据：无映射范围转零置信剧集复核，标准集号、绝对号或斜线别名不能遮蔽该范围，也不能把第一集/最后一集变成唯一单元。无范围的电影Episode标题仍遵守原独立年份合同。黄金来源的季标量与数组只适配原始格式，许可/固定哈希/标题/完整数组保持独立校验；合法标量不得生成部分覆盖。
v43仅将独立电影发行年份两侧成对星号视为装饰，保留片名内部星号、未配对星号和多年份/季集歧义。EZTVx.to与eztv是已证实的分发尾标，移除时保留原始输入和实际发布组，未知括号仍保留。v44为固定独立来源证明的“www TamilBlasters tel -”及“www 1TamilBlasters tel -”增加仅前置、带明确分隔的空格格式支持；未知家族、错误后缀、缺少分隔或正文中的相似词不清理。完整原始输入、季集/日期竞争覆盖和零授权复核不变；解析版本随缓存键推进，不改写历史事实。
v45将独立固定输入中的完整BDRemux标记识别为Remux，并作为标题质量边界；仅完整词有效，MyBDRemux/BDRemuxish等子串不提供来源事实。与WEB-DL或不支持的DVDRip竞争时来源保持未知，不能任选策略排名较高的一项。技术尾部的唯一年份不独立建立作品发行年，明确早于质量边界的作品年份仍保留；无年份片名、原始输入和零单元授权复核不变。缓存键随版本推进，不重新写入旧事实/历史任务。
v46对明确发行尾部的[Complete]、品质括号前Collection及带数量的Movie Collection转为零授权合集复核，不枚举成员或证明实际文件覆盖。[Complete]后若接正文而不是发行元数据则保持片名，裸Complete/Collection、仅冠词或未知品质括号不触发该规则。仅与声明绑定的vN/R2J/VFR/Dual Audio尾标参与词法清理；未知发布前缀和完整RawName仍保留，普通电影标题、原品质事实、三位季编号和仅季资源的复核合同不放宽。明确TMDB/IMDb标记或标准单集号不能绕过合集声明；缓存键按v46重新评估，不重写旧任务、文件或事实。

v47仅在明确技术标签前的唯一四位数字构成全部片名时保留其为数字作品标题，不将它作为独立发行年份；所有可能年份先进入歧义检查，多年份仍需复核。真实前置作品年份、标准数字剧名和完整原始季集/日期竞争证据不变；无年份数字电影仍零单元授权，品质词不替代身份确认。缓存键随v47重新评估，旧文件、任务和事实不重写。

v48只在完整绝对号提示上下文中将末尾纯数字/版本号视为集号而非发布组；普通电影的数字发布组、已绑定的MTBB/CRC绝对号证据和完整RawName继续保留。v49将四位及更长S季号纳入拒绝性提示与竞争覆盖检查，但可执行标准季号语法仍仅1–2位；S2025E86等超出支持范围的声明转零授权剧集复核，与标准/替代集号混用也不得保留部分单元。不将季号数字写成作品年份，不扩展体育/长季号映射。独立固定来源的4k仅适配为标准2160p，原始字典/许可/哈希不改写，矛盾分辨率继续拒绝；音轨、CRC、1080i及容器标签不冒充测量品质。缓存键按v49重新评估，历史任务、文件和事实不重写。

v50将已证实的西语cap. N Al M作为完整输入中的拒绝性范围证据：不认证原标签只列出的首集，也不推断未绑定范围的规范单元。带发布前缀、1–3位裸整数和完整8位CRC尾标但缺少连字符绑定的输入转零授权剧集复核；CRC不冒充文件哈希测量或编号映射。标准集号或斜线后的电影片段不能遮蔽这些声明，混合覆盖不能保留部分单元。四位作品年份、普通Cap/Cape片名和已绑定的连字符绝对号合同不变；未知前缀、完整原输入及原始源断言保留。缓存按v50重新评估，不改写历史文件、任务或事实。
v51在标题提取时保留有明确词边界的连续大写单字母缩写点号及其后的标题词/续集数字分隔；普通文件分隔符、年份/日期与完整原始季集检查不变。独立来源中既有的空格式缩写标签不重写，只有逐项记录的title_punctuation_evidence可用原文中唯一逐字出现的完整缩写片段替换完全相等的单字母序列；其余标题词及大小写必须与独立标签相等，不能增年份、集号、单元或换词。既有literal_title仍只允许大小写差异，解析输出继续逐字比较，不使用通用点号忽略比较。标题/品质词法结果不授予获取权限；缓存按v57重新评估，不改写历史文件、任务或事实。

v52仅在电影标题开头保留完整配对的1–3位数字括号（如(500)）；去除发行边缘装饰时不能删掉该左括号。没有年份仍复核，括号年份、未配对装饰、技术尾部年份、日期/季集竞争及完整RawName门禁不变；不补配括号、不引入新编号/映射/获取权限。当前缓存按v57重新评估，不改写历史来源、文件、任务或事实。

v53仅在多词片名后识别完整大写CAM/SCR/DVDR尾标（可带XviD/DivX）及其后的有界字面发布组，清理该发行尾部以保留独立片名；普通Cam片名词、裸标记、未知子串及年份前的片名词不因此删除。来源/编码不因标题清理成为受支持品质，未测分辨率/音轨/HDR/版本仍未知；本轮无年份输入继续复核且不产生movie单元。完整RawName、年份/日期/季集及斜线前段竞争检查不变；当前缓存按v57重新评估，不改写历史来源、文件、任务或事实。

v54仅补入固定独立标题断言证实的1337x.to空格-空格及括号内tracker.xn--p1ai后空格两种完整分发前缀；不将其扩展成通用punycode域名清理。前缀在作品年份/品质/标题分析前排除；完整RawName及去除前缀后整个输入的季集/范围/日期/竞争年份检查保留，斜线前段和显式movie ID不能绕过拒绝。未知域名、缺少分隔符及片名内部相似词保留；缓存按v57重新评估，不改写历史事实，不增加获取或覆盖权限。

固定MIT Rakun@9cb4960d、Beast@2fb0c592、Atama@d3d73039及Clement@98f7d838的原表、独立断言和许可证按SHA校验，不执行上游解析器。Rakun50/93、Beast27/119、Atama37/39、Clement15/31为实际完整黄金标注计数；原始季集标量、完整数组、范围字节、十进制和版本声明均保留作独立反证，不从端点、默认季或源解析输出生成覆盖。Cehbz固定MIT测试及五个历史版本已核实为独立手写断言来源，17条完整合同入库，22条合成控制仍排除；历史原件与冻结争议见testdata/releases/review-inputs/m3-02-v73-review。另有Java、moistari/rls、Hellberg及经固定MIT原断言审核的新来源；重复原文只计一次。来源整体权限与formal_golden_acceptance仍false，原1758去重池不变；只有reviewed-full.json中的1015条完整独立合同达到1000门槛。当前解析v74、正式31/42，M3-02本地验收通过。两个公开入口对完整原文中的范围、卷号、未绑定编号、合集、字幕/ZIP及明确partNN.rar分卷证据保留零覆盖复核，路径尾、年份、电影ID或其他集号不能恢复权限；普通片名、原始字节与已确认的规范编号不变。绝对号仍须当前映射版本，审核不替代获取授权。实际全media/quality race、真实私有PG三条单集App.Run链、原计划重启恢复、七类回执/失效守卫、编号映射、纠正历史和缓存，以及lint/生成漂移/依赖门禁通过；自动TV/整包/升级/跨设备动作仍须各自后续任务验收。

v55仅在品质提取中将完整VOSTFR（不区分大小写）视为字幕元数据尾标，不认证为发布组；不将其改成音轨或来源事实。原始输入及原字幕声明保留，片名中的同名词和VOSTFRx/VOSTFR2等未知组名不删除。季/集/绝对号、日期、作品年份、覆盖与置信合同不变；缓存按v57重新评估，不改写历史任务、文件、来源或事实，不增加获取权限。

v56仅将有明确词边界、分隔符及1–2位数字的法语Saison/Saisons声明作为拒绝性覆盖证据，不增加可执行季号语法或编号映射。电影回退前检查完整原输入，不能凭后续作品年份生成movie单元；标准/替代集号、绝对号及斜线尾段不能遮蔽前段声明，也不能保留部分覆盖。普通Saison/Saisonnier片名词和四位作品年份继续遵守原合同，原始输入、独立季集声明及词法品质保留。缓存按v57重新评估，不改写历史来源、任务、文件或事实，不增加获取权限。

v57仅在已有“发布前缀＋1–3位裸整数＋8位CRC”拒绝性提示中允许整数与CRC之间隔着一个受支持的480p/720p/1080p/2160p方括号，不扩展为任意技术括号或可执行编号语法。完整输入中的未绑定声明在电影回退及标准/替代/绝对号提交前均拒绝，斜线后的电影年份或单集不能隐藏前段证据，也不能留下部分覆盖。原RawName及独立编号/CRC断言保留；CRC不认证测量文件哈希、发布组或编号映射。四位电影年份和既有带连字符绑定的绝对号合同不变。缓存按v57重新评估，不改写历史来源、任务、文件或事实，不增加获取权限。

Rakun逐项准入还保留原meta与codecs字段作为独立反证：原complete/pack/has_ovas/has_specials声明要求零置信、零规范/绝对覆盖复核，不可只因标注requires_review就保留部分季集或movie单元；这不把待审合集升格为黄金样本。原audio_5_1只保留为音轨声明，不测量声道、不转成视频编码，未知布局和冲突视频编码仍拒绝。新增499-504/773-782两条逐字标题声明后完整独立标注560/1000，Rakun21已审/72待审，1758池与整体权限false不变；生产解析器保持v57，不新增执行语法或权限。

v58仅将完整COMPLETE＋BluRay/Blu-Ray＋Coffret组合作为合集拒绝证据，不扩展裸COMPLETE、普通Coffret片名或完整单电影光盘语义。两个公开解析入口都检查这组原始证据；作品年份、外部ID及斜线后的电影/标准/替代/绝对编号不能保留部分覆盖。Complete Series＋OVA/OAV后的已知技术尾段只缩窄词法标题，不删除未知标题续文、任意括号或原RawName，不推断实际季集/文件清单。固定142-150/802-810两条声明逐项审核后完整标注562/1000，Rakun23已审/70待审；缓存随v58重评，不改历史事实或增加执行权限。

v59将带发布前缀的明确数字范围作为完整原输入中的拒绝证据；仅识别紧邻数字的连字符或字面波浪号，不将数字片名＋空格连字符＋单集误当范围。范围不展开成规范集号，年份区间继续原有歧义复核；外部movie ID、斜线尾段与标准/替代/绝对号不能遮蔽前段范围或留下端点覆盖。
v60仅在已证实发布前缀和明确技术尾段形成边界时清理词法片名；未知前缀/括号/版本标记保留。已知单个OVA声明与完整系列＋OVA不同，仍须零置信/零规范或绝对覆盖复核，不据此补季号、特别篇集合或实际文件清单；完整原输入/品质声明保留。
v61对明确.srt/.ass/.ssa/.sub/.idx/.vtt字幕文件仅保留词法标题和原始输入、品质声明；两个公开入口均移除电影Identity、标准/绝对单元与置信，reason=non_video_file，必须复核。电影年份、显式movie ID、标准/绝对集号和重复字幕扩展名均不能绕过，普通SRT片名与未知srtx后缀不受此规则影响。缓存按v61重新评估，不改写历史文件、任务、来源或事实，不增加执行权限。

v62将明确All Seasons (1-8)、Seasons 1 to 7 Complete等季范围作为完整原输入的拒绝证据，不展开端点、不保留斜线尾段或外部ID产生的电影/标准/绝对单元；只清理已确认的尾部Complete展示词。仅完整Ultra-Compressed技术后缀不认证为发布组，普通Compressed组名和正文片名不变。

v63仅移除固定独立声明证实的DKB/EMBER/ToonsHub标准单集前缀并保留发布组原证据，不删除未知发布前缀。两个公开入口拒绝完整输入斜线或反斜线父段中的明确标准/替代集号，文件尾段不能重新认证为另一单元；单独季目录与普通目录沿用既有规则。已有明确范围的拒绝理由优先保留，原始完整输入、历史事实与任务不变。缓存按v63重评，不新增获取权限。

v66的动漫标题词法视图仅在无年份的既有复核片名分支使用：有限已证实发布前缀与完整技术尾部才可移除，原始完整输入、年份、季集、范围和相对编号拒绝先行，不从展示文本反推规范覆盖。v67的括号发布组仅在分辨率、支持来源和编码均明确时处理完整技术尾部，内部明确分隔的组名与外部组/内部encoder区分；普通片名括号、技术标签、未知尾部续文继续保留。组名识别不增加品质测量、规范单元、映射有效期或获取权限，缓存按当前解析版本重评。

v64拒绝发布前缀下的未绑定广播编号及Episodio完整区间，四位电影年份与已绑定单绝对号保持既有合同。v65把完整技术/CRC布局、分段字母、物理卷号及明确Batch区间作为零覆盖证据；单个编号、位深、版本或未知括号本身不构成新授权或可执行语法。每个原输入段及已知完整布局后的年份、电影ID、标准或绝对编号均须保留复核；不得通过文件名尾部或斜线恢复部分单元。带空格的连字符范围仅在完整独立Batch尾声明下识别；真实日期边界保留，数字片名＋单绝对号和Berserk 2017的13-24范围不被错误日期模式吞掉。仅已证实发布组/中继及完整技术布局用于复核标题，原始输入、历史事实、品质与映射版本保持独立。VP8作为明确不支持的编码反证保持未知，不能与x264/x265并存时只认证支持的一方；未开放新编码枚举或测量事实。缓存按v65重评，不新增下载或文件动作权限。
v38保留独立原始标题证实的1–3位N.0、小数片名、Mr.后空格及开头序号标点，不把年份分隔符当小数。仅在完整域名和明确分隔边界清理已证实的分发前缀，并在年份/覆盖/品质分析前排除其数字或编码词；原输入保留。覆盖回退检查完整输入，斜线后的文件尾段不能遮蔽前面的标准集号或日期。ESub/ESubs及带闭括号的纯尺寸尾标不作为发布组，未知UTR等原证据保留。完整独立标注432/1000，自动TV/整包/升级仍关闭。

v39对已经通过唯一年份检查的电影，允许明确年份前置时从后续文本提取片名；前缀数字不残留在标题。多年份歧义、数字剧名和原标准/日期/整包覆盖门禁不变。新增固定MIT完整字典tests/test_main.py:182/459/528/857四个输入逐项审核，完整独立标注436/1000；未扩展品质枚举或未测量音轨，自动TV/整包/升级继续关闭。

v40保存传入识别器的完整原始字符串作为RawName，不能将斜线分隔的多语言别名/范围证据截成尾段；解析视图仍使用文件尾段，完整输入继续用于覆盖拒绝。RawName只是规则证据，不是文件路径操作权限或已确认媒体身份。固定MIT日历/完整季/俄文范围/双语电影六条独立记录推动完整标注442/1000；日期/未知季范围必须复核，不能默认S01或宣称整包完成。

原始范围标签格式可能是季/集编号标量或数组；独立样本准入按同一有效编号集合校验，拒绝非法类型、空值及重复，未确认完整覆盖时仍不发放单元授权。追加固定MIT别名电影与季范围五条审核记录后，完整黄金实际447/1000，M3-02尚未达到1000验收要求。

质量事实保存 source、resolution、codec、HDR、audio、edition、group 等原始属性与解析版本；**不保存全局 quality_rank**。filename HDR 当前另存 `hdr_parser_version=hdr.tokens.v1`，只在完整 token 识别时绑定该字段；DVD/Adventure 子串不是 Dolby Vision。缺少或不支持此来源版本的旧 HDR 解码为该属性未知，不取得 HDR 加分或据此达到 cutoff，原 JSON、其它基础品质、文件/事实历史与物理占位不原地改写或删除。当前没有独立 HDR 测量写入合同，不以 filename marker 冒充 ffprobe 证据。source 与 codec 亦由完整分隔 token 识别，支持与不支持的来源/编码 token 同时出现也保持未知（如 WEB/DVD、HEVC/Xvid），不能忽略反证后保留允许属性；分别携带 `source_parser_version=source.tokens.v2`、`codec_parser_version=codec.tokens.v2`；旧/不支持版本的单属性解码为未知，旧 source 不参与允许集合或 rank。旧 JSON、其它有效证据、入库文件与占位不原地重签或删除；没有独立 source/codec 测量合同，不能把 filename 标记冒充媒体探针事实。rank/score 只属于具体质量策略版本，不跨策略比较；未知属性不伪装为 rank=0，也不自动视为达到 cutoff。发布组拒绝是负面门禁：已保存的 `GROUP[relay-tag]` 不能靠括号后缀绕过 `GROUP` 的拒绝规则，比较时保守匹配前缀但不原地改写或认证旧事实。原记录缺少发布组时，原标题最后明确 dash 后的精确拒绝组也只作为负面证据，不填补或认证旧 Group；移除明确视频后缀仅用于比较。新解析另接受由2至8个单ASCII字母以字面点分隔组成的发布组缩写（如 S.V）；空格/下划线不能代替点，长词点分不取得该缩写身份，不把 WEB-DL.x264 等技术尾串当组名。三项以上逐项明确E号的dash列表只解释显式单元集合，最多20项，不补间隙；两端点范围语义不变，零号、重复、未知尾串、片段和混合编号仍无覆盖授权；覆盖尾串E03/S01不得作为发布组身份。裸WEB不推断WEB-DL/WEBRip子型。新解析只剥离已知分发标签（eztv/ettv/rartv/TGx/ETRG/rarbg），未知后缀保留原样；BRRip 仅表述 Blu-ray 来源族，不证明编码保真或拥有。完整样本预期由固定 MIT 输入与原标签逐条审核，不能从解析结果生成；部分完整审核合同与部分字段 oracle 均不自动构成千条验收，实际数量与验收证据只在开发计划记录。动漫绝对号后的括号分辨率只接受明确480/720/1080/2160p；解析出原编号仍须原批准映射，未知括号、片段和第二覆盖证据不产生canonical单元。明确音频格式后的标准声道乘数仅从覆盖扫描排除，保留RawName和原始quality输入，不生成audio事实；未知音轨写法不猜，双语slash中的未知覆盖守卫保留。标准/alt TV 仅剥离已明确且尾置的发行年，标题数字保持；身份建议另验原作品前缀日期，不能从集标题日期推断作品身份。日期式覆盖没有 canonical S/E 证据时保持待审，不能把完整播出日期中的年份或外部电影ID当作 movie 全单元许可；明确 S/E 仍保留标准单元，但播出日期不作为作品发行年。完整英文日/月/年（含受限序数后缀）同样只代表日期证据，核对公历日期与序数，非法日期不能掩藏额外覆盖；单独月份片名不匹配该完整日期语法。电影发行年后明确的 sample/clip/part/half 片段标记使完整覆盖失效，保留原展示标题和非权威分类，原 RawName/品质证据不修改。复合合集/多作品覆盖（complete/ultimate collection、complete pack/set/series、full series、all N seasons、N movies、box set）同样保持零置信和零单元，年份或外部电影ID不能越过该门禁；独立单词片名如 The Collection、Complete、Full Metal Jacket 不受该复合语法影响，不推断包含的作品或季集。有作品前缀且紧接年份、技术/身份标记或文件尾的明确多部曲后缀（Duology/Trilogy/Tetralogy/Quadrilogy/Quadrology/Pentalogy/Hexalogy/Heptalogy/Octalogy/Octology/Ennealogy/Nonalogy/Decalogy）也不证明单movie覆盖；不推断内部作品，嵌入式片名 Trilogy of Terror、单词 Trilogy 或仅文章前缀的片名不按该后缀语法判合集。合集后缀与日期/技术边界之间只容许最多三个明确语言/字幕标记（SWESUB/MULTi/TRUEFRENCH/FRENCH/GERMAN/ENGLISH/SUBBED/DUBBED），不以任意尾词或字母子串补覆盖。发布组解析与负面比较使用临时身份标签视图：完整成对的 TMDB/IMDb 标签及有明确分隔的完整 IMDb 尾标不得成为组名，也不得借身份尾标或其相邻分隔符绕过原组拒绝；未知括号后缀原样保留，不借此认证组名，损坏身份标签不静默剥离，原 RawName/旧 Group 不重签。完整 WEB-DL/Blu-Ray/DTS-HD 技术标记内部的 dash 不作为发布组分隔；独立数字组尾（如 -1920）仅在前方已有明确技术属性时从作品年份证据排除，不借组名补作品日期，不删除普通 Movie-1999 或冲突作品年份。原 RawName/Group/品质证据不重签。当前解析合同为 `episode.v29`，候选缓存和审核/执行证据必须核对该版本。历史 completed 下载即使 matched 或 acquisition source_stamp/fence 一致，在创建新 movie 整理计划前仍复核当前完整覆盖合同，失败保留下载、文件与物理占位并转人工，不给旧覆盖重新签计划。升级纯规则先验证当前/候选属性版本、策略及允许集合再比较，未知或不可比用 `ScoresKnown=false` 表达，真实允许的 0 分仍是合法分数；纯结果没有标题拒绝词、对象权限、事务和物理执行授权，不能直接当作升级许可。

当前单电影升级评估通过 `POST /api/v1/subscriptions/{id}/upgrade-previews` 暴露，客户端只提交旧文件 UUID、候选 UUID 集合及可选 expected_version；不接受路径、品质、rank 或分数。仅当前活动管理员会话可用，Token/system/service 不取得该命令入口。窄仓储在同一 REPEATABLE READ 快照按账号→绑定策略→订阅→根→单元读取并加必要共享行锁，核验唯一文件/单元映射、有效 imported 品质事实、当前投影和候选/来源完整版本，再按绑定 immutable 策略重算当前与候选分数、cutoff 及标题/发布组准入；不跨策略或取任意首单元。所有可能阻塞读取结束后，统一使用新的数据库时间核验投影、事实与来源期限。未知属性、共享文件、多单元、TV 或不支持范围不获得升级许可，同级优化默认关闭。响应恒为 `preview_not_executable`、`execution_allowed=false`；事务仅 SELECT（含共享行锁），不写业务行、job、占位、授权、审计或事件，不触盘或联网。提前返回使用不继承调用方取消的独立五秒回滚期限，保留原业务/取消错误，避免无响应清理拖住HTTP停机排空；成功结束事务不会被二次回滚改写为失败。该评估及版本围栏都不是后续升级授权，执行仍须独立窄UoW重新验证当前证据并冻结全集合。旧文件多单元映射不得因单单元升级整体退休；冻结授权由下述schema64非执行预留合同提供，受控发送、回收与恢复仍按M3-04后续合同实施。

**冻结单电影升级预留实际切片（schema64；不可执行）**：订阅持久升级模式默认 `disabled`，schema64首片仅显式允许 `stop_at_cutoff`；schema80按下述独立模式合同开放 `replace_if_better`。窄UoW先锁当前数据库账户并校验真实角色/对象范围，按账号→策略→订阅→根→单元→文件/事实→操作/来源/下载器/候选顺序锁定当前材料，复用同事务可信读取与策略重算。专用只读 observation 另绑定下载器配置摘要，客户端只能提交旧文件/候选引用、版本和摘要围栏及显式确认，不能提供路径或品质分数。旧文件恰好对应唯一单元、当前imported事实、来源/投影期限、模式及严格升级/cutoff门禁均成立后，一次事务写不可变 `movie_upgrade_authority`、全局acquisition lane、unit reservation、goal=upgrade需求围栏、operation、成功审计/事件与幂等结果；包、单集与升级共享同一物理取得保护，冲突整次回滚。

授权首次INSERT后必须先比较全部显式业务绑定列、固定goal/status和完整snapshot的JSONB相等性，NULL/false/查询错误均拒绝，不能把触发器改写后的行直接当作可信基线。随后每次跨表写入及 `SET CONSTRAINTS ALL IMMEDIATE` 后，重验已持久行与原受保护输入，仅扣除本次已逐项验证的lane和reservation插入引起的两次facts_version增长；额外事实或原配置变化不被归一化。成功回执恒 `reserved_not_executable`、`execution_allowed=false`，不创建执行job/download_task、不调用Add；通用SubmitDownload/ReconcileTask/ObserveTask在读取旧任务/状态或调用客户端前拒绝升级authority、需求或reservation，schema64数据库门禁拒绝该authority生成物理任务；schema65仅按下述专用准备合同允许不可发送的submitting任务，通用路径继续拒绝。原键恢复仍先核验当前权限和对象归属，可在关闭模式后返回原非执行历史回执，不重新授权。迁移Down先锁相关表再拒绝丢弃模式/授权历史。升级实际torrent完整覆盖纯预检及窄ports验证已按下述合同实现；专用准备/提交/发送/恢复、回收/版本切换及公开确认流程另行实施。

**单电影升级原始torrent预检实际切片（schema64；文件证据，不是执行授权）**：原生v1解码器在10MiB上限内保留原始torrent字节、原info SHA1及完整torrent SHA256，以冻结movie/unit UUID与版本、标准movie key、标题/受控别名、年份、明确外部ID及parser v74核对完整普通文件清单。只接受恰好一个有正大小的明确电影视频，安全附件（含零字节）原样保留。逐段核对父目录及附件声明；额外/缺失/重复视频、第二作品、foreign ID、年份冲突、TV/合集/分片覆盖、路径别名碰撞、归档/符号链接/未知布局均转复核。裸标题/别名成功前执行完整身份、年份及覆盖反证；点号/下划线采用对称比较。绑定标题内普通词只从额外词法危险词检查中豁免，完整原名仍进入全部覆盖及身份检查，标题外partial声明不被消除，无年份歧义布局仍可保守复核。

`MovieUpgradeMetainfoProof`私有持有原字节、完整清单、目标及唯一视频，访问返回副本，零值/JSON/变更证据不能签发有效proof。用例与仓储只依赖ports proof视图及显式注入的`MovieUpgradeMetainfoVerifier`；原生Inspector验证器拒绝自定义或嵌入包装proof，即使其自报ValidateBinding=true，并逐项核对全部冻结目标字段及identity/alias集合，保留调用方取消。此合同不授予当前数据库权限、租约或升级执行能力；准备事务须另行重验当前授权和保护，不能因收到proof而创建job、调用Add或退休旧文件。schema65专用准备仓储按下述合同完成本地PG验证，尚不具备发送或真实下载器验收。

**单电影升级专用准备实际切片（schema65；prepared_not_sent）**：新增独立的准备租约及不可变准备凭证，保留schema64的authority、保护和非执行历史回执。内部窄ports仓储显式注入ID与原生proof验证器；claim/prepare先锁当前数据库账号并校验真实角色、原actor及订阅对象归属，才读历史回执。准备租约由服务端产生token并固定60秒，取得行锁后及事务尾均使用新的数据库时间核验owner/token/state、有效租约与冻结证据期限。按原锁序重读当前策略/模式、唯一旧文件/有效事实、单元/候选/来源、根与下载器；精确核对原lane、unit reservation和requirement身份。只在已验证本操作两项预留写入及唯一task INSERT完整行相等后，归一化它们各自引起的事实版本增长；额外增长仍冲突回滚。

原生proof须与同事务重建的完整movie target、原始字节SHA256及已有infohash相等。当前根路径提供实际save_path，不接受调用方路径或不存在的subscription路径字段。事务原子写不可变准备凭证、固定submitting的download_task、成功审计/事件/独立准备回执并将租约置prepared；任务在INSERT前构造完整预期，时间戳在PG内规范化，INSERT后与每次跨写及延迟约束触发结束后重验全部任务/凭证/受保护输入。原始torrent可能含tracker passkey，只在私有proof内存持有；数据库存完整文件清单、目标、infohash及摘要，不落原始torrent、URL或凭据明文。回执恒prepared_not_sent、execution_allowed=false；当前task是不可发送的准备记录，不创建执行job，不取得Add、通用提交/跟踪/恢复或旧文件退休权限。数据库及通用入口继续阻止该任务发送或变成tracking/unknown。历史回执恢复先核验当前权限，不重新授权；准备历史禁止改写/删除。Down先取得相关表锁并拒绝丢弃租约、准备或成功历史；并发准备提交后不能被降级丢弃。专用一次发送授权及原连接物理许可按下述schema66合同实现；原物理返回按schema67独立事务记录；实际发送、R3、受控回收与公开App/HTTP/UI仍需后续合同。

**单电影升级一次发送授权实际切片（schema66；armed_not_sent）**：专用内部session固定原pgx连接，使用operation派生的会话级独占锁和独立nonce；原连接跨越授权事务并由调用方显式关闭。Arm先于等待本地gate建立15秒工作期限，gate取得后复查取消；Close等待gate和关闭共用不继承调用方取消的独立五秒期限，超时不冒充清理完成，也不释放正在执行的原连接资格。通过当前账号/对象权限、原准备与完整task快照、原保护身份、当前来源/事实/配置及原生proof/全字节摘要复验后，同事务写不可变movie_upgrade_send_authorization、数据库产生的60秒发送租约、成功审计/事件/回执。task、authority和preparation保持原样，无执行job或网络调用。

首次INSERT前构造完整期望，插入后及每次跨写、延迟约束完成后比较全行并使用新的DB时间验证期限。数据库同时绑定operation的固定锁key、原backend PID/start/nonce和ExclusiveLock；pg_locks不足以区分会话锁与事务锁，因此在共享事务锁屏障内执行独占session解锁并重取，明确拒绝错误key、共享锁及仅事务锁。成功路径屏障持续阻止另一规范独占执行者进入；SQL失败不交付发送资格，原连接由调用方清理，不宣称错误路径必然仍持有原独占锁。持久授权不会因Close、连接丢失或逻辑租约到期被删除/接管；同session重复、后续session重入均转观察，不重放。成功回执恒armed_not_sent/execution_allowed=false，只是历史证据，不是可复用Add能力。原始torrent/passkey不落库；备份恢复保留不可变许可并拒绝重入，Down先锁后拒绝任何发送授权或成功历史。受信任返回证明的窄ports与独立结果写回事务已按下述schema67合同实现；生产证明签发、实际同字节发送、共享Manager预算、R3与公开入口仍需后续实施。

**单电影升级原物理返回记录实际切片（schema67；结果不授予执行）**：专用repository构造时显式注入可信返回验证器，原Arm session默认无该依赖并拒绝结果写回。验证器必须独立验证真实同步调用已经返回的证据，精确绑定operation/task/owner/token、完整Arm SHA256及原始torrent SHA256；调用方布尔值、零值、伪造/其它调用、未来时间或未配置验证器均不取得记录权。目前没有生产签发器或Add；测试依赖仅验证窄UoW，不构成物理发送证据。

写回事务保留原连接、nonce及固定独占session锁，先核验当前真实账号/对象权限，再按原账号→策略→订阅→操作→根→单元锁序锁定原准备/任务/保护。只核对原完整图和归属，不按当前来源期限、根generation或下载器配置重新选任务/路径；HTTP期间发生配置变化仍可记录原调用。旧文件与单元使用FOR UPDATE阻止新增FK子行，既有映射按固定顺序加锁；在锁后捕获当前文件、事实、映射、配置及完整保护行集作为仅本事务使用的摘要，禁止结果或成功账本触发器顺带修改它们。预先构造完整INSERT期望，跨写及延迟约束后比较完整结果/Arm/任务/准备/成功账本及当前基线；最后一条授权读取在所有可能阻塞的校验结束后复核原owner/token/state/session和严格有效DB租约，再提交。该有效性点不保证COMMIT返回前租约仍未到期。

成功只追加不可变movie_upgrade_send_result及成功审计/事件/独立原键回执，status为confirmed或unknown、execution_allowed=false；不把原submitting任务改成tracking/unknown，不改写authority、preparation或Arm，不释放lane/reservation/需求或物理许可。历史回放同样先校验当前权限、同键请求绑定、原连接和仍有效租约，不重发；断连/到期不能伪装为同步调用已返回。库中不保存原始torrent、链接、外部错误或原响应。Down先锁原图/结果/账本相关表，再拒绝任何结果或未决Arm历史，备份恢复保留不可变证据。实际同字节发送、可信生产签发、R3与受控退休/回收仍是后续边界。

**单电影升级原会话dispatch实际切片（schema68；无网络）**：专用构造器仅开启原Arm session上的一次性dispatch事务，原Arm/result-only构造器不自动获得该能力。等待gate受同一工作期限约束，原连接/gate保持到提交返回；进入事务前消耗本session尝试，随后任何错误或提交不明均不恢复尝试资格。先检查当前账号及对象范围，按原业务锁序重验当前来源、策略、事实、根/下载器、原保护及完整native proof和原字节摘要，绑定原Arm/preparation/task与仍有效DB租约；不复用历史Arm/result/dispatch回执作为执行许可。

同事务保存不可变movie_upgrade_send_dispatch及成功审计/事件/独立回执，只持久化原操作/任务、owner/token、原session/key及摘要/时间，不保存raw torrent、tracker/passkey或响应。首次插入前构造精确期望，跨写及强制延迟约束后完整比对，最终阻塞读取结束后再按DB时间校验租约；SQL guard同时复验固定原session独占锁，并在准备图检查后再次拒绝到期。专用lease约束映射conflict，其它数据库失败和取消保留既有分类。dispatch的operation主键阻止跨连接重放，更新/删除/TRUNCATE禁止；Down先锁相关表，再拒绝任何dispatch或成功历史，备份恢复保留围栏。

仅明确COMMIT成功后可产生内部binding；普通dispatch接口仍只返回dispatch_recorded/execution_allowed=false，不暴露该binding，其无网络UoW不执行Add、不改写原task/authority/preparation/Arm/result或任何保护。

专用MovieUpgradeNativeSendRepository由显式构造的同一Manager native driver、私有dispatch verifier和接受schema68的仓储组合，尚未注册到worker/App/public升级入口。原账号/对象权限及已有授权检查先于Manager预算；native driver在共享客户端预算内校验冻结client配置摘要，仅为支持原字节上传的native qB客户端创建本次attempt。原连接与gate覆盖Arm、明确提交的一次性dispatch、同步Add和结果写回；attempt及预算至同步调用真实返回后才可关闭。重复/并发Close不能释放仍在运行的调用资格；原连接丢失时持久Arm/dispatch阻止重发，仍在执行的HTTP保持客户端预算。

私有dispatch proof绑定不可伪造的原session/issuer身份、精确descriptor、dispatch与Arm规范摘要、原字节SHA256及复制字节，只可消费一次。native verifier在原连接复验持久dispatch/Arm快照、原session/key/nonce和有效租约后，将同一复制字节及冻结save path、infohash、操作tag交给同步Add；公开回执、JSON和调用方任意proof不能签发执行能力。native qB复用无GetBody的原字节发送器，不因HTTP/2或会话刷新自动重放Add。

私有返回proof仅在该attempt同步调用真实返回后创建，绑定同一dispatch/调用身份和原Arm；明确成功记录confirmed，失败或不明记录unknown，不保存外部错误原文。具体类型、同一实例、原issuer、完整binding及attempt存活均由验证器复核，复制/JSON/跨调用替换拒绝。结果写回使用独立于调用方取消的有界context，复用schema67原任务及当前权限/保护校验，HTTP期间配置变化不改绑原调用；连接丢失或写回失败保留原围栏和保护，不盲目重发。该发送接线已按显式SEND job注册受控worker/App，下述R3只读观察已按受控job接线注册worker/App；周期跟踪、受控退休回收或公开升级确认/UI尚未实现；测试中的真实native HTTP不等同生产下载器/NAS验收。

**单电影升级显式SEND命令与job围栏（schema71）**：准备完成后必须由原用户提交一条新的显式确认命令，按当前账号、对象及既有业务锁序重验完整准备、原任务、原图和保护。窄命令仓储同事务写不可变movie_upgrade_send_job、精确command/operation/task/actor安全payload与固定dedupe/max_attempts=1的job，以及审计/事件/原键回执；同键恢复历史queued/execution_allowed=false，不创建执行能力。旧reserved/prepared回执、preview、job租约或worker身份不能替代此命令；不自动扫描旧记录生成发送任务。

专用job-fenced sender在原连接/gate内一次性捕获完整running claim及session/nonce。Arm、dispatch、私有证明消费和原任务结果提交均按数据库时间复验owner/token/state/有效租约及完整claim；不heartbeat、换主、重排队或重新捕获。复用schema66/68持久物理许可和dispatch，不另建重复许可；同步调用真正返回之前不释放共享Manager预算。confirmed/unknown和精确job成功同事务提交，强制延迟约束与最终比较保留；过期或失联不能发布陈旧终态，围栏和全部保护保留，转人工而不盲重发。HTTP期间配置变化仍按原调用记录真实返回，不重绑当前下载器；旧任务、文件事实和保护不改写。

schema71同时窄扩展65两处禁止job及Go完整准备基数规则，只有完整验证的发送/只读观察绑定可存在，通用入口仍不能发送。未提供新命令的旧native/Arm/dispatch在71下拒绝；proof消费无条件重新核验当前已安装规则，因此71之前已提交但尚未消费的dispatch也不能在迁移后绕过命令开始Add。已实际开始的历史调用可在原有效租约及身份合同内写回。新命令/claim不可变，仅保存安全job快照及原图摘要，无raw torrent、passkey或下载链接。Down先锁完整依赖并拒绝历史，再按依赖顺序删除和逐字恢复70两处规则。窄命令仓储、job-fenced发送及下述受控worker/App已本地验收；公开升级模式/预留HTTP已接入且固定不可执行；公开准备HTTP已接入且固定不可执行，公开SEND确认及UI尚未接入，既有受控R3观察继续独立工作。

**受控SEND取源及worker/App（schema71）**：只领取新显式命令创建的queued、attempts=0/max1发送job；不自动发现旧reserved/prepared记录，不续租、接管expired running或安排后继发送。取种子前从准备记录重建原账号，按现有业务锁序复验当前权限、完整准备图及有效running claim，冻结整行claim与命令。受控内存中解密原源链接和索引站凭据，经已有有界、拒绝重定向的Torznab取源路径获取字节；先比较准备时raw_sha256，再由原生检查器签发绑定完整电影目标及原字节的私有proof，不持久化raw torrent。取源期间的claim、权限、配置或证据变化仍由71 sender复验拒绝；物理发送复用App原Repository/Manager预算。worker同步等待真正返回，不单独Succeed，结果及job终态仍同事务；失败仅有界安全终结，物理许可及原任务/文件事实/保护不自动释放。App在所有取消分支排空该worker并最终等待实际返回。

有效显式SEND已排队或持有有效running租约时，自动Wanted投影写回在原policy/subscription/root/unit/Wanted锁内暂缓；只接受71的精确命令/job绑定、未到期原证据和当前完整保护摘要，不发布虚假当前投影。候选调度采用同一条件排除暂缓项，避免占满其他订阅批次。job终结、租约或证据过期、完整保护变化后恢复自动评估；显式跳过/范围变更不被暂缓，仍立即使旧发送失效。旧schema的直接投影先探测71表是否存在，历史合同不被升级。

**公开升级模式及冻结预留（schema71，无执行）**：`POST /api/v1/subscriptions/{id}/upgrade-modes`只接受显式confirmed的disabled/stop_at_cutoff与当前intent；`POST /api/v1/subscriptions/{id}/upgrade-reservations`绑定当前旧文件、候选、完整预览版本与expected_client_digest。两者经现有Cookie/session、CSRF及单一Idempotency-Key接入原写用例，严格拒绝未知、重复和额外嵌套字段、无确认、非法UUID与不支持模式。预览在原账号/对象授权的同一RR快照中、最终有效期检查之前读取下载器不透明配置摘要；内部观察快照不序列化该传递字段，不改历史冻结seal。首次与同键恢复均返回原202结果，预留固定reserved_not_executable/execution_allowed=false；恢复仍锁定并复验当前账号/对象，不以旧回执提权。不取种子、不准备、不创建发送job、不Arm/dispatch/Add，原文件事实及保护保留。公开准备见下段；公开SEND命令、对应UI和生产下载器/NAS验收尚未完成。

**公开升级准备与原键恢复（schema71，无发送）**：`POST /api/v1/subscriptions/{id}/upgrade-preparations`只接受显式confirmed及operation_id，不接受调用方proof、raw torrent、源链接、owner或token。现有Cookie/CSRF、单一Idempotency-Key及严格JSON准入后，先在原账号/对象锁序下校验当前用户及路由订阅，再只读查询历史准备记录。回执恢复从不可变准备记录重建原owner/token/raw_sha256指纹，核对原任务、原请求键、原202及规范安全响应；真实租约到期或源/根/下载器变化后仍可恢复，但不授予执行权、不重新取源。首次准备由服务端生成owner、申请原60秒租约，在同一当前权限与完整准备图校验下读取加密源及重建目标，事务外经现有有界Torznab取回原字节、内存解密并签发原生proof，再交原准备事务复验并提交任务/准备/audit/outbox/回执。结果固定prepared_not_sent/execution_allowed=false；不创建发送job、不Arm/dispatch/Add，不释放原保护。并发首提只允许原租约持有者取源，其他请求只作回执读取或冲突。无效metainfo按既有人工复核合同返回409，不泄露原生诊断。

有效准备租约在原authority/actor/unit及未到期源证据下，仅于leased且数据库租约有效时暂缓自动Wanted投影写回；候选批次同条件排除。准备提交后，在原证据期限内对完整prepared任务且尚无SEND命令、permit、dispatch或返回记录的确认窗口继续暂缓。已显式确认且原授权未到期、账号/订阅意图有效、三类占位仍完整的未准备预留，也暂缓该Wanted自动写回；此时必须无准备租约、准备记录、任务或发送授权，预留不触发取源或发送。两处候选批次使用同条件过滤；原期限失效、跳过、关闭升级、撤权或任务终结后恢复，不发布虚假当前投影，不屏蔽显式Wanted/策略/源变更，所有后续事务仍复验原冻结事实。

**公开升级SEND确认（schema71）**：`POST /api/v1/subscriptions/{id}/upgrade-sends`仅接受operation_id、task_id和显式confirmed，沿用Cookie/CSRF、单一原键及严格JSON准入；不接受job/owner/token/proof或下载配置。路由订阅绑定在原当前账号/对象锁序内于历史回执读取前核验，历史恢复和事务尾部重复核验；内部零订阅合同和原JSON指纹保持兼容。确认复用原专用命令事务创建唯一SEND job，原键仅返回原queued回执，不创建第二job；历史reserved/prepared回执不触发发送。正式App复用已验收的取源、原SHA校验、Arm/dispatch和原字节Add及结果/job同事务，不明返回仅由既有R3一次观察转人工；重启不重发、不接管，旧文件/事实/保护不变。公开界面已在既有电影订阅详情内接入真实文件与候选选择、手动站点检索、只读品质比较和模式/预留/准备/SEND四步独立确认；没有自动检索轮次的已拥有电影仍可打开入口。日志按当前账号/订阅绑定，严格验证持久参数和各步回执；Web Locks包住读合并、写前落盘、HTTP与闭合，未知结果保留原键，历史回执重新校验后才允许下一步。服务器确认先闭合内存再保存，存储失败只允许修复；损坏/跨账号日志阻断新键且提示不被资源刷新清除。SSE/版本变化和切换选择取消读请求、清除新比较与勾选，旧响应不得授权新选择；已确认原命令仍可恢复。queued仅表示发送命令排队，不展示为下载完成；周期调度/worker/App及受控回收仍未验收。

**单电影升级已确认发送的原生跟踪（schema72；仓储/native切片）**：仅原发送结果为confirmed、原authority/preparation/Arm/dispatch与完整原任务及保护图一致时准入；unknown仍走独立R3。原submitting任务整行保持冻结，跟踪不改写原任务、文件/事实、lane、reservation或需求。当前活动原账号/对象权限与冻结下载器配置摘要在准入、私有查询证明消费和写回时复验；历史成功回执另按当前账号/对象授权恢复，不要求过时配置仍可执行，不产生查询授权。

每次观察在同一数据库事务追加不可变观察、60秒租约、物理许可和成功账本，绑定原图摘要、完整descriptor、原键、backend PID/启动时刻、nonce与operation固定键独占session锁。单operation按前驱唯一串行：首个观察或前驱已返回tracking且数据库记录时间距今至少30秒才准入；尚无结果的许可、断连、过期或已终结结果均不能接管或重观。查询只在许可提交并再次复验同一原连接/锁/完整行后开始；返回记录与完整成功账本同事务提交，最后阻塞操作后按数据库当前时间再次检查租约。Down锁完整依赖并拒绝观察/许可/结果及相关历史，备份恢复保留孤儿许可阻断。

native驱动与发送/R3复用同一Manager预算；持有原session gate与预算直到同步调用、私有返回验证及写回事务结束，取消不提前释放仍执行的调用。具体返回proof必须是原attempt/issuer内签发的同一实例，完整绑定、复制/伪造/跨调用替换均校验。先读唯一operation tag并核对infohash/save_path/大小；下载中记录tracking，后续受30秒冷却约束。完整分支要求第一次完整状态、qBittorrent大小受限导出torrent解码后的原infohash/完整电影目标与精确文件清单；Transmission无导出能力，仅以发送准备时私有检查并冻结的原infohash与完整清单作基准、native完整文件清单/大小/进度以及第二次同身份完整状态全部匹配，才写completed/organization_review_required；缺失、歧义、失败或冲突写manual_review。全部execution_allowed=false，完成证据不等于整理、保护释放或退休授权。仓储/native合同由下述schema73独立job运行时复用；周期跟踪不能更新原冻结任务。

**单电影升级周期跟踪运行时（schema73）**：独立不可变tracking job绑定原operation/task、原账号、前驱观察与完整queued行；首轮和每次已返回tracking且按数据库时间冷却30秒后的前驱各只能生成一个job，最多每批25条。调度在账号、业务与operation串行锁下重新复验完整原图和永久去重；原发送confirmed才允许进入，unknown继续走原R3；manual_review与completed均停止自动观察。首行排队证据和一次性完整running领取另持久化，不能靠载荷中加入operation字段通过原任务的通用job入口。

准入、私有查询消费与返回写回均复验原用户对象权限、完整任务绑定、owner/token/state/有效租约和完整领取快照。观察与物理许可只在完整领取绑定后提交；原结果记录与该job的成功终态、审计/事件/幂等结果在同一事务提交，尾部按数据库当前时间再次验租约。绑定后的running行禁止续租、换owner/token、重排和重领；允许无成功结果时安全失败或dead-letter，但保留物理许可。已入队/领取/许可的历史永久去重；过期running、断连和孤儿许可不自动接管，原仓储入口也不能绕过既有job绑定。

App在启动及每30秒运行专用调度，由专用worker按一次领取执行，不单独调用通用Succeed、不续租、不重新Add。原生跟踪复用现有下载器Repository/Manager预算，取消与关闭先停止调度并等待同步原生调用和结果事务排空，再关闭数据库；保留原冻结submitting任务、旧文件/映射/事实、需求和全部占位。completed仍仅表示organization_review_required，不产生升级整理、保护释放或旧文件退休权限。schema73 Down锁完整依赖并拒绝绑定/领取、job及相关账本历史；无历史的73→72恢复schema72函数原文。

**单电影升级整理复核与冻结授权（schema74）**：只有原发送confirmed、原生跟踪completed/organization_review_required且可信完成回执与账本完整的操作可进入只读复核。按原账号、策略、订阅、根和单元锁顺序复验原冻结任务/物理返回/完整保护，并冻结完成观察及其账本、当前账号安全摘要、当前策略修订、旧事实品质、候选品质、电影身份、根、精确单视频源与占位证据。当前stop_at_cutoff、未跳过、原策略修订/根健康/电影版本与身份均须有效，旧文件只能映射该目标单元，当前品质须允许升级且未达到cutoff。调用方明确确认并提交完整input_stamp后，同事务追加唯一不可变整理授权、审计、事件和幂等成功结果；初次插入和事务尾均比较完整授权行、当前账号/对象权限、完成账本与冻结摘要，尾部变更全部回滚。

授权固定organization_authorized_not_executable、execution_allowed=false；不创建transfer_plan/step/job、不查询下载器或Add，不释放需求/占位，不修改原任务/文件/映射/事实或退休旧文件。原键恢复在当前账号/订阅权限下验证原请求绑定和原成功账本，模式关闭后仍可恢复历史回执，不重做观察或要求原租约；改变原命令不能恢复。此授权不赋予通用UpgradeMediaFile路径任何新执行能力。schema74 Down锁完整依赖并拒绝授权及相关历史；无历史74→73保留原函数原文。后续受控整理执行须另行绑定冻结授权和领取、完整校验恢复及最终保护释放，尚未接入。

**单电影升级整理物理计划准备（schema75）**：在schema74冻结授权之后须另行明确确认，授权原回执不能自动产生计划。仓储先按原账号/策略/订阅/根/单元锁序复验当前权限、完整原图、可信完成账本与授权input_stamp，事务外通过配置根能力持有全部根槽位，只读观察当前根、非符号链接的根marker、精确源文件和仍在库旧文件；不创建目录、硬链接、job，不退休旧文件或释放保护。仅接受当前冻结下载器路径映射所解析的受控源根、完整单视频长度/设备/inode/mtime，旧文件实际stat指纹须精确匹配已验证library_file，命名目标必须不存在且源与目标同设备。观察开始到事务尾采用数据库时钟最多30秒，等待根或DB锁不能延长有效期；取消时根槽位一直持有到实际观察返回。

随后再次复验完整授权/当前账号和业务证据，同事务唯一追加movie_upgrade_organization_preparation、精确单步transfer_plan/step、审计、事件及成功回执。准备行/计划/步骤均冻结完整首行，状态固定organization_prepared_not_executable、execution_allowed=false，plan为review_required/requires_approval=true、零execution_token且无owner/lease，step为pending。初次插入及事务尾比较完整行集合和原旧文件/映射/事实/占位；SQL拒绝增删改步骤、更新/批准/领取计划、篡改准备行和TRUNCATE。通用ExecutePlan与现有批准/领取路径不能执行这份计划。原键恢复只复验当前账号/对象权限、原命令绑定及完整历史成功账本，不访问文件系统，不要求原源目录仍存在或原模式仍启用，不重新准备。Down锁完整依赖，存在物理准备或相关历史即拒绝。后续专用执行须新增明确命令、完整领取绑定、物理独占、恢复和最终入库/退休/保护释放协议；schema75本身不提供这些执行能力。

**单电影升级整理执行命令与逻辑领取（schema76；无文件操作）**：schema75准备完成之后还须新的明确确认。命令、唯一movie_upgrade_organization_job、精确queued job、审计/事件/幂等回执同事务保存；不能把原organization_prepared_not_executable回执当成执行命令。绑定原operation/preparation/actor、完整准备行及queued job首行，payload仅含organization_job_id，kind固定movie_upgrade_organization_execute、max_attempts=1。新命令在原账号→策略/订阅→根/单元锁序中复验schema74授权、可信完成账本、当前完整basis和schema75原计划/步骤/物理快照。首次插入和事务尾都精确比较完整绑定/job行，防触发器改写后成为合法基线。原键查询及重放只按当前账号/对象权限、原命令绑定和原成功账本恢复原queued回执，不访问文件系统，也不要求原模式或运行租约仍有效；改换命令不能恢复。

真实队列只允许一次queued→running：首次attempt/token均为1，随后以原账号、当前完整业务证据和有效JobLease明确捕获唯一movie_upgrade_organization_job_claim，其完整claimed_job、领取时间、审计/事件/回执同事务提交。事务尾在所有锁/账本写入之后重验账号、完整快照和数据库当前租约；等待跨过租约必须回滚，不能留下领取记录。命令/领取不可UPDATE/DELETE/TRUNCATE；job禁止续租、owner/token改变、重排、删除和伪造succeeded。允许已有queued/running job安全failed/dead终止而保留原领取历史，重启、备份恢复和原键恢复均不能创建第二次领取。队列回执固定queued、领取回执claim_captured_not_executable，execution_allowed均为false；它们只是逻辑冻结，不是文件执行凭证。schema75计划仍review_required、步骤pending，通用批准/领取/ExecutePlan持续被拒绝；未注册新App worker，不实施硬链接、入库、旧文件退休或保护释放。专用物理独占/可信文件执行/恢复/最终原子入库仍须后续合同。schema76 Down先锁完整依赖，存在命令/领取/job或相关审计/事件/幂等历史则拒绝，空历史76→75保留原75定义。


**单电影升级整理原生物理许可与返回（schema77；由下述受控worker消费）**：只消费schema76已经捕获的原始running领取。以原operation固定独占会话锁、原backend PID/start、原发送会话nonce及完整claim/job绑定签发唯一持久许可；必须证明仍持有原exclusive session lock，事务锁或shared session lock不能替代。许可与自己的审计/事件/回执同事务保存，窗口最多60秒且不超过原job租约。保存许可前和物理调用前，在全部阻塞锁/账本之后复验账号、当前完整业务证据、原claim/job/计划/保护和数据库租约，并在最后重查文件观察未满30秒。许可一旦提交，即使尚未开始文件操作、原连接丢失或租约过期，也不得自动删除、接管或再签发。

原生驱动在事务外持有源根和目标根的现有内核独占槽位，重新确认配置根/标记、精确旧文件与源文件指纹、目标不存在，调用现有防符号链接/路径逃逸的单文件Hardlink，不做跨设备复制或覆盖目标。同步返回是私有可信凭据，没有公开Record/Returned=true接口。取消或逻辑超时不提前释放槽位；即使驱动忽略取消，也等待物理返回、重新观察和结果写回结束。只有返回无错误且根/标记/旧文件/源文件未变、目标device/inode/size/mtime与源文件完全相等，才记录linked_pending_ingestion；返回错误或证据不符记录manual_review。结果、结果成功账本与原job精确succeeded终态同事务提交，数据库双向约束禁止有结果而job仍running，也禁止无匹配结果而伪造job成功。写回再次验证原会话/许可/账号/原job/完整业务和受保护行，并在所有检查后以数据库时间确认租约仍有效；拒绝时结果、账本和job终态全回滚，原持久许可继续阻止重做。

原键结果恢复只读校验当前账号/对象权限、不可变原命令/claim/permit/result/终态job及全部原成功账本，不重新观察文件，不依赖历史模式或租约继续有效，不再调用Hardlink。备份恢复保留已返回结果和无可信返回的孤儿许可，二者均不重做；有任何物理许可/结果/相关账本历史则拒绝schema77降级，并发Down等待原写事务后检查。两个结果的execution_allowed均false：不授权后续入库/退休/回收。schema75原review_required计划/pending步骤和原下载任务、旧文件/映射/事实/保护全部保持不变；新文件入库由schema78独立提交，退休与保护释放使用后续受控合同。已链接但未入库的可信结果由schema78恢复；无可信返回的许可保持复核，不能根据逻辑终态重试。

**单电影升级可信链接入库与受控worker（schema78）**：`movie_upgrade_organization_ingestion`只消费原不可变`linked_pending_ingestion`结果。按当前原账号与对象范围验权，事务外持配置根物理槽位重新观察根/marker、旧文件与新链接的精确device/inode/size/mtime；观察前取得数据库时间。短事务重验完整原授权、业务版本、计划/claim/job/返回和全部保护图，原子记录新文件、唯一映射、品质imported事实、根水位及审计/事件/稳定回执；同一事务调用既有PublishImported，冻结各媒体服务器刷新投递与通知合并成员，事件持久化不表示实际投递成功。首行与延迟提交围栏核对完整基线、精确自身写入和五秒观察有效期，尾部篡改整笔回滚。返回`imported_pending_retirement`，保留旧文件/事实与三类占位。历史恢复仅校验当前权限和原不可变记录，不依赖旧租约或重新硬链接。

App拥有专用整理worker的启动、取消和等待，消费schema76固定job kind，捕获原领取后执行schema77；取消时等待实际I/O返回。周期有界查询尚无入库记录的可信链接结果，逐条按原账号恢复schema78入库；重启继续入库，无可信返回的孤儿许可不重放。公开整理在`/api/v1/subscriptions/{id}`提供`GET upgrade-organizations/{operation_id}`和`POST upgrade-organization-previews`，再由独立`upgrade-organization-authorizations`、`upgrade-organization-preparations`、`upgrade-organization-jobs`明确确认。OpenAPI和生成客户端同源，每步核对当前活动管理员会话及订阅范围，严格拒绝重复/未知字段和非法幂等头。前端三项确认沿原journal/Web Locks恢复链保留原键/完整参数；SSE重置重读当前状态及材料戳，未变时保留整理勾选，实质变化或读取失败才清除。跨标签写入期间延后读取storage通知，命令结束后同步最新持久回执。


**单电影升级旧版本受控回收与退休（schema79）**：新文件可信入库后，当前管理员通过`POST upgrade-retirement-previews`读取原旧/新文件、同一不可变策略下的分数和完整input_stamp；`POST upgrade-retirements`须独立明确确认、原幂等键及完全相同的材料戳，才能原子写入不可变命令与成功账本。默认保留7天，只处理同一配置根、唯一旧文件映射及完整本次保护。App已有整理worker有界读取待处理命令，持根物理槽位在事务外重验根/marker、新链接和旧文件精确身份；短事务签发唯一owner/token/有效期绑定的持久许可。许可不续租、不接管，过期或失联不能重做。原生FD能力使用RENAME_NOREPLACE移入`.pulsemedia_recycle/{operation_id}/`、核对inode与源缺失并fsync目录；不原地改内容，保留下载源与保种硬链接。物理返回后再次观察，五秒窗口内原子写入退休结果、旧文件retired_at、旧事实撤销、本次lane释放、reservation consumed、需求imported及根水位与审计/事件/回执；精确预期副作用与延迟约束拒绝尾部篡改。不确定或失效结果保持全部旧保护并记录manual_review。状态查询区分排队/执行/人工复核/已退休，成功切换current_file_id为新文件并返回retained_until；前端原journal/Web Locks恢复命令，不因丢响应产生新键。历史回执与备份恢复仅经当前权限返回原结果，不重做文件操作。存在命令、许可、结果或相关账本历史则拒绝79降级。

**持续更优升级模式（schema80）**：订阅默认仍为`disabled`；两主动模式必须经现有管理员/对象权限及独立confirmed命令开启。`stop_at_cutoff`在当前分数达到正cutoff后停止，`replace_if_better`在达到或未设置cutoff时继续比较严格更优候选；两者共享独立品质事实、绑定策略、未知拒绝及同级优化默认关闭规则。公开确认、冻结预留/准备/发送、整理与退休均核对当前模式、原冻结mode和intent，模式改变令旧材料失效；历史成功回执只恢复原结果，不重新授权。自动Wanted排队及写回在两主动模式下使用相同完整保护条件，显式修改与过期仍生效。schema80保留原basis JSON格式以兼容原停止模式历史；订阅存在replacement模式或任何replacement授权历史时拒绝降级到79，即使当前模式已关闭也不能丢失历史语义。

**已满足电影的升级候选发现（schema134）**：仅活动管理员的有效单电影订阅在已满足且启用升级模式时参与；`stop_at_cutoff`达到当前策略cutoff后停止，`replace_if_better`继续发现。现有搜索循环每轮最多20项，每订阅六小时冷却；创建前按账号→策略→订阅锁序检查共享活跃搜索上限。复用interactive_search/source job、分页证据和站点预算，不建立获取round或下载。每轮保存不可变的搜索与输入摘要，调度指针单独更新；worker发送前和读候选时均重验原订阅、策略、根、文件、身份与权限。GET只读现有结果，不触发搜索；过时结果隐藏搜索/文件ID。页面对检索候选调用既有升级预览确认更优，再交给原电影升级界面逐阶段确认，不能自动预留、发送或退休。


**已满足标准单集的升级候选发现（schema143）**：仅当前有效管理员自己的TV订阅在升级模式与cutoff允许时参与；标准集已播出、单个旧视频及唯一有效映射、可信未过期事实和健康物理根缺一不可。复用现有搜索循环、站点预算和分页证据，每批最多20个单元、单元六小时冷却，并与交互/电影搜索共享账号活跃额度。原轮次不可变，当前指针单独保存；冻结原意图、策略、规范身份、文件/事实及当前代际最早成功扫描的物理根身份，排除扫描与派生投影水位。来源发送、完成及读取均复验当前材料，等锁后重读数据库时间拒绝过期事实；GET不触发搜索。页面展示全部持久候选，明确比较后交给单集工作区；交接再次确认原文件、标准集、当前订阅与唯一映射，不自动预约或下载。 同一轮次和当前文件范围内的后台重读保留在途比较；范围变化、当前权限读取失效或离开页面时取消并丢弃迟到结果，重读期间禁止交接。只读快照遇数据库40001并发冲突可有界重建完整读取事务，不重试命令或外部I/O。

**标准单集洗版（schema137）**：独立TV授权仅覆盖一个已播出的标准集、唯一有效旧视频及映射；拒绝共享文件、多集视频、整季替换和未证明编号。管理员在订阅详情选择真实库文件/规范集及交互搜索候选，比较后分别确认预留、取源准备、发送、观察、整理与旧版本退休。冻结原策略、意图、规范身份、候选、原端点、文件与根证据；扫描token、派生mutation水位及更新时间不作为授权版本，真实根代际/身份/健康和文件映射/事实仍逐阶段校验。专用owner/token/有效租约和物理根锁包围真实I/O及结果写回；持久许可和可信物理返回证据防止连接丢失或取消后的盲目重放。新文件可信入库后才可将旧视频无覆盖移动至同根回收区，保留原字节和保护链。每阶段独立原键回执；GET只恢复数据库结果，不请求下载器或操作文件。普通下载、观察、整理入口继续默认拒绝该授权，不能借新增TV实现放宽原隔离。


**已有文件与回收区的显式维护（schema142、144）**：管理员Cookie可预览同根单文件的新相对路径，目标必须不存在，当前文件须有唯一有效规范映射及可信导入事实；仅移动视频，不批量应用模板或移动附件。按原键明确确认后，以短事务签发永久物理许可，在根FD能力与独占下执行原子不覆盖重命名，原路径变化、根代际/最早成功扫描物理身份、权限、事实或租约变化均拒绝。映射与事实历史保留，原许可与结果不重放。回收清单仅读持久退休历史，文件大小不等于可释放空间；显式预览才观察原回收文件、当前替代文件、实际分配字节与硬链接数，并冻结当前版本。恢复只到原路径，保持新视频，建立新的库文件与导入事实，旧退休历史不复活；到期清理须另行确认且再次核对保留期、替代文件、精确身份和物理返回。取消或连接丢失仍等待实际I/O返回，不确定结果保留排他许可并要求人工核对；GET只读原账号回执。两类升级旧视频均支持，保留期届满本身不触发删除，不提供自动清扫。 回收历史可按电影/剧集与原相对路径关键词检索，关键词去首尾空白并作忽略大小写的字面匹配；筛选在分页前执行，游标绑定规范化筛选范围，无筛选时兼容旧游标。筛选和翻页只读取数据库，不取得观测、恢复或清理授权。

**单电影升级R3观察事务（schema69；无网络）**：仅已提交的原movie_upgrade_send_result.status=unknown连同完整authority/preparation/Arm/dispatch/task及保护图成立时准入；缺少返回或confirmed不能以backend/session锁/旧租约消失代替物理返回证据。按现有当前账号、策略/订阅及对象锁序验证权限，重验冻结下载器身份，原发送租约不必继续有效。新专用连接复用同一operation独占session锁与nonce证明，提交独立60秒观察租约及持久permit；同operation唯一，不接管孤儿permit，不自动重观。

观察和permit首次插入前构造完整期望，原键、原图与完整保护摘要进入不可变记录；所有写入、成功账本及延迟约束后复核，最后阻塞读取之后按数据库当前时间检查新租约。只有构造时注入的可信返回验证器证明本次同步查询已返回，才追加绑定完整观察/permit摘要的结果。exact_match、absent、ambiguous、unknown均仅manual_review/execution_allowed=false；保留原unknown发送、submitting任务、旧文件/事实与全部lane/reservation/需求，不重发Add、不退休回收。原独立无网络接口保持可用；专用native观察及受控worker接线如下，公开模式/预留不提供R3或发送授权。

执行接口与回执恢复分开：原执行session维持一次性与有效租约约束；两个独立只读历史接口先核验当前活动账号/对象范围，再验证原owner/key、admission/result完整成功账本及不可变行。历史恢复不要求旧连接、session锁或仍有效的执行租约，返回已提交的原回执而不创建/续租permit或授予查询能力。准入请求键持久化并纳入完整插入期望。Down先锁真实完整依赖表，再明确拒绝观察/许可/结果及成功历史；备份恢复保持不可变围栏。真实60秒跨期、关闭/过期后恢复、撤权等待、尾部回滚、DML/DDL分权及并发Down均有Main原生PG验收。

**单电影升级native R3观察接线（schema69）**：显式构造的MovieUpgradeNativeObservationRepository组合当前仓储、同一Manager的共享client预算与native只读驱动，交互仓储不注册public入口，受控job路径按下文接入worker/App。当前账号/对象、真实unknown返回、完整原图及冻结client摘要先于预算；持原观察连接与gate完成准入、同步查询及写回。原无网络准入/结果方法仅抽出持gate的私有体，保持独立接口与历史恢复语义。

私有query proof只在准入COMMIT明确成功后创建，绑定原session/issuer、精确descriptor及完整观察/permit摘要；消费一次，在原连接复验当前权限、冻结配置、完整图/账本、session锁/nonce及数据库时间租约。消费或提交不明不查询，不接管或续租许可。native adapter只暴露按操作tag的有界只读列表，不暴露Add/删除，复用严格原生字段及重复/非ASCII别名防护。唯一hash、save path、总字节及唯一操作tag全部相符为exact_match；完整空列表absent；多项或不符ambiguous；错误/畸形unknown。所有结果仅manual_review，不改变原任务、旧文件/事实或任何保护。

返回proof仅本次同步查询真实结束后产生，绑定同一attempt实例和原观察/permit；布尔、复制、跨attempt、改绑定或已关闭scope拒绝。查询前取消不签返回证明；忽略取消的同步查询继续持gate/共享预算，返回后使用独立有界context完成结果事务。连接丢失仍持运行中调用预算，持久permit阻止另一实例重观；冻结client变化或失去原session拒绝陈旧写回并保留保护。此本地native HTTP/PG验收不代表生产下载器/NAS验收。


**单电影升级job绑定R3观察（schema70）**：显式MovieUpgradeNativeJobObservationRepository复用原native观察链与当前账号/对象锁序；job租约只是执行围栏，不能代替原用户权限。真实unknown、精确operation/task/actor安全payload、固定dedupe、max_attempts=1和当前running owner/token/有效租约一致时才准入。不可变movie_upgrade_observation_job同事务绑定job、观察、原图/完整保护摘要及完整claimed job行，不复制明文下载器配置或torrent。原69交互接口与独立历史恢复保持原语义。

同一原session/gate内，准入、私有query消费及结果事务都锁定job并按数据库时间复验完整claim和租约，不heartbeat或换主接管。查询忽略取消仍持共享预算，job过期或终止不能释放物理查询资格。结果先同事务构造精确job成功行，再写审计/事件/幂等账本、执行延迟约束与最终复验，不另行调用queue.Succeed。所有结果仍manual_review，原unknown、下载任务、旧文件/事实及全部保护不变。

SQL首次INSERT独立核验完整claimed行，必填JSON字段空值安全比较。无结果时仅允许完整原running claim转dead/failed并清空owner/lease，拒绝续租、换主、重排队和后续终态编辑；物理permit保留。schema70替换65两处禁止job规则，仅为精确只读观察job放行，通用发送job仍拒绝；Down锁定完整依赖、拒绝已排队或持久历史，并逐字恢复原两函数。job绑定仓储与真实Queue/native HTTP/PG已验收；受控调度和worker/App按下文接入，public升级入口仍待完成。

**单电影升级受控R3调度与App运行时（schema70，无新增迁移）**：窄调度仓储每轮最多处理25个真实UNKNOWN候选；先按持久准备的原actor验证当前账号/角色及对象范围，遵守原业务及保护行锁序，再取得operation排他锁并重复验证。任何历史观察/许可或任何状态的观察job都永久阻止重复调度；原UNKNOWN发送permit保留，不作为排除条件。固定安全operation/task/actor payload、全状态dedupe、max_attempts=1及完整首次job期望均由服务端构造；跨写及延迟约束后复验完整原图/保护/权限再提交。旧queued可首次claim，running/expired、已有准入或终态job不接管、不重排队或创建后继。

App显式组合原下载仓储execution Manager、原master key、native verifier与job绑定仓储，复用client预算。同步worker只claim本kind，严格解析三字段payload，原用户仅由持久准备及当前账号重建，并在业务锁后复验完整claim。直接调用ObserveMovieUpgradeJob，由窄事务提交结果/job成功，不调用交互观察后另行Succeed；无heartbeat。失败仅安全死信终结，许可/保护不释放且不重观。worker纳入App取消和实际返回排空；忽略取消的调用结束前不关闭数据库、释放data-dir锁或预算。正式App重启不增加查询/Add，不采用过期running；结果仍manual_review，原任务/旧文件事实/保护不变。升级发送job、周期tracking、退休回收、公开升级确认/UI和生产下载器/NAS未验收。

策略create/publish/restore及订阅create/state/skip/scope/release_block命令先拒绝Token/system/service，在短事务锁定当前活动用户并核对实际role后，才读取幂等回执或取得policy/订阅业务锁。用户行锁保留至事务终点，锁等待后校验当前值；旧内部策略发布/恢复同样受控，成功回执不会给已停用或角色变化的账户恢复写权限。

策略以 immutable revision 发布，current_revision 单调增加；回滚复制旧内容成为新 revision，不倒退版本。冻结计划/评估引用的版本不能清理。

**策略管理实际切片（2026-10-08，schema22；非 M2 全阶段放行）**：`policy.Service` 只依赖纯 quality 与 `ports.PolicyRepository`，app 注入 `NewPolicyStore`；GET policies/current 及指定冻结 revision，POST create/publish/restore 强制管理员、命令 key、expected revision，配置 ID/版本/规则/相关订阅 intent/投影失效/成功审计/event/稳定 receipt 同窄 UoW。重复请求只回原 revision 指针，指纹变化或陈旧版本 409；成员不能以重放或 Token scopes 提权。`00022_immutable_policy_revisions.sql` 禁修改/删除已发布规则，恢复复制旧内容为更高 revision。HTTP preview 读取当前或指定版本，或者验证草稿，无网络/文件副作用、无固定 rev1/默认成功回退；数据库失败非空成功。允许集合只接受已知分辨率/来源/有限非负分数、cutoff、HDR 偏好及有界不重复拒绝词/发布组。纯 `KnownProfile` / `EvaluateCandidate` / `AssessOwnership` 共用允许语义：未知 hold，已知但未允许 reject；acquire 不要求升级 cutoff，允许的 0 分候选也可补缺。设置页有明确标签/版本编辑、预览、冻结历史恢复；发现可选策略，版本化绑定可切换引用，旧客户端省略策略保持原冻结引用。未确认发布锁草稿并保留同 key，已确认发布但刷新失败只能重读，409 要重新加载；有 abort owner、桌面/390px 实际验证。此策略切片不构成自动 upgrade 验收；当前订阅内单资源拒绝管理按下述schema56合同执行。

**自定义候选质量词（M9-03，无新schema）**：词规则随不可变`PolicyRules.custom_words`发布；省略或显式空数组在新版本中表示无自定义词，历史JSON编码、规则摘要和旧回执保持原形。最多64条，每词1–128字符；字面忽略大小写匹配，ASCII字母/数字采用词边界，`DV`不误中`DVD`、`CAM`不误中`SCAM`，不执行正则表达式。排除先于必含，全部必含词必须满足；每词只计一次，分数限±1000，拒绝空白/控制字符、重复词、互斥标志及无效声明。`preference_score`只影响新候选的相对选择，电影、TV及交互结果在严格准入后按品质分与词偏好排序；品质分、cutoff、持久品质事实和执行授权分不加入词分，未知品质不因词加分变为已知。重命名库存和升级比较不能从当前路径补造原发布词证据。新交互会话在创建事务内读取服务器中的订阅绑定版本或默认当前版本；原键恢复先读取原快照，不再读新策略。公开策略请求在认证后、生成绑定前拒绝每层重复键、大小写别名、未知字段及缺失词字段；策略恢复复制旧内容为新版本，旧会话和旧命令回执不改写。独立偏好分不授权预约、发送或文件操作，既有账户、范围、配置、租约及事务尾部复验继续生效。

订阅端点/下载器/策略切换同样在当前用户锁后处理回执与expected intent，在原业务锁下原子刷新Wanted并失效旧轮次。自动轮次禁止空索引器绑定作为全部端点通配；空绑定等待且不创建轮次或任务。策略create/publish、绑定切换与状态命令的界面按用户/对象保存未确认的原键/请求/版本，刷新或会话恢复后同键重放；已确认后读取失败只重读。保存内容不含凭据，服务器仍重检当前账户、对象和版本，不以浏览器存储授予权限。

**订阅内单资源拒绝（schema56）**：`release_block`以(subscription UUID,candidate UUID)为唯一范围，外键绑定现存订阅/候选；blocked=false保留解除历史，非空历史拒绝降级。只接受该订阅持久轮次/端点/RSS关联或原屏蔽记录中的候选，每订阅最多100个当前屏蔽。沿用subscription control的当前用户、对象范围、expected intent与原键回执合同，在同一短事务提交屏蔽状态、意图推进、Wanted绑定刷新、投影失效、审计和事件。旧轮次/未提交授权据意图版本失效；最终选择及共享来源取得均在订阅锁内另查屏蔽状态，不能借另一订阅的来源越过拒绝。它是当前订阅的负面获取门禁，不更改全局候选、密文、品质、纯规则cache、rank或其他订阅；不重发/删除已提交下载，不释放unknown/在途保护。候选解释优先展示所有当前屏蔽（包含早于最近轮次的条目）与有界最近候选，规则通过不代表可获取。界面确认屏蔽/解除，丢回执保持原键和版本，401清私有界面后仍可恢复该命令；范围排除/用户跳过不隐藏解除入口，范围重新纳入不清用户跳过，零目标不沿用旧分母显示完成。

文件与单元是多对多：一个 E01-E02 文件可覆盖两集，同一集也可以有多个文件版本。文件身份不等于路径：不同内容占用同一路径时旧 file 设 retired_at，新建身份，失效旧事实与确认，保留历史引用。匹配纠正同步更新关系与事实版本，不删除仍被历史事实引用的关系。

文件选项复用`GET /api/v1/library/mapping-options`，可选root_id和文件游标，每页最多100条。根筛选在LIMIT前执行，保留created_at降序/id升序，游标绑定根范围和时间/UUID；游标文件退休或新增更晚文件不改变续页边界。媒体支持本地标题/原名的字面包含检索、类型与精确TMDB ID筛选，使用独立的media_cursor及updated_at降序/id升序续页，并可按原媒体ID单独回填。媒体游标绑定检索条件，拒绝跨条件续页；并发更新可能移动排序，界面按ID去重但不宣称跨页快照。文件和媒体分别返回是否还有下一页及游标，翻页互不清除已有选择。该GET严格拒绝重复/未知参数、请求体及命令键，所有结果private/no-store，每页仍校验当前管理员；读取不访问FS、不改业务记录或创建任务。升级仅请求订阅目标根，交接时直接重读所选文件的权威映射和订阅，不把分页或搜索选项当作当前覆盖证明。

**识别纠正实际切片（schema29，2026-10-08）**：`library_file_unit.retired_at` 区分当前映射与保留历史，不删除被事实引用的 pair；`library_file.mapping_revision` 对关系变化单调递增。退役关系即时撤销旧 imported/confirmed；重激活只开当前关联，不复活旧事实。拥有/复用/状态/刷新及活动关系查询均过滤关系 retired_at；源关系/事实变化通过既有 facts_revision 失效投影。降级若有历史退役关系会拒绝，不能丢列而制造旧拥有。
`ports.FileMappingRepository` 与 `media.FileMappingService` 提供管理员已有文件的有界分页与持久媒体选项（标题/类型/TMDB ID检索、独立续页及原ID回填）、REPEATABLE READ 预览和明确覆盖纠正；选项、预览、命令与回执读取均在事务入口先锁定并核对当前活动会话管理员，API凭据/系统/停用/降级/不存在账户不能借用旧Actor权限。预览只是现索引/有效导入事实和当前元数据，不触盘、不新建物理存在证明。无 active imported 来源、根不在线/基线未完成、退役/非视频/不安全相对身份、媒体类型不符或来源品质冲突均 hold。命令必须明确 confirmed_coverage、目标媒体/最多20不同单元、完整 input_stamp 和 key；网络/文件动作不存在。窄事务 root→sorted旧/新unit→media→file（与 TV 元数据刷新一致，跨根不倒序） 锁后重新读取实际文件根/身份/映射/媒体/单元/事实及当前TTL；旧stamp拒写。退役旧关系/事实，新增映射派生 imported 只继承当前来源品质与最短期限、不扩大证据寿命；审计/事件/回执末尾以数据库实时时间再次检查原来源最短期限，过期回滚整笔关系/事实/根水位；root mutation_seq 与 file 水位/成功审计/事件/稳定资源回执同事务。审计保留前后revision、旧unit及来源/新增fact UUID，缓存不放root绝对路径或原事实内容。角色先于重放，无效/迟到媒体服务器确认在相同root/unit/file锁后复核活动导入关系并拒绝。
公开 `/api/v1/library/mapping-options` 与 `/api/v1/library/files/{id}/mapping` 接入 app 和整理页管理员明确面板。原任意目录扫描/搬运仍未开放；不把关系保存称文件新入库。界面按用户保存未确认命令的原文件ID/媒体ID/明确单元集合/input_stamp/key，详情关闭/刷新/真实退出再登录后重放原请求；GET观测不清掉原命令，不替代其回执；确认后刷新失败仅重读、历史和当前关系分开、明确 E01/E03不扩E02；390px 与正常cookie真实PG/HTTP验证。不是自动动漫绝对号映射、完整跨根整理或新文件扫描。

### 4.4 拥有与状态推导

默认只认订阅目标根的有效文件。库 A 有、目标 B 无时优先生成本地复用整理需求，源根须在线/可读；不能把 B 直接判为完成，也不能优先重复下载。

**跨根本地复用正式切片（schema82）**：批量预览使用同一数据库快照，按目标根可信拥有、其它根当前有效入库事实、完整文件映射和冻结品质策略选择候选；候选截断、未知品质或不可用根不能授执行。已有获取入口保留local_reuse_required围栏，明确确认完整覆盖、目标相对路径和hardlink/copy/reflink动作后，独立library_reuse_plan及完整单元保护、job、审计、事件和原键回执同事务持久化，不伪造远程release或下载。两个获取来源在同一单元行锁下排斥有效占位；回执恢复仍先校验当前账号与对象范围。

App现有受控整理worker消费复用job；跨根按物理身份有序独占，长操作续租但逻辑过期不释放仍在执行的物理槽位。文件发布前后及入库事务核对当前账号、策略/意图、根身份、媒体/单元、完整源映射和品质事实；copy/reflink先持久化独立不可变完整内容证据，不能覆盖批准材料。不可用源或空间不足进入等待；撤权、身份/内容或版本变化进入复核并保留保护。目标实际文件、完整映射、全部imported事实、标准media.imported、根mutation_seq、成功审计、job完成和本次保护结束同事务提交；只有目标根可信入库后才推导拥有。匹配纠正保留原历史引用并推进水位，旧扫描不能覆盖之后的整理/纠正结果。原键恢复、并发互斥、物理调用跨租约及电影/多集浏览器已经本地验证；DML与备份保留批准、内容和回执，持久历史阻止82降级，空迁移可往返。实际NAS能力与生产服务归M4验收。

推导顺序：用户 skipped → 未播出 awaiting_airdate → 根不健康/基线未完成 waiting_library → 拥有判断 → 活动占位判断。正常结果为 satisfied、missing、in_flight、below_cutoff、upgrading；低置信/未知属性与未决外部动作显示原因及待复核，不视为普通 missing。

根失联时保留原文件事实，但当前可用性变 unknown，暂停相关自动获取。已有效 imported 不要求 Jellyfin 在线才成立。

**当前状态读取切片（2026-10-08，非 M2-02 完整放行）**：GET `/api/v1/subscriptions/{id}/status` 通过 `ports.SubscriptionStatusRepository` 读取对象授权后的 PostgreSQL READ ONLY / REPEATABLE READ 快照；库健康/基线/generation、意图/引用的不可变策略 revision、单元日期/retired/facts_version、文件/事实/占位、端点预算及同意图轮次均在同一 MVCC 观测中读取。SQL 仅维护在 sqlc 查询；usecase 依赖 ports 与纯 domain 推导，不启动网络/文件 I/O 或改变 wanted 投影。响应包含 observed_at/version、逐目标状态/原因、独立 reservation_protected；无路径、凭据、链接、原始适配器错误。根失联/基线未完成、品质未知或无有效 imported 为 unknown；仅目标根有效映射文件及未撤销/未过期 imported 原始属性通过引用策略允许规则才 satisfied，遍历全部文件版本，confirmed 不替代失效 imported。acquire 不把 upgrade cutoff 当已有合格文件必须达到的门槛。submitted/downloaded 仅来自匹配真实 task/root 的有效观测，不能从 reservation 或 task.state 编造。当前落地表仍为 root-bound `unit_reservation`，TTL 到期不自动释放；不是已实现全局 acquisition_reservation/M2-06 共享。paused/archived、未来/退休/未知日期、未绑/离线/退避端点、缺少下载器与本意图轮次 deadline 有明确原因；原因不构成获取授权。订阅详情禁缓存、可取消读取/刷新意图，读取失败清除旧绿色，未知数不解释为文件丢失，保留季选择；列表计数显式标为旧投影。HTTP 200 为 private,no-store。2026-10-08 已进一步统一 Evaluator/调度投影与 reserve 的拥有语义，详见下段；schema21 已接通完整输入戳及提交即失效，见下段；这不等于 M2 全项放行。

**当前拥有/投影写切片（2026-10-08，非 M2-02 全项放行）**：`quality.AssessOwnership` 是只读状态、guarded projection 和最终获取共同的纯允许规则；只认有效目标根 imported 原始属性，检查全部版本，未知证据保持 hold，不把 rank/confirmed/映射存在当拥有。`subscription.NewEvaluator(repo)` 只依赖 domain/ports，app 注入 `ports.WantedProjectionRepository` 的 PostgreSQL 窄 UoW；按 policy→subscription→root→unit→wanted 锁输入，过时策略引用/预读意图变化拒写；只在锁内重读当前 skipped/日期/根/有效文件事实/占位后写 intent/stage/satisfaction/next_evaluate_at。事实 TTL 和日期评估使用全部锁取得后的数据库时间，last_evaluated_at 同步该时间，不能用等待锁之前的 BEGIN 时刻。已满足目标仍 5 分钟重算，不永久关闭；占位无有效 task 事实不伪造 submitted，逻辑观测过期不释放物理保护。投影不把网络配置门禁当文件拥有，scheduler 对纯 missing、已知文件拒绝、无合格候选仍经同一受控 StartScheduledRound 与最终 UoW；stale projection conflict 只跳过重建，不下单。manual acquire 锁 default current revision，再 root/unit 检查 baseline/健康与有效证据；自动 finalize 使用冻结 revision 的同规则。unknown source 返回 OWNERSHIP_UNCERTAIN 409 或 expired/ownership_unknown，保留观测/保护，零新获取；真实已拥有返回 already_owned。删除了仅映射 EXISTS 和 LIMIT 1/confirmed 独立拥有的旧 sqlc 捷径。finalize 的 wanted 锁移到 root/unit 后，避免与导入 root→unit→wanted 逆序；取得 wanted 后仍复核跳过/目标。策略发布锁 policy 后分配单调新 revision，并发发布不复用旧预读 revision。schema21 已补输入版本与精确时间失效；全局默认 lane/跨根共享、完整策略操作/候选 UI、健康恢复、TV/upgrade 尚待完成，仍不以局部故障测试替代全阶段验收。

**精确输入失效切片（2026-10-08，schema21）**：追加 `00021_wanted_input_versions.sql`，wanted 原位记录 evaluated intent、quality/过滤合并规则 policy ID/revision、root/unit/media UUID、root generation/health_revision、media/unit metadata_version、unit facts_version、控制状态/skipped 与 projection_valid_until。NULL 历史戳不回填为当前。source 的事实/映射/文件/占位/task state/round state 变化用 statement transition tables 以 UUID 稳定顺序推进 unit facts_version；metadata/identity、日期/retired、root 状态/基线/identity 推进对应 revision，禁止版本倒退。文件 size/mtime/fingerprint 改动同时撤销旧 imported/confirmed 并留 file_content_changed 原因，不改原品质、不删除映射或制造缺失；root path/type/generation 变化使旧 baseline 无效，须新代完整扫描重新确认。触发器不反锁 subscription/wanted，不释放占位，不发网络/文件 I/O。root/unit 业务锁为 NO KEY UPDATE，仍互斥属性写且兼容 source FK KEY SHARE；建轮次先 root/unit 再 wanted，避免 source 版本触发器与导入形成逆序。`wanted_projection_validity` 精确比较实体 ID 和计算使用的版本（相同数字不等于相同实体）。现有 owner 的 due 查询不受未来 next_evaluate_at 掩盖版本变化；HTTP POST/GET/list 共用 fresh 投影消费查询，版本不一致或到期时屏蔽旧 satisfied/stage，不删除缓存观测；后台至多下一调度周期重算，不承诺同步完成。投影源 TTL、未来 air_date、活动 round deadline 的最早边界限制时间有效性/next_evaluate_at，数据库 session 时区不改变 DATE 的 UTC 语义，故到期即使无更新事件也不能继续绿色。轮次冻结 unit/media metadata version，身份等同 title/year 的变化亦使最终收口拒旧候选；旧无新契约轮次 migration 中隔离。最终获取仍在窄 UoW 读当前事实，不以 fresh cache 授权。规则 revision 的不可变性依赖现有发布合同，完整策略命令/预览/回滚与 UI 仍未重新放行；此处不增加运行时 Hook/总线或全局 acquisition lane。

**评估合同失效切片（2026-10-11，schema44/45/46）**：数据版本相同不等于评估语义相同。`wanted_item` 另存 `evaluated_contract_version` 和每次写回的新 `evaluated_write_token`；schema44 原合同为 `ownership.quality.v1`，schema45 接受 `ownership.quality.v2`，让来源/编码 v1 旧语义投影失效；追加 schema46 后只接受 `ownership.quality.v3`，覆盖裸容器尾词的发布组负向拒绝新语义，不给原品质、组名、文件或占位补签。后续语义变化也须同步追加迁移更新有效性视图。schema44 不回填历史合同、不清除旧结果、原始品质、文件/映射或物理保护。视图只接受精确合同及非空写回凭证，缺少或不支持版本的投影立即屏蔽 satisfied/stage，并在未来 next_evaluate_at 之前唤醒现有重算。新版窄 UoW 写回生成独立 UUID；旧程序更新 is_satisfied/current_fact_stage/last_evaluated_at 即使值未变，也因没有新凭证而被触发器清除合同，不能借新版留下的戳继续写绿色。触发器只修改同一 wanted 缓存行，不重新锁源表或更改事实。事务回滚保留原合同；新证据仍须独立有效事实，重算不提升历史未知品质。尚无任何写回凭证时才可撤回迁移；已有评估写回凭证（即使合同后来失效）均拒绝 schema45/46 降级，避免复活被替代的旧评估，保留历史证据。此戳仍不是最终获取或文件写入权限。

<a id="search"></a>
## 5. 搜索与候选决策

### 5.1 三层持久模型

自动订阅执行详情按原订阅对象权限，以只读一致快照读取近期round、来源任务、查询页、分页回执及候选持久摘要，不创建搜索或执行任务。成员仅本人，管理员可跨对象；read Token仍受当前账号和对象限制。没有持久拒绝证据的候选明确标为原因未保存，不以当前策略倒签历史原因；等待和下一步由持久状态推导，成功空结果与来源失败分开。返回省略下载链接、查询payload和原始路径；前端失败/换账号清除旧详情。

```text
SearchRound：订阅的一轮决策（版本/目标快照、deadline）
       │ 多对多 QueryDemand
QueryExecution：参数与配置版本相同的共享网络执行
       └─ QueryPage：按适配器页码/offset 持久续传
```

round 保存 subscription/version、质量与过滤 revision、目标快照、deadline、状态；交互搜索可无订阅，只返回候选。execution 保存 indexer、规范化参数摘要 query_hash、config_revision、执行 ID 与状态；page 唯一 `(execution_id, page)`。

- 仅活动 queued/running 执行去重；完成后同名查询可创建新执行，不依赖删除历史行。
- 完整成功结果可短缓存，默认 2 分钟；强制刷新不复用终态缓存。配置版本不同不合并。
- INSERT ON CONFLICT DO NOTHING 后，用下一条语句查询共享执行；若已结束则按缓存规则复用或重试创建。不要认为同一 snapshot 的 CTE 兜底查询能看到刚提交的竞争行。
- RSS、交互与自动补缺共用端点预算：每端点 1 个在途，最小间隔默认 2s；限流遵守 retry_after。

**当前运行切片（2026-10-07，非 M2 全阶段放行）**：`00019_search_runtime.sql` 与 schema 19 持久化轮次/端点 job、query-demand/候选关系、基于 app_owner 的 job 恢复及端点间隔/429 退避。`App.Run` 显式启动并取消/join 自动补缺 owner：每 3s 评估最多 20 个到期 wanted，4 个端点 consumer 与独立 finalize consumer，网络调用受 round deadline 和 35s 上限约束。端点真实 I/O 持有专用 PG session advisory permit，逻辑租约过期不释放物理独占；仅确认 permit 空闲才 retire 孤儿 running query，端点配置变化不接受旧结果。自动模式只处理显式绑定索引器/下载器、根 online 且 completed baseline 的单电影 `acquire`：稳定单元或标题/年份证据，冲突外部 ID、剧集/包/模糊结果拒绝自动映射；只评估关联到本轮 query 的候选，最终以 rank 并列稳定 ID 择优，提交时复核 cutoff 和全部版本。`00020_rss_runtime.sql` / 当前 schema 20 增加默认关闭的 RSS 配置和持久 `rss_poll`：管理员 GET/PUT `/api/v1/indexers/{id}/rss`，PUT 必需幂等键与 expected_version；Feed URL 加密，成功回执仅含配置版本/游标/时间/候选数。`ports.RSSRepository` 的 PostgreSQL 窄 UoW 同事务提交配置/幂等结果/成功审计/事件，或回执/轮次额外证据/成功审计/事件/job 终态；RSS 用例只依赖 ports，SecretProtector 和 store 仅由 app 显式组装。每 3s 最多排 20 个到期来源，沿既有 4 个网络 consumer 与每端点 PG session permit/2s 预算执行，复用 Retry-After 与持久 ±10% 来源抖动；job 身份关联成功 query，回执失败后的重启先观察完整查询，不重复网络请求。配置变化立即 fence 旧 RSS job；租约过期不释放仍执行的物理独占，旧 owner/token 不能发布回执。RSS 通过独立 round↔query 关系补充候选，不覆盖定向检索，也不另建获取入口。最新完整回执默认缓存 2 分钟，`wanted.rss_seen_at` 与 source 成功时间保留未消费唤醒，不依赖可被覆盖的 next_evaluate_at；新轮次按 UUID 顺序 SHARE 锁来源，等待在写回执提交后再冻结配置/读取游标，同轮次事务消费水位，仍活动旧轮次不消费后到信号。回执关联已锁轮次使用 SKIP LOCKED，避免来源/轮次锁倒置；略过关联仍保留持久唤醒。普通 RSS 读取精确 URL、严格 RSS 根和响应上限，禁止重定向，缺失大小/做种不虚构；含 URL/passkey 的 GUID 以摘要去重，下载引用继续加密。旧内存 PollRSS 入口仍禁用；这不是剧集/升级或完整 RSS UI 放行。

`FinalizeLeasedRound` 通过 acquisition 窄 UoW 同事务锁定策略→订阅/轮次/job/端点配置→root→unit→acquisition，重新检查 owner/token/state/实际租约、active/意图与策略版本、根 marker/baseline、当前 imported 和占位；轮次终态、轮次/操作候选关系、需求/占位/下载任务/持久 job/事件/成功审计/job 成功统一提交。HTTP POST 订阅只保存意图，`indexer_ids` 明确绑定（空数组禁用，省略保留重订阅已有绑定）；`downloader_id`、范围/seasons/specials、wanted 与意图版本在 subscription UoW 同事务创建/重配置，GET/POST 返回配置 UUID、不返回秘密。2026-10-08 已接入订阅创建显式端点选择、详情独立绑定编辑与站点 RSS 配置。PUT `/api/v1/subscriptions/{id}/bindings` 由 BindingService/ports 窄 UoW 校验对象权限、expected_version 和幂等指纹，仅改变端点与意图版本，不改 active/paused/archived、季集/跳过、占位；绑定/wanted version/成功审计/事件/幂等结果同事务。响应丢失重试复用命令键；409 必须读取最新版本后再编辑，已确认保存但刷新失败仅重载。RSS Feed URL 仅在瞬时密码字段输入，保存成功清空，不回显、不持久缓存；普通 RSS 启用保存要求重新输入完整 URL，空地址保存清除覆盖。订阅 GET/list 返回真实媒体类型、标题、根名、unit_key 与绑定；读失败不伪装空成功，前端保存/刷新保留海报与选中季。UI 对剧集、模糊、多单元明确尚未自动获取，不把空筛选、绑定或配置启用说成检索/入库完成。端点名址修改的空凭据/categories 在 SQL UPDATE 原子保留已有值；旧协议自动推断/健康标记不作为真实联调证据。完整策略/候选/等待原因与健康管理 UI、剧集/多单元自动匹配和 upgrade 调度仍未放行；不以本地 metadata/海报 fixture 冒充授权互联网联调。
**无合格候选退避与即时投影重算（2026-10-09）**：轮次终态造成输入版本失效仍立即重算拥有/等待，但不等于新网络搜索许可。对于 `no_qualified_candidate` 保留已有 `next_evaluate_at`，重算不向后滑动；未到期且无未消费的、启用在线原绑定RSS回执时，scheduler不建新轮次。freshRSS可提前唤醒且建轮次内锁来源/消费水位的原子合同保持；已到期的退避可以继续搜索。真实App.Run反例先证投影失效每6秒绕5min边界，修后跨两个tick仍仅一Query/同deadline；显式把独立测试库retry推进到due，第二轮正常执行但矛盾Year仍无获取。正常RSS迟到回执→新轮次→唯一下载任务回归通过。

- Prowlarr 聚合端点不是一个真实站点；上游负责实际站点预算。一个慢端点不能阻塞其它端点或无限延长轮次。

### 5.2 候选与评估

release 按 `(indexer_id, guid, source_config_version)` 去重；原始结果、解析结果、候选版本、parser_version 与匹配证据落库，下载引用加密。匹配外部 ID 优先，标题/年份为后备；低置信或覆盖不明确不自动获取。

**候选解释实际切片（2026-10-08，schema27）**：GET subscriptions/{id}/wanted/{wanted_id}/candidates 先对象授权，再 READ ONLY REPEATABLE READ 读取当前意图、策略、媒体/单元版本、库/事实/占位/端点及最近持久轮次候选；不联网/触盘、不返回 GUID/URL/cipher，敏感标题隐藏。纯 ExplainMovieCandidate 与最终自动获取共规则，accept/reject/hold 区分已知拒绝和未知证据；允许 0 分补缺不要求 upgrade cutoff。candidate_rule_cache 只保存纯结果，键包括解析/决策实现版本、候选/源配置、原品质、目标实体/元数据/范围/不可变策略与规则；实时可获取性不缓存。source/deadline/意图/策略不匹配、根或占位门禁均禁止将 cached accept 当下载许可；实际提交独立再读。100 上限 explicit limited，空表示无轮次关联证据不是站点没资源；存储失败503/no-store且 UI清旧accept。
评估输出 accept/reject/hold、rank/score 与原因。纯规则结果缓存键覆盖候选、解析、策略与范围版本；当前可获取性读取实时事实、拒绝列表、端点健康与活动占位。

**端点健康实际切片（2026-10-08，schema23）**：`ports.IndexerHealthRepository` + `HealthService` 持久排 `indexer_health` readonly probe，只对已绑定 active 订阅或启用 RSS 的配置端点工作；同四网络 consumer、同物理 permit/持久 next_query_at/429预算与 search/RSS/manual，共 owner/取消/join。periodic next time 可重建、live job 去重，失租约换新 token 但不释放旧物理调用；网络后写回重新校验 job 的 owner/token/state/有效租约、payload、config version、真实终态 query。成功空响应可 online，失败持久指数退避/确定性抖动，429 显示限流/预算等待而不判 offline；不保存远端原错或 body。记录 checked 与 query 完成的 observed 时间、query ID、失败次数/下次检测，延迟旧回执不得覆盖同配置的更新观察。配置变化清旧 observation；健康/audit/event/job成功同交易。手动测试也共预算、收到持久回执才宣称成功，不能清 RetryAfter。公开 list 经窄 repo 读取，读取失败 503，不虚构空列表；UI 无 checked 显示尚未确认，成功也说明最近观测而非永久在线，503 清旧 online/latency。实际 App.Run 两个真 HTTP 端点故障→恢复/慢端点不阻其他 worker 有 PG/race 测试；仍不是授权互联网/NAS/下载器联调或配置协议类型全部准确的证据。

整季 require_pack 必须明确覆盖所选季的全部目标；纯完整性规则必须以已知全季单元集合与已知覆盖集合精确比较，拒绝缺号、重复、额外集、未知文件及未确认的全季元数据。首尾集号、TotalSeasonEps 或标题 COMPLETE 只是摘要，不得当完整证据；特别篇不借普通季包门禁放行。prefer_pack/prefer_episodes 只影响偏好，不授予获取或文件执行权限。覆盖按已确认单元，不宣称能排除所有未识别包的物理重叠。

**包/单集选择预览实际切片**：`quality.ChooseTVSelection` 由 `acquisition.PreviewTVSelection` 消费。模式和缺失比例门槛必须显式提供并绑定独立策略输入摘要，不从已有不可变 `PolicyRules` JSON 推断或补默认值；`none` 无格式偏好，`prefer_pack` 在比例达到门槛时偏好合格完整包，`prefer_episodes` 偏好覆盖全部待补目标的合格单集方案，`require_pack` 无合格完整包时等待且不回退。候选先经当前质量规则校验；单集逐目标独立择优，方案间比较最弱单集质量与整包质量，不按集数累加，平分按稳定候选 ID 决定。用例验证所提供当前账号/对象范围、意图/策略/配置/范围版本、完整季证明与全部 canonical/Wanted 的 UUID/版本/提供者/编号集合，以及每个候选独立重算事实和历史复核的身份/输入摘要；特别季、未知或不完整覆盖不供完整包选择。当前完整集合获取要求全季单元均已选、缺失、已播出且无保护，预览保持此边界，不能以较低比例门槛获取已满足或受保护单元；缺失目标中的未播出/受保护项不能通过省略而制造完整单集方案。输出恒为 `preview_not_executable`、`ExecutionAllowed=false`，无命令、占位、job、执行封印或文件副作用。生产观察由 `postgres.TVSelectionStore` 的账号优先 repeatable-read 窄 UoW 组装：当前账号/对象范围、完整季证明、canonical/Wanted 及投影合同/事实版本、来源/映射/根/下载器、候选与所属复核均来自同一快照；全部 Wanted 必须当前，读取不重算投影或创建命令。`acquisition.TVSelectionService` 仅接收订阅/季、候选与复核 ID、显式包偏好及可选预期版本，再调用纯预览，禁止把事实布尔值、质量分数或摘要当作公开证据。来源期限先于逐候选读取捕获：整包材料绑定全部来源证明，取最早截止；单集只要求存在有效观察，取最晚截止；完成全部可能阻塞的证据读取后，再以同一数据库时间核验全部投影、映射及来源期限。显式调用方取消保留 `context.Canceled`，数据库锁/超时/序列化故障归为不可用。正式 App 通过 `POST /api/v1/subscriptions/{id}/tv-selection-preview` 暴露只读计算：Cookie/同源 CSRF、session-only 管理员与数据库账号/对象范围复验，严格验证原始 JSON 的全部必填字段、嵌套未知/重复/大小写别名字段、数组总数（最多 100 项）、多值请求头和尾随内容；输入只含季、候选/审核 ID、显式 PackPolicy 与可选预期版本，不接收来源事实、质量分或执行权限。响应 `private, no-store`，返回当前版本用于下次比对，预期版本中的账号/订阅取当前会话/路径；失败返回 RFC 9457 的 400/401/403/409/503 安全问题响应。预览不创建审计、回执、任务或占位；正式 App 的后台调度写入独立于该计算。暂无自动执行调用，后续实际获取仍走各自窄 UoW，不使用预览作为授权。

**包/单集只读比较界面实际切片**：既有资源审阅入口收集当前账号、订阅与来源签名绑定的独立整包和单集审核引用；完整包须读取当前完整覆盖及目标状态后方可加入备选。用户显式选择季、缺失比例门槛、四种偏好及要比较的审核备选，再调用正式 `tv-selection-preview`；界面不接受客户端补齐的事实、分数或执行许可，只接收 `preview_not_executable / execution_allowed=false`。同一输入的再次读取携带服务端版本；账号、订阅/来源签名、候选、审核引用、参数或读取失败都会清除旧结果，取消及代次/请求签名围栏阻止晚到响应恢复旧比较。显示当前选择、全部目标和排除原因，比较结果不接执行按钮；实际获取继续独立审阅/确认和原键恢复合同。原因字段须为字符串，文案仅查自有属性并安全回退；比较面板及相邻单集/完整覆盖审核不以原型键作为渲染内容。真实 Chrome/Cookie/PostgreSQL/App 与原生 qBittorrent HTTP 夹具已核验四模式、版本重读、撤权恢复、移动布局及比较零下载业务副作用；后续独立获取仍单次取种/同字节 Add，不构成外部下载器/NAS或整包整理验收。

**完整季元数据实际切片（schema57）**：成功的完整普通季刷新在既有窄 UoW 中写入不可变 `tv_season_metadata_snapshot`，按媒体/媒体元数据版本/季保留历史，冻结每个明确单元的 UUID、单元版本、TMDB 单集 ID 和集号，并记录提供者剧集 ID 与规范化内容摘要。证明与元数据、Wanted、审计、事件及幂等回执同事务发布；旧行、特别季、空或不完整响应不补签证明。`EpisodeSourceContext.CompleteSeasons` 只读取当前媒体版本且来源、全部活动 canonical 单元集合、单元版本、编号键及提供者身份精确匹配的证明；缺失、重复、额外或坐标列缺失/错配但仍带该季 canonical key 的单元使证明失效，不补齐非连续集合。无证明字段用 `omitempty` 保持旧来源输入戳兼容；有证明时将其纳入来源输入戳。此证明只证明元数据来自完整季响应；候选真实文件覆盖、整包复核/获取与提交前扩展必须走后续独立合同。

**完整覆盖候选复核实际切片（schema58）**：普通季候选使用独立 `TVCoverageCandidateReviewService` 与窄仓储，在当前完整季证明、全部活动 canonical 单元、解析出的明确标准季集集合和所选目标集合精确一致时才允许确认；首尾摘要、特别季、整包标题、未绑定编号和缺失元数据不替代集合证据。物化封印绑定候选原始材料摘要与大小、来源配置/有效观察、提供者身份、映射、元数据及单元版本、策略、意图、根和已选下载器的配置摘要；文件/拥有/占位等可变事实组成单独观察封印。公开预览和确认使用规范 OpenAPI 的 `/subscriptions/{id}/tv-coverage-candidate-reviews[/preview]`，通过现有认证、同源 Cookie 检查及 RFC 9457 错误合同；确认要求原观察封印和幂等键。事务先锁并验证当前数据库用户与对象范围，再按既有顺序锁版本边界，提交前重新计算来源和完整集合，复核、审计、事件及回执一并提交。成功记录不可变且状态恒为 `reviewed_not_executable`；不创建操作、job、下载任务、lane 或占位。当前账号及对象范围仍有效时，同键可读回原历史回执，材料或事实后续变化不重写历史；新确认必须匹配当前观察，后续获取必须重新验证全部材料与事实，不能把历史回执当现时下载授权。现有单集复核封印及单单元提交守卫保持各自合同；全集合获取使用 schema59 的独立事务；metainfo 提交验证仍由后续专用路径实现。

**完整覆盖获取事务实际切片（schema59）**：`CompleteTVAcquisitionStore` 使用独立命令和 `complete_tv_execution_authority`，先验证当前数据库账号及对象范围，再锁策略、订阅、根、按 UUID 排序的完整单元集合及来源边界；历史 review 回执不能替代当前材料与事实校验。确认范围内必须全部仍选中、已播、未满足且无冲突；全部默认 lane、根级 reservation、需求 fence、操作、专用 job、不可变执行快照、事件、成功审计及幂等回执同事务提交。事务尾部仅消除自身本次 lane/reservation 引起的两次事实版本变化后重算完整材料、事实和来源有效性，任何外部变化或失效整体回滚。同键恢复原获取回执仍须通过当前账号与范围校验；新键复用只接受完整同一单元集合与执行材料，不能补写部分集合或释放 unknown/在途保护。结果为 `reserved_not_executable`，尚无公开获取路由、App/worker 注册或下载器 Add；专用提交必须进一步核验完整 metainfo、全部需求/fence/占位与冻结覆盖精确一致，并遵守物理独占、租约写回及 R3 观察合同。

**完整覆盖 metainfo 预检实际切片**：`torrent.Inspector` 只接受限长、限深、限节点且字典键严格有序的完整 v1 bencode，保留独立的原始 torrent 字节副本及 SHA-256，并直接对原始 info 区间计算 v1 info hash；不重编码、不以 URL 再取回替代已检查字节。v2/hybrid、符号链接、非普通文件属性、编码/路径别名、路径逃逸及文件/目录冲突、异常整数/重复键、文件数或 piece 数不一致等结果要求人工复核。domain 的 `ValidateCompleteTVTorrentFiles` 在完整活动 canonical 季、冻结 UUID/单元版本/提供者 ID 精确一致的前提下，仅接受每个视频明确对应一个标准单集且整体集合相等；允许有限的明确字幕/说明/海报附件，不从附件或范围首尾推导覆盖。缺失/新增/重复视频、归档/未知文件、特别集/绝对编号/多集视频或父目录明确范围与子文件冲突均不能取得覆盖；集合、跨季或归档拒绝证据不因解析器返回 movie 类型而被跳过。该预检已接入下述专用同步发送端；真实 PG 验证现有通用提交的 job-kind 白名单拒绝完整覆盖 job，即使其有有效租约也不创建下载任务或释放占位。专用提交在锁内核对整个 job/需求/fence/lane/占位与冻结授权集合，再发送同一份已检查字节，并保持物理独占与 R3 观察协议。

**完整覆盖提交准备事务实际切片（schema60；schema61扩展证据版本）**：`InspectCompleteTVMetainfo` 将经过原始字节与完整文件覆盖核验的结果封装为不可由外部构造有效内容的字节证据，访问原始字节时返回副本；`CompleteTVSubmissionStore.PrepareCompleteTVSubmission` 只接受该证据及匹配的专用 job 租约。事务先锁当前冻结账号，再锁策略、订阅、根、按 UUID 排序的完整 canonical 单元、操作、来源与 job；核验执行授权、当前材料/配置/来源、选中且已播的完整目标、全部有效需求及 fence、默认 lane 和根级占位均与冻结集合相等。全部保护行锁取得后重新读取数据库时间，不能用等待前时间接受已过期占位；尾部重算当前来源与事实，仅扣除获取及本次准备自身已知的事实版本变化，并再次核验有效 owner/token/state/租约。新建 submitting 下载任务、全部 task-unit 关联、lane/task 绑定、dispatched 占位、不可变准备记录、成功审计及事件原子提交；记录绑定原始 torrent SHA-256、info hash、覆盖与原发送租约，原始字节和下载链接不落入该记录。重复调用只能在原租约及全套当前保护仍有效、任务仍 submitting、字节及任务身份完全一致时返回原记录；新租约或 unknown/终态不借历史准备再次获取发送资格。未知操作/明确账号失效/租约或证据冲突进入待复核并保留保护；数据库故障及锁超时保持 unavailable，取消保持调用上下文错误。准备结果恒为 `prepared_not_sent`；专用同步发送端只消费新准备结果，重复准备回执不能触发 Add。公开获取、App/worker 和 R3 观察尚未注册，准备状态不解释为已向下载器发送。

**完整覆盖物理结果写回事务实际切片（schema61）**：`RecordCompleteTVSubmissionResult` 只接收原发送 job 的 owner/token、原 task/op 与相同的封闭 metainfo 证据；锁内验证专用 job 仍 running 且租约有效，原执行授权与不可变准备记录、客户端/候选/info hash、完整单元集合及 task-unit、全部需求/fence/lane/根级占位完全一致。`complete_tv_prepared_v2` 在原始字节摘要、文件覆盖及租约外冻结原保存路径，回调核验任务路径未改变；HTTP 期间当前根配置、账号、策略或来源变化不否认已经发生的原始物理结果。schema61 追加允许 v1/v2 记录，要求 v2 路径是非空字符串；历史 v1 不改写且不能通过准备重放补签，留有 v2 记录时降级被拒绝。确认成功写 `tracking`，Add 失败或结果不明写 `unknown`，任务、job、审计与事件同事务提交；最终 job 写回再次校验有效租约并要求 `RETURNING`，尾部故障全部回滚。旧 token、过期回调和不匹配证据不释放任何保护，供后续 R3 观察；数据库故障保持 unavailable，原始外部错误不进入事件/审计。该结果合同已连接下述专用同步发送端，已注册下述受控 worker，公开获取已接入下述正式路由；准备结果仍不代表发送已执行，实际调用持有物理独占直到 HTTP 返回并发送同一份已检查字节。

**完整覆盖同步同字节发送实际切片**：`Manager.SendCompleteTV` 由组装层显式注入原生取种、metainfo检查、准备和结果合同，调用时在管理器锁内捕获服务与客户端工厂；使用当前数据库客户端配置构造实例，不借缓存旧凭据发送。持有共享客户端预算和按operation计算的跨实例PG session advisory lock直到实际HTTP与结果写回返回；仅新的准备结果可发送，已有task/proof或AlreadyExisted均要求观察。准备后按不可变v2重新取原task保存路径，锁连接丢失或任务身份变化不发送。原生Torznab单次取回限长字节，检查所得封闭证据的同一字节副本交给显式qBittorrent能力，DownloadURL为空，不重新取种或改编码；只有HTTP200且明确Ok.视为确认，错误/歧义写unknown并保留保护。严格Add清除自动GetBody，阻止Go HTTP/2在已收到完整POST后因PROTOCOL_ERROR等stream错误重建请求体盲重放；真实TLS/H2负控及修复验证为两次Add变为一次并返回Unavailable。取消或逻辑租约到期不提前释放仍在执行的物理预算，迟到结果按原租约拒绝且不释放保护。已连接下述生产 App/worker 与独立 R3 观察；公开获取已接入下述正式路由，整包整理仍待完成；不会以准备、tracking或下载完成冒充入库。

**完整覆盖独立 R3 观察事务**：`LoadCompleteTVObservation` 使用与原发送不同的专用观察 job/owner/token，在短事务内锁原账号（含停用账号）、冻结 UUID 单元、操作、任务、观察 job、不可变准备与全部保护、客户端；不以当前命令权限、来源或选择变化否认原始物理事实。原始 v2 字节摘要、文件/单位关联、原 task/op/hash/path、客户端配置、完整需求/fence/lane/根级占位及全部历史行必须与冻结集合精确一致，获取所有行锁后才核验当前观察租约。加载提交后才允许调用原生网络；调用方须保留发送端相同物理锁和共享客户端预算到观察和写回结束。`CommitCompleteTVObservation` 再次加载并逐字段比对完整快照封印，只有完整且唯一的 hash/op-tag/原路径/大小匹配可恢复 tracking；完整无结果、歧义或身份冲突写 unknown 并要求人工复核，不完整/离线/数据库故障返回 unavailable 且不制造不存在证据。状态、观察 job、审计与事件原子提交，尾部再次核对全部保护和任务状态/错误、最终 job 的 RETURNING 与有效租约。既不释放保护，也不创建 Add 或整理权限。通用 SubmitDownload、ReconcileTask 与 ObserveTask 在旧回执或状态分支之前检查任一完整覆盖执行授权、准备记录或 task-unit 证据，拒绝绕过专用字节发送及观察合同。`Manager.ObserveCompleteTV` 显式接收观察仓储，复用发送端相同的 operation PG session advisory lock 与管理器客户端预算，持有到实际原生读取及最终写回结束；锁内捕获工厂并从当前数据库配置构造客户端，不使用缓存旧凭据。专用 qBittorrent 能力执行限长、完整的操作 tag 查询，逐项要求合法身份/大小/状态和存在的 tag 字段，保留重复/多结果供复核，null/截断/缺字段或原生失败不作缺失证据；tag 按完整逗号分隔 token 核验而非前缀。schema62 的 `complete_tv_physical_permit` 将 operation、独占 token、原锁 backend 与 advisory key 持久绑定；发送与观察获得同一许可后才可加载/执行，不以超时或会话消失重授许可。同步 owner 只在实际 I/O 和写回返回后按 token 删除自己的许可；清理失败或进程终止则保留，须人工确认执行已结束，不盲重放，数据库降级在判空前取得 ACCESS EXCLUSIVE 并持有到删除结束，等待已获插入锁的事务提交后重新检查，不得丢弃并发或保留的许可。实际配置摘要必须匹配快照；I/O 前与提交前验证原锁连接存活，观察事务加载和尾部写回再次锁定、封印并核验许可 token 及原 advisory session，拒绝连接在提交期间丢失的结果。原生 JSON 对象中的重复身份字段（含大小写别名）必须拒绝，原生 context 取消保持取消分类，过期观察租约拒绝写回；忽略取消的网络读取仍占有物理许可，完整响应允许独立限时事实写回。该路径没有 Add、整理、释放或入库权限；已由下述受控 worker 与 R3 调度消费，公开获取已接入下述正式路由。

**完整覆盖专用跟踪／完成观察实际切片（schema62）**：`CompleteTVTrackingStore` 与显式同步 `Manager.ObserveCompleteTVTracking` 只消费原 tracking 任务和独立 `complete_tv_coverage_tracking` job。沿用发送／R3 的 operation 物理锁、持久许可和客户端预算，持有到同步原生读取与事实写回结束；按原配置构造客户端，当前账号变化不否定已发生的物理事实。完成须前后两次唯一且身份／路径／大小相符的精确完成观察、qBittorrent中间只读导出的原始 info hash 与 v2 准备字节证据一致；Transmission仅对新准备中已冻结original_files完整原清单的任务开放，使用独立只读清单端口逐项比对原私有检查证据，不伪造导出能力，缺少该历史证据时拒绝完成判定，以及完整文件清单逐项路径、大小和精确进度一致；允许的零字节附件也必须出现在清单，不因字节为零省略。完整无结果、身份或完整清单冲突要求 unknown 复核；离线、缺字段、截断、重复／歧义 JSON 或原生失败返回 unavailable，不制造完成证据。共享原生 JSON 准入在转义解码后拒绝非 ASCII 字段名，再按 ASCII 大小写去重，防 Go Unicode folding 将 `progre\u017fs` 覆盖 progress；Unicode 文件名等值仍允许通过既有后续校验。提交以新数据库时间验证有效 job owner/token/状态/租约、原 session/许可、不可变准备与完整冻结单元／文件绑定、全部活动及已释放的保护历史；sqlc 正式查询仅更新仍为 tracking 的原任务，task/job/成功审计/事件原子提交，尾部重查状态、保护与许可。部分观察保留 tracking，不连续投影部分进度；确认 completed 同时标记 organization_state=review_required，不创建整理权限、释放保护或入库事实。此观察合同无 Add、取消、重放或文件操作。App 已显式将专用 tracking 接入同一 manager、原生 Inspector 与受控 worker 的取消／续租／等待生命周期。独立窄调度事务以事务级 try-lock、每批最多25个 task 的 SKIP LOCKED 和活 job 部分唯一约束去重；先锁 running job，再以当前数据库时间将过期 tracking job 作 dead 并推进 token。首次、成功部分观察后及过期换代均按持久时间戳等待至少30秒；单个 job 错误最多3次尝试，连续租约过期换代最多3代，普通错误耗尽不得由调度器重新复活，成功部分观察后重新计算后续过期换代次数。保留物理 permit 时不排队、不重授独占，completed 不再调度，unknown 只进入独立 R3 观察；实际调用与写回结束前不释放许可，App 关闭等待专用 worker 排空。周期跟踪、重启恢复及兼容回归已在真实 PG／原生 HTTP 验证；完成仍只进入整理复核，schema63 的显式确认与整理合同见下文，外部下载器／NAS尚未验收。

**完整覆盖受控执行与 R3 调度实际切片**：App 显式组装同一下载仓储、原生取种/检查、准备/结果、独立观察与恢复合同，专用 worker 仅领取 `complete_tv_coverage_download_submission` / `complete_tv_coverage_observation`，由 App.Run 取消并等待排空；每次执行持有真实 owner/token，90 秒租约每 30 秒续租，续租失败取消逻辑执行但等待同步网络及事实写回返回、续租协程退出后才结束 worker。成功业务结果由窄 UoW 同事务完成 job，worker 不补签业务成功；失败按原租约有限重试。恢复调度在 job 行锁后读取数据库时间，过期未准备发送可用新 token 重新领取；已有 task/不可变准备的发送 job 必须封锁为观察，不能再 Add。原生结果不明只生成独立观察 job，部分唯一索引和事务级调度锁去重；观察租约到期用新 job/token 恢复，最多三代，完整无结果/歧义的终态复核不自动重排。持久物理许可不因逻辑超时、进程或 session 消失回收，保留许可时不得安排新物理请求；原发送仍在执行则继续保留全套保护。App.Run 原生 HTTP/PG 验证涵盖单次 Add、丢回执后的只读恢复、原准备重启、双实例过期不重叠、旧 token 拒绝及关闭期间事实写回排空。公开获取已接入下述正式路由；包/单集只读比较已独立验收；周期跟踪及下述标准整包显式整理后端已独立验证；tracking 和下载完成本身不产生入库事实。

**完整覆盖公开获取实际切片**：规范 OpenAPI、Go/TS 生成合同与正式 App 注册 `POST /api/v1/subscriptions/{id}/complete-tv-acquisitions`。HTTP 仅验证严格请求并提交原持久命令，不执行取种或 Add；沿用 Cookie 同源 CSRF、session-only 管理员与对象范围检查，在事务锁定当前数据库账号并复核后才读取同键回执。拒绝重复请求头/JSON 字段、额外权限或范围字段及尾随 JSON。新结果为 queued，历史 reserved_not_executable 回执原样恢复，不改写历史执行语义。真实 HTTP → App.Run → 原生 Torznab/qBittorrent 检查单次同字节 Add 与丢回执后只读观察；媒体根须以真实 identity marker 和完整扫描通过健康检查，不能靠测试中仅写 online 绕过执行守卫。公开界面分别确认审阅和获取，按账号/订阅持久冻结原候选、完整季集 UUID/版本、材料封印、请求 key/body 与历史回执。未知结果遇到撤权或其他 4xx 仍保留原命令；恢复权限后只允许同键恢复。所有 journal 的读改写（含读取刷新后的回执修复）使用同一账号/订阅 Web Lock，防止另一标签覆盖待恢复命令。有效成功回执先在当前页面内存封闭确认，再尝试可能失败的持久化；存储修复与确认后的刷新不重发已确认命令。旧格式未知命令保留并阻止静默换键。当前真实 Chrome/Cookie/PG/App.Run 验证已覆盖完整季集展示、丢回执/撤权后原键恢复、跨标签竞争、确认后只读刷新、持久全套保护和单次同字节 Add；异步浏览器等待必须显式检查解析后的布尔值。周期跟踪及标准整包显式整理后端已独立验证；整包整理的公开确认HTTP/UI及真实外部下载器/NAS仍须独立验收。

**标准整包显式整理后端（schema63）**：只支持单一普通季、全部冻结标准单元和一视频一单元；命令只引用原 task/operation 与成功的专用 tracking job，不接受客户端文件清单、路径或自行声明的许可。窄事务读取成功 job 的完整回执、原 authority、v2 preparation 和逐视频 UUID/版本绑定；先锁定当前账号、策略与订阅对象归属，再读取缓存回执，合法用户可恢复原历史结果而不重签材料。新确认还锁定根、按 UUID 排序的全集合与操作，重验完整季、来源、客户端、质量、当前需求和保护。取得物理槽后、任何根身份或文件观察之前捕获数据库时间；marker、根 identity 和全部源视频 Inspect 在事务外完成，锁等待或慢观察不能刷新五秒证据。远程 save_path 须经明确路径映射与受控源根解析。完整清单含附件保存，附件留在源目录且不生成步骤。计划、不可变整包证据、全部视频步骤、任务绑定、download_organization job、审计、事件与同键回执原子发布；既有受控 worker 消费，未新增自动确认。缺行/版本/权限失效为业务拒绝，数据库锁超时或连接故障保持可重试分类，显式取消保留取消语义。

**整包逐视频发布与恢复**：每步以独立整包授权核验整个冻结集合，复用现有物理根锁、源/目标 identity 校验和 hardlink 执行内核；只归一化本计划已完成步骤必然造成的事实版本、根水位与需求/占位消费，不重签外部变化。已链接但尚未发布的原 inode 可在重启后观察恢复；扫描登记但未关联的相同 inode 可复用。冲突、源/root 替换、外部版本或完整集合变化转人工并保留保护。最终收尾按同一账号→策略→订阅→根→单元→操作锁序，在释放前后及 job 成功写入后重验全计划、有效事实、唯一文件关联、冻结基线、持久终态、结果和原租约；任一尾部副作用使整次收尾回滚。仅所有步骤完成才通过专用 sqlc 查询释放原全集合 lane；查询只改 released_at，保留冻结 updated_at，终态比较仅扣除每单元自身释放必然产生的一次事实版本增长，其他字段和额外变化继续严格比对。schema63 冻结触发器不改变普通计划删除；Down 在独占证据表锁后判空，有历史或并发发布时拒绝丢弃。标准整包后台确认、真实硬链接、App中断恢复及63→62→63已验证；公开确认HTTP已独立接入并验证；跨季、绝对号、多集视频和真实外部下载器/NAS仍不在已验收范围。

**标准整包公开显式确认**：`POST /api/v1/download-tasks/{id}/complete-tv-organizations` 仅接受原 `operation_id`、成功 `tracking_job_id` 与 `Idempotency-Key`；不接受文件、路径、摘要或自行声明的许可。当前用户须为有效管理员并具有原任务与当前订阅对象范围，Token 在读取 body 或回执前拒绝，Cookie 写入遵守同源/CSRF。严格 JSON 拒绝未知、重复及嵌套伪造字段；仓储在事务外读取持久追踪回执解析可信摘要，再复用已有确认事务和受控 worker。相同账号/键且意图一致时返回原冻结历史回执，后续真实完成或材料变化不改写原 queued；新键仍按当前证据校验。202 仅表示冻结计划已排队，不证明入库完成。响应 private, no-store；业务冲突与安全后端错误沿既有 RFC 9457 合同返回，显式取消保留语义。当前Main真实Cookie/PG及正式App启动、双视频hardlink/imported、重启字节一致原回执已验证；公开材料发现及人工勾选确认界面已接入Activity，实际能力与恢复边界见下一段。

**整包材料发现与界面恢复**：`GET /api/v1/operations/{id}/complete-tv-organizations` 只允许当前会话管理员读取本人当前订阅对象的可信完成材料；同一 RR 快照先核验当前账号及对象范围，读取至多一个已完成任务的全体视频与附件数量。仅暴露精确季集、UUID、版本和源/目标相对路径，不做文件或网络观察，不写业务行或生成 job；`can_confirm` 只描述持久证据，POST 仍独立观察文件及核验五秒窗口。Activity 明确展示原 queued 回执和当前完成事实，须人工勾选才提交原 POST。成功响应须通过运行时结构、非零 UUID、操作绑定、严格布尔、标准季集、大小、相对路径与重复项检查；无效响应清除旧预览与新确认许可，不创建 journal，也不删除已有未知命令。发送前持久保存账号/操作范围的原 task、tracking job 和幂等键，提交及回执修复共用跨标签 Web Lock；后续4xx不覆盖未知命令或替换键。有效回执先在内存封闭，再尝试持久化，存储失败后只读修复，不重发。SSE 在原 POST 等待期间推迟刷新，账号/操作切换及忽略取消的迟到响应均受代次围栏约束。Main真实Cookie/Chrome/App/PG已核验双视频hardlink/imported、未知后403及原键恢复、存储修复、跨标签排他和移动布局；仍不声称生产下载器/NAS联调或整阶段验收。

### 5.3 决策提交

轮次全部执行终态或到达持久 deadline 后择优。事务内重新校验 active、订阅/策略/端点配置版本、目标根健康、当前拥有和排他占位；暂停、编辑或发布策略使旧快照失效。无合格候选则等待下一轮，不无限等待失败端点。

暂停在线性化点前阻止新决策；已发送的外部动作不能承诺撤回。RSS 到达可提前促成新决策，但仍经过同一事务入口。

<a id="acquisition"></a>
## 6. 下载占位与共享获取

**版本化控制实际切片（2026-10-08）**：PUT subscriptions/{id}/control 以 expected_version/required Idempotency-Key 改 state 或单元 skip；policy→subscription→root/sorted unit→wanted 锁内重读，意图/wanted版本、成功 audit/event/稳定 receipt 同事务。权限先于重放；相同命令只执行一次，state 同值不推进版本，403/409 后重读，结果未确认同 key retry、已确认且刷新失败只读恢复。归档保留订阅/Wanted/事实/需求历史，不承诺撤回已发送动作。旧无版本 pause/resume/DELETE 明确 deprecated 且409拒写，不能绕新事务覆盖新意图或删除历史。HTTP 不创建默认目录/marker，不用 UPDATE 伪造 baseline：省略目标仅可选唯一已配置同类型根，多根必须明确选择，扫描事实只能 scanner 写入。

### 6.1 物理获取与入库需求

一条 download_task 表示稳定操作 ID 的物理获取，不由单个 subscription_id 决定所有权。download_requirement 关联订阅目标/手动命令、单元、目标根与处理状态；多需求可共享一个下载。

**发送前需求与释放实际切片（2026-10-08，schema24 前置；schema25/26 见下段）**：需求原位追加完整 submission_fence（root/unit/media/actor/candidate 实体、不可变 policy ID/revision、自动 subscription/intent、root generation、unit/media metadata、material SourceStamp），不为 NULL legacy 伪造授权。sourceStamp 冻结标题/infohash/加密引用/原始品质，不含明文链接，不随做种数变动；自动需求不能把旧轮次 metadata 戳更新成新授权。唯一生产 Add 入口显式带 JobLease，app 注入 `ports.DownloadSubmissionRepository`；短事务按 policy→subscription→sorted roots/units/media→acquisition advisory→job 写权锁后重读需求，验证 owner/token/state/有效租约、真实 payload、当前控制/skipped/范围/根/日期/策略/原始品质、有效拥有及占位、源完整戳与 client/save-path。至少一个当前合格需求才提交 submitting + 准备成功审计，网络/解密前后的准备数据不在事务内做 I/O；最终 Add 在事务外，此准备提交是发送阶段线性化点，不能承诺取消已开始的动作。有既存 submitting/unknown 等只观察同操作，不换资源盲重发。NULL/源/根/元数据等无法确定时保持保护；若全部具有完整控制证据的需求因暂停/跳过/意图改变取消，且获取锁内证明尚无 task，则同交易标 released（保留历史）、取消未发送 job、解除该已证明未发保护、审计/event。`ReleaseRequirement` 改为窄仓储，ActorType+ID 必须拥有需求，锁内计剩余；任何 task 存在即不以 cancelled/completed/TTL 代替未发证明，unknown/in-flight 保持保护。legacy Attach 仅兼容已保护同根的有效 manual 需求，不借自动订阅授权或开放跨根；新主体有自己的 fence。schema24 Down 有 released 历史时安全拒降级，不能将历史恢复为 pending。此段记录 schema24 前置；当前跨根/全局 lane 协议见下段，完整阶段验收仍独立于局部通过。

**全局共享与源版本实际切片（2026-10-08，schema25/26）**：`00025_global_acquisition_lane.sql` 对 unit/default 建唯一活动 acquisition_reservation；root-bound unit_reservation 仅作目标保护，不单独授权另一次获取。lane 冻结源候选 SourceStamp、来源根/generation、实际 task；单电影兼容允许品质、当前 source、下载器及保存路径才附加原 operation，零新 submission job。manual/RSS/自动共最终窄 UoW，sorted root/unit 后同获取 advisory/行锁串行需求追加/完成/取消；组织结束也先 unit 再更新 task/lane。旧多 operation 同 unit 不推断等价，保留全部旧 root 保护并建立 legacy_conflict 隔离、旧未完成 submission job dead；有活动 lane 拒 Down。schema26 候选唯一键为 indexer/guid/source_config_version；旧 0 只读隔离，不当当前源，回执关联确切配置版本。已有其它根 imported 文件优先返回 LOCAL_REUSE_REQUIRED/等待后续复用计划，不以新下载代替复用。投影对其它根 lane 只显示保护/共享核验；SharedSource 短轮次冻结既有 source，经同一 leased finalizer 复核附加，零新 query。unknown/submitting/legacy/源失效/不兼容继续保护，不造本根 submitted/owned。公开自身需求 list/withdraw 仅 ActorType+ID 本人，管理员无隐式强撤；expected_fence 无秘密规范化摘要拒旧意图，key/digest/稳定结果与历史/audit/event/未发 job 取消/保护解除同事务。已有任何 task 不承诺撤回外部动作；整体 cancel 409，HTTP 无 SQL。GET library-roots 只提供认证的 ID/名称/type/观测健康、generation/基线，不暴露路径；发现创建显式选择同类型根，后端锁内重读类型/健康，不凭 UI 提权。RSS 消费水位取来源锁下实际已提交 ACK 时间，不用创建 wallclock；后到回执仍持久唤醒。
acquisition_reservation 保护 `(unit_id, acquisition_lane)`，指向物理获取，released_at 为 NULL 表示仍占用。V1 只有 default lane；不按质量 rank 自动拆 lane。当前获取可满足新需求时附加，不能满足则等待/提示冲突。

### 6.2 原子创建

**M3 覆盖提交前置闸补齐（2026-10-09）**：沿计划检查发现旧通用 `AcquireMovie` 虽名movie，却可接受TV unit/根、多unit包或未证明名称，先造获取占位再等worker处置；这是未验入口，不是可信mapping授权。现在公共/自动/private reserve共硬约束恰好一个标准`movie`单元、parent/root均movie、非退休、候选`RecognizeRelease`为非review单movie且无敏感引用；TV/包/绝对号/未知先返回`ErrUnsupportedCoverage`、公开409 `UNSUPPORTED_COVERAGE`，不创建lane、reservation、requirement/job/event/audit。原有未知全局保护/其它根复用等待协议保持，已有任何外部task不被此门禁解除。
旧 `/releases/download` 的无matched/explicit unit分支删除HTTP独立创建media/unit及忽略错误代码；现在400 `UNIT_REQUIRED`，明确未建媒体/获取。当前发现页unitless直接下载因此尚不可用，后续需显式根/可信电影目标选择与同一窄UoW持久创建，不能靠解析名称猜季或创建元数据掩盖失败。此路径未作新浏览器完成验收；已配置单电影manual根/单元仍支持202持久任务（非网络已送达），订阅自动movie真实入口需本轮全量复验。
实PG直接拒TV目标（即便只有1UUID）、TV/多集/absolute/unknown/敏感candidate绑movie目标且零新保护；HTTP无unit实际media/unit/reservation/job counts保持。旧`PackVsSingle`只模拟碰撞且不验证实际覆盖，改为UnsupportedPack保原单集占位与零partial，不将其算M3-03已放行；纯require_pack规则测试保留，未来独立完整coverage UoW另实现。新guard使旧`Fight.Club.1080p`未知year正例不再成功，正例补真实1999，未知原样另列负例，不放宽parsed RequiresReview。


1. 规范化并排序资源覆盖的全部已确认单元；不完整/低置信覆盖先确认。
2. 按统一顺序锁策略、订阅、根、单元，复核当前条件。
3. 创建或复用物理获取，写入目标入库需求。
4. 全部覆盖单元占位成功，再写 job、必要事件、审计与幂等结果。
5. 任一冲突整体回滚，不能拿到半个包；手动、RSS、自动搜索共用入口。

需求新增、完成与释放占位先取得同一获取行锁，重读剩余需求后提交；不能在事务外统计再释放。源 payload 实际覆盖与冻结结果不同且涉及新增单元时，提交前重新校验/扩展占位；无法可靠判断则人工复核。

### 6.3 外部状态与释放

**普通下载的原生暂停/恢复**：独立控制命令绑定原operation/task/client/hash/连接摘要和账号；确认共享影响需当前管理员显式授权，个人需求撤回或订阅暂停不等于暂停物理任务。命令许可、审计与原键回执同事务提交，原生调用在事务外；发送前及完成时复核当前身份、配置和授权。结果不明或物理返回尚未记录时，持久许可阻止新键替代发送。回执GET只读数据库，显式观察POST只查询原任务以确认状态，不再发送；取消、租约过期、连接丢失与迟到返回不能释放占位或冒充成功。qBittorrent按实际协议版本选启停方法；Transmission仅明确409拒绝允许协商，其他未知结果不重发。

rTorrent普通任务控制沿同一许可链，仅对精确原v1 hash发送一次`d.stop`或`d.start`，严格接受整数0回执；传输丢失、fault或畸形回执保留unknown。显式观察在同次有界`d.multicall2`读取完整身份、路径、操作标签、`d.state`和`d.is_active`；运行意图与活动状态一致且无hash校验才返回暂停/运行事实，排队、校验中和矛盾状态不能伪装暂停或恢复。观察不发送启停、重新加载或移除请求，专用TV/升级权限不因新增可选接口而扩大。

**既有专用任务暂停/恢复（schema135）**：自动TV单集、完整季包及单电影升级使用各自已确认下载的authority准入，不删除普通路径隔离。控制绑定原端点/hash、当前完整需求及权限/策略/文件证据，并保存不可变授权摘要。共享物理锁和持久hold阻挡发送、跟踪及替代控制；会话丢失后仍须原backend启动身份与可信返回才能释放，PID重用不构成返回证据。暂停不释放lane/reservation，不撤销需求，不等于取消或整理许可；未知仅允许显式只读观察。schema137的新TV洗版只经schema139的独立准入，不借普通或其它专用路径继承控制权限。

**标准单集洗版控制（schema139）**：已确认的原单集发送投影为唯一专用download_task，任务只用于展示与控制，不得进入普通发送/观察/整理。活动详情与单集工作区复用原生暂停、恢复、观察及持久回执，冻结TV authority和原端点/hash；只有当前有效tracking阶段、原需求/文件/根/策略及完整物理保护成立时可新发控制。控制在途围住原升级阶段，未知或无可信物理返回时阻止替代命令，扫描和派生投影水位不使原授权失效。回执与观察不发送启停；下载完成进入整理复核后不能再暂停/恢复，但原控制回执仍可读。

```text
pending → submitting → submitted → downloading → completed
                ├→ failed（明确拒绝）
                └→ unknown → 对账/同操作安全重试/人工复核
submitted / downloading → failed / removed（可靠确认）
```

worker 先持久记录 submitting，再 Add；外部确认后才写 submitted fact。下载 completed 只表示下载完成，不代表所有整理需求结束。

下载器可靠完成状态、完整字节数及稳定标识/保存路径核对后，在同一事务记录 completed、唯一 download_organization job 与成功审计。组织状态独立为 pending/queued/completed/review_required；operation 仅在有效 imported 事实满足全部需求后完成，不能从下载进度推断。当前电影自动组织限在线且完成基线的根、单个有效电影单元、完整普通视频文件或完整清单中唯一视频的目录来源与已配置来源路径；多视频、多单元、路径映射不明、源/根变化或文件能力不可用转人工并保留占位。共享单电影在同一获取锁内逐根冻结独立计划并保留历史；每根有效 imported 与该根需求 imported/占位 consumed 同提交。仍有 pending 时续排同一组织 job 并清当前计划指针，不重发下载；全部需求处理且无活动目标占位才释放 default lane。一根离线转人工并保留保护，已成功根事实不回滚。job 与冻结计划相关联，app 显式启动、取消并等待消费者；重启先观察冻结计划和硬链接后置条件，不重发下载。组织写回校验 job 和 plan 的 owner/token/state/有效租约；业务锁序为执行权→根→单元→获取。手动获取在相同根/单元锁下重查拥有及根版本，不能在导入消费占位后再次抓取。

电影目录来源只接受同一下载任务、客户端配置、真实 hash 与保存路径下的完整 Inventory：所有条目已完成且总字节精确相等，目录内恰有一个正大小视频。保存相对文件路径、尺寸和进度，不保存凭据或原始链接；客户端配置在完整清单请求前冻结，提交锁后复核。Schema55 将 Inventory 的 task/operation/client 绑定为复合外键，单电影目录不借用 episode_execution_authority；既有清单不回填或重签，有电影清单历史不能降级丢弃。下载完成、不可变清单、唯一组织任务、审计和事件同事务保存，下载任务 content_path 指向唯一视频，total_bytes 保留整个清单字节。冻结步骤分别保存视频尺寸、清单 task ID 与配置来源路径，在计划创建、执行和入库各围栏复核原清单和客户端，不能拿目录总字节冒充单视频大小。字幕等附件保留，不参与视频入库；第二视频、未完成条目、字节矛盾或路径越界不选择来源。目录中的唯一视频可沿相同人工目标修订合同生成后继计划，复制原清单/步骤证据，不重新观测或新签来源。文件能力继续检查目录身份、marker、符号链接与发布后文件身份。

- 本地计算/规范化 infohash，使用稳定操作标记；同 client/infohash 的现有外部绑定复用，不偷偷修改保存路径/分类。
- 外部绑定保持到确认移除；终态历史允许保留，新的生命周期不能被永久唯一键堵死。
自身需求的读取、带键撤回、无键释放以及回执重放，都必须在事务中先核验当前会话用户的活动状态和角色；锁序为用户→根→单元→获取。Actor快照或已有成功回执不能恢复失效权限，API/system凭据不能调用手动释放。只可撤回自己持有的目标，不能利用管理员角色替他人撤回；需求变更、可确认未发送的释放、审计/事件及成功回执同事务，末尾失败整体回滚。已有submitting、unknown或tracking任务时，撤回只改变逻辑目标，不调用外部删除、不释放保护，也不声称外部任务已取消。丢回执可在当前权限有效时复用原键恢复，其他需求、原任务和历史事实保持。

- unknown 保留候选、操作 ID 与占位。事实 TTL、job 租约到期、一次查询为空都不能释放或换资源重试。
- 支持可靠标识幂等的适配器只安全重试同一操作；否则转人工。worker 发送前复核是否仍有有效需求，无需求且确定尚未发送才安全取消。
- 明确失败/确认移除、提交前安全取消，或入库需求全部处理完成，才能按协议释放。整理结果不确定继续保护。
- 人工强制解除须审计并说明外部动作可能仍存在；系统不把人工判断包装成证明。

**job 租约是执行权，reservation 是业务排他。** DB token 不能撤销已发出的请求或文件操作。

### 6.4 做种资格与保留文件移除

M10做种规则仅使用下载器的原生做种秒数、分享率和已验证下载路径的剩余空间；添加时间、累计字节或全局磁盘容量不能替代这些事实。规则显式选择any/all，未知不当作0；Transmission的-1分享率为未知、-2为无限，qBittorrent的9999仍是有限显示上限。qBittorrent容量只在默认保存目录与任务路径完全一致时可用；Transmission按任务路径读取容量。两种原生客户端均在移除前最后一次身份读取后调用授权检查，只允许一次保留文件的POST，不重放副作用请求。

管理员通过版本化配置绑定下载器更新时间和配置摘要、规则、周期及原账号。保存配置、成功审计、事件及原键回执同事务，保存和GET不产生执行或网络请求。schema112仅为新普通发送在原事务中保存实际端点摘要及原job租约；历史任务不从当前配置补造发送证据。普通任务必须同时有原发送成功回执、完成整理计划和步骤、整理job成功回执、每项原需求的有效文件映射及未撤销入库事实，且没有待处理需求、占位或整理工作。自动TV、整包、升级等使用独立账本的任务，以及外部或历史未知任务，不借用普通资格授权；缺少对应移除证明时保持原任务与保护。

schema113的永久许可按原task/operation唯一且只追加，绑定原下载器、配置版本、账号、job及首次有效owner/token和原生hash/tag/path/bytes。末尾延迟约束重新验证权限、规则、有效租约、完整入库图及物理gate；许可、审计和事件同事务提交后才可发送。获取锁和触发器阻止许可后为同一原操作追加需求或重新领取发送任务，配置或租约变更不能产生第二份许可。实际动作固定为qBittorrent deleteFiles=false或Transmission delete-local-data=false，不删除本地文件，不撤销入库事实，不消费其他需求。

schema114调度每批至多64项，只排队具备完整证据且到期的任务；job/dispatch/schedule与成功审计事件同事务。活动dispatch冻结账号/配置/摘要，不因编辑而改写；原生Retry-After按既有1小时上限持久退避，未知结果保留永久许可。schema115记录原job每次租约的物理执行和释放，逻辑租约过期不能释放仍在执行的会话锁；App单消费者取消后等待真实I/O返回，再记录释放。已释放且已有永久许可的恢复只读观察原任务，absent确认完成、present转人工；数据库写回回滚或丢回执均不重发。会话失联且无明确释放证据时转review_required，不把锁消失当作I/O已结束。有限3次租约尝试只允许观察恢复，不能重建永久许可。

<a id="storage"></a>
## 7. 媒体库扫描与文件整理

### 7.1 根身份与基线

每个 root 配置规范化路径、媒体类型、命名策略、操作能力及身份标记。挂载根同时验证预期 mount source/类型与本轮 mount fingerprint；只读根由用户放置 marker，不降级成仅判断目录存在。

**命名模板（M9-04，无新schema）**：`organization.naming_templates`复用版本化setting与原键回执，包含显式`movie_template`和`episode_template`字符串；空字符串保持现有规范命名，缺失字段不等于清空。单次字面解析有限占位符，变量只产生一个路径组件，不递归解释其花括号；拒绝未知/缺失字段、不完整语法、绝对路径、路径逃逸、控制字符、非法组件及超限路径。渲染使用受控元数据、真实稀疏季集号和可信命名空间身份，品质占位符必须有对应可验证事实；未知字段不补造，输出保留原视频容器扩展名并通过正片路径过滤。管理员草稿/已存配置预览只返回样例相对路径和配置版本，不读视频、不请求元数据、不创建任务或事实，不授予执行权；权限撤销与数据库失败不会回退为成功默认值，成功与错误及历史回执均禁止缓存。

**可视化规则编辑（M9-07）**：设置页复用现有策略发布与命名setting合同，不另建规则存储。词行草稿深复制全部嵌套字段，必含、排除和偏好分分别编辑；客户端校验仅用于阻断不完整输入，字面匹配、品质分与偏好分的计算仍以服务器预览为准。历史省略`custom_words`与显式空列表保持各自编码。命名面板明确区分草稿预览和已存模板预览；样例未知字段省略，稀疏季集编号不补齐，预览不授予执行许可。保存前必须持久化原请求键；存储失败不发写请求，账号或角色变化使旧生命周期回调失效。命名丢回执先读取原键，只有明确404后允许相同内容原键重试；策略沿原公开幂等合同显式重放完整冻结命令，不自动发布。新版本只影响后续评估或首次计划，已冻结执行目标与人工确认路径继续沿M9-04边界恢复。

普通计划、自动TV首次计划及升级首次物理准备在原业务事务内锁定配置版本并冻结目标，恢复不读取当前模板。升级事务外物理观察两侧即使路径相同，也须核对配置版本；后续排队重验使用原准备记录，同时保留当前账户、配置根/映射、授权图、账本和SQL约束校验。整包预览的`naming_version`随原键命令持久化，确认时锁到同版本，变化则重新预览；已有整包计划直接展示已冻结目标。新增版本证据零值省略，历史JSON/摘要/回执保持原形。外部文件整理和跨根复用的人工确认已经包含目标相对路径，当前模板不覆盖该意图；复用目标存在时只读校验原inode或原持久完整摘要，不再次进入写接口，替换或缺失证据拒绝。模板更改不批量重命名库存、不重定向原永久许可，也不增加下载、整理或删除权限。

健康为 online/offline/stale/stalled，baseline_pending 独立。根配置/身份变化递增 generation；mount ID/device 不是 NAS 的跨重启永久身份。同一 generation 的成功扫描保留目录device/inode作为保守的基线连续性围栏；变化只表示无法确认当前根仍为原基线，不推定数据丢失，也不自动绑定新目录。合法重挂载/重启导致身份变化时，需显式核验并推进新generation建立基线。开始、遍历结束、应用缺失前均验证；无法确认则 unknown/stale。

新根完成基线前不自动获取。正向发现与匹配可入库，但不能因此宣称整个扫描成功。

### 7.2 批次、水位与令牌

```text
探测根 → scan_run（generation、scan_token、mutation 水位）
   → 分批记录 scan_observation → 再探测根
   → finalizing（短事务分批对账）→ succeeded（基线可用）
子目录失败 / 访问异常 / 身份变化 → partial/failed，不应用缺失
```

- 同一根一个活动扫描；单调 scan_token 阻止旧扫描迟到覆盖。
- 根读取、探测健康、扫描开始和中断通过扫描窄端口显式构造；用例不得携带pgx/sqlc类型或直接访问store。运行中中断按根→run锁序核验当前配置/健康版本、token和起始水位，只能终结其授权的运行记录；失败回滚，不改已终结记录。数据库错误必须返回，不能把持久失败隐藏在文件I/O错误之后。
- 开始扫描和探测健康写回绑定实际探测的根路径、媒体类型、标记文件名、generation 和健康版本；取得物理槽后重读根配置并核对目录能力，SQL 等锁后再次校验快照。旧目录探测不得改写替换后的根健康，不得递增其令牌或创建观察；数据库写入错误必须中止。正常离线恢复使用健康写回返回的新版本继续开始扫描。
- 已有成功扫描的根，在新遍历开始写正向观察前，比对实际FD目录身份与同generation首次成功封账的物理身份；仅标记文件存在不足以证明挂载来源一致。根锁内开始和封账入口再次比较该持久围栏，拒绝缺失/不同身份，错误回滚不得生成新run、观察或缺失候选。复用既有成功scan_run/封账历史，不让partial/failed扫描或新传入的目录重签原基线；无成功历史才允许建立首个绑定。
- 扫描成功写回先锁定根行，再核验当前健康、generation、scan_token 与运行中扫描；扫描终态和根基线在同一短事务内提交。等锁前的快照不能用于授权，末尾写入失败必须整体回滚。
- 失败写回只能将执行中的running/finalizing终结为partial/failed；封存扫描中止必须核验run/token和原封存凭据。迟到的失败及重复完成不能改写已终结扫描。
- 整理、匹配等内容变更递增 root.mutation_seq；只处理 file.last_mutation_seq 不大于扫描起始水位的旧记录。
- 每条已验证正向观察与文件版本替换通过窄事务提交，取得根锁后校验当前路径/类型/标记、generation、健康版本和run/token/水位。版本变化按根→单元→文件锁序重新核验映射修订与文件状态，原子退役旧版本及其映射、撤销旧文件的imported/confirmed来源、创建新UUID和观察；保留旧质量JSON、历史映射与文件，不继承质量、映射或确认，也不释放下载占位。新stat.v1指纹记录inode/size/mtime，只用于当前格式的变化检测；它不是内容哈希，旧未知或不透明指纹不能重解释或补签。扫描起点后修改的文件受水位保护；同run/path相同观察可重放，冲突观察必须拒绝。部分扫描仅保留可靠正向观察，不推导缺失。
- 完整遍历结束后再次核对根标记、目录device/inode及根配置快照，封存run进入finalizing并禁止继续增删改观察；正向观察及计数持久提交。分批对账使用持久UUID游标，每批最多128行短事务，并在取根行锁后核验generation/token/健康版本及原封存凭据；排除起始水位后修改或创建的文件。游标与缺失计数原子前进，最后一批才同时发布succeeded和根基线；完成回执重放只读原计数，不重复推进或重写时间戳。
- 进程重启在取得原根物理槽后核对目录device/inode、标记和封存版本，续做原run/token而不重新遍历；取消和数据库失败保留已提交游标。原目录丢失或被替换时，只有当前根快照授权才能原子写offline与partial，不应用缺失。配置或健康版本变化只能终止原封存run，不能改写替换根健康/基线；旧token不可干预新扫描。活动且仍拥有当前token的finalizing扫描禁止数据库回退。大库不在一个长事务中完成整轮I/O，未知覆盖不得推导为不存在。

### 7.3 缺失确认

首次完整成功且未见：missing_candidate，暂不失效拥有。至少另一轮完整成功，间隔默认 5 分钟，目标 Stat 明确 NotExist 且根身份有效，才标记 missing 并同步失效文件事实/确认。

同一 run 重放不能计两次；文件重现清除候选。权限错误、网络错误、超时不是 NotExist。缺失候选至少 10 个且超过已索引文件 20% 时暂停批量应用、要求复核。

候选周期按文件UUID与首次扫描保留历史，首次和最近来源都必须引用同一根的scan_run；分批事务同时提交候选、计数和游标。来源尚未成功的候选仅为临时证据，不能用于确认；同run重放不刷新来源或时间。文件正向观察即清除候选，即使本轮随后partial。来源失败、根身份/generation/健康版本变化或文件越过原水位时，下一次可靠缺失重新建立周期，不沿用旧轮次。

比例分母在封存事务冻结本轮可对账的旧文件范围，排除起始水位后创建/修改或已退役文件；严格超过20%且至少10个时持久标记需复核，完整遍历仍可发布基线，但该来源不允许批量失效。旧游标缺少这组证据时保持未认证，不补签历史。候选历史或活动finalizing存在时禁止回退丢弃来源关系。

只读确认清单要求两次不同的完整成功扫描、第二轮开始距首次完成至少5分钟、同目录device/inode及配置/generation/健康版本、当前根在线且token有效，两轮均未触发复核。清单只能授权受限目录能力内的Stat检查；明确NotExist及提交前再次校验仍不可省略，权限/超时/符号链接/路径逃逸等错误不能转为缺失。

缺失复查独立持有原根物理槽，在受限目录Inspect前后校验标记和device/inode；只有明确NotExist允许失效，重现只清除候选。提交在根→单元→文件锁序后再次核验封存凭据、两轮来源资格、文件UUID/路径/水位/指纹/映射版本，并检查数据库签发的五秒有效期限，等锁时间不能延长期限。退役文件与映射、撤销imported/confirmed、推进根水位、清除候选及保存不可改写回执同事务提交；不释放下载占位，不覆盖历史质量JSON与来源。相同检查恢复只读原回执，失败回滚全部写入；回执历史存在时禁止回退丢失证据。

生产扫描由app显式组装持久调度和两个独立消费者；调度只创建按根隔离的任务，默认每五分钟扫描。任务持久绑定root、phase与run，同根queued/running/dead任务阻塞后续调度，其他根可独立执行。调度取得根和调度行锁后，必须在新的READ COMMITTED语句中重新读取到期时间、前驱成功状态和queued/running/dead占用资格，不能复用选根语句获取锁之前的快照；实际入队数只计成功写入的根，唯一通道约束和SKIP LOCKED始终保留。所有写入事务与提交前核验当前job owner/token/state/数据库租期及root/run范围；逻辑过期不释放仍被执行中的物理槽。取消遗留running遍历只在重新取得物理槽和有效任务授权后原子转partial并绑定新run，失败整体回滚；finalizing按原封存游标恢复。基线完成、扫描任务成功与后续缺失任务原子提交，最后缺失回执与缺失任务成功原子提交。运行时取消并等待所有物理消费者退出后，应用才关闭数据库并释放数据目录锁；活动扫描任务禁止回退丢失持久绑定。

观察不是文件系统快照；真实挂载切换与恢复仍需故障测试。可用性未知期间暂停自动获取，不能因 NAS 暂时失联全库补下载。

### 7.4 冻结整理计划

plan 冻结源身份/内容证据、单元映射、目标根/相对路径、操作、冲突策略与所用 revision。批准后修改生成新计划，不执行中读取最新命名策略。缺少可信冻结证据的存量未完成计划，在升级后转人工复核，不使用默认值补出「可执行」证据。

```text
draft → awaiting_approval / queued → executing → succeeded
                                    └→ failed / manual_review
manual_review → 明确判定后重新排队 / 终止
```

**目标版本冻结实际切片（2026-10-10，schema38）**：电影专用 `CreateMoviePlan` 曾接受 TV/退休单元，已真实 PG 反例确认并在创建、首次文件写入与最终入库拒绝。`transfer_step.target_evidence` 单独保存媒体/单元 UUID、metadata version、标准 key 与 parser version，不混入品质事实。创建在 root→unit→task/acquisition→media 锁下同事务冻结；执行和导入锁后逐项复核，媒体/单元版本变化不读取最新值重签。数据库首次绑定要求当前 canonical 对应；冻结证据不能清除、改写或改目标，step 完成还需当前证据，旧二进制的同事务写回也拒绝无证据或过期版本。未知物理硬链接保留，不删除文件或释放占位来伪造回滚。38 将存量未完成待执行计划转 review_required；executing 的 owner/token/lease 原样保留，缺证据无法完成写回。非空证据禁止降级丢弃。真实 PG 覆盖 TV/退休绕路、批准后身份变更、unit/media 版本在首次写入前和硬链后变化、冻结重签拒绝、37→38 人工隔离及执行权保持；独立 PostgreSQL18.6 的全量与 app/transfer/download/postgres/subscription/httpapi/pgtest race、生成漂移通过。仍只是受控电影安全前置，不是可信 TV/包/升级整阶段放行，**下载窄合同实际切片（2026-10-10）**：`internal/usecase/download` 实现已通过直接 go list 核对只依赖 domain/ports 与标准库/UUID。任务执行、客户端管理和运行配置分为三窄合同组合，命令保留 operation/client/candidate/path 与原 JobLease；结果是普通 DownloadExecutionTask DTO（REAL进度精度、可选plan UUID），不带 DB 事务/生成nullable类型、密钥、下载链接或原始错误。App 显式构造 store/download repository；原发送/未知对账/观测/完成与组织 job 同事务协议迁入实现层，解密和客户端配置在实现内，网络在短事务外且物理独占不因逻辑超时释放。带未知结果的非空任务与错误同时保留，nil不造成功；纯端口探针确认仅一次提交、对账和观测不触发 Add，context/租约不变。原 PG/HTTP/App.Run 丢回执/重启/旧token/配置变更/并发源故障夹具重跑全量与关键race通过，生成漂移通过。历史关键词默认下载器 router 只供旧测试、无 App 注册，归 store 原型，不是新自动路由权限。上述 DAG 前置修复仍不开放 TV/整包/未知覆盖。

**整理窄合同实际切片（2026-10-10）**：生产 `internal/usecase/transfer` 实现依赖已收敛为 domain/ports（直接 go list 核验）；`TransferRepository` 命令和返回值是普通 UUID/标量/品质 DTO，不暴露 pgx/sqlc 或事务。App 显式注入 `internal/store/transfer.NewRepository` 和 FD filesystem，原 root→unit→operation/media、执行 owner/token/state/lease、物理跨进程锁、schema38目标版本、源根/挂载/身份、硬链观察、导入回滚及独立 Outbox 规则迁入实际 store 实现，网络/文件 I/O 仍在短事务外。原 PG 安全故障夹具随实现移 store，增加纯端口命令/ctx/lease/准备失败不执行探针，生产 App.Run 和原全量/关键 race、生成漂移重新通过。历史 path copy/move、NFO 和直接 SQL reuse 原型移 `adapter/transferlegacy`、`store/libraryreuse`，仍只有旧演示测试、无 App 注册，不作为 M3-05/06/正式刮削放行依据。


**M7-01 自动刮削接入边界（2026-10-20，本地验收通过）**：纯NFO已按已绑定的正TMDB ID输出 `<uniqueid type="tmdb" default="true">`，拒绝空标题/身份与非法年份，未知年份不推断；保留XML文本转义和原公开DTO，不改冻结媒体/授权/回执JSON。历史 `transferlegacy.ScrapeAndSave` 仍不组装进App。生产接入使用独立 `media_sidecar` job，由原 `PublishImported` UoW的事务绑定queries安排，零通知通道/零媒体服务器也不能漏排，不使用另开连接的Queue.Enqueue；事件按unit发布，文件权限只从file/unit/root UUID及当前有效imported事实重新解析，payload里的路径不构成权限。该任务是已提交入库的系统续作，不授予下载、删除或其他用户需求写入权；恢复隔离、当前根代际/挂载身份、文件/映射/媒体身份或版本不符均阻止文件动作。
海报GET、图片解码和文件I/O在短事务外；只接受提供者opaque图片路径投影到固定TMDB主机，拒绝重定向、超限/坏图片，实际输出受限JPEG，物理请求和body关闭前不释放共享预算。NFO与海报分别记录结果；网络失败不撤销入库或篡改品质/占位/库存。文件发布复用现有物理根独占与FD能力，执行前后核对配置路径命名空间、根/源文件和目标目录身份；同目录独占临时写入、fsync及原子no-replace发布，既有普通文件/符号链接/目录均不得覆盖、跟随或删掉。TV show目录必须由当前目标命名/映射证明，不盲取两级父目录或把共享库根当作品目录。取消/租约丢失仍等待实际I/O返回；已发布而数据库回执丢失时先观察，保留既有文件并明确区分本次创建、已存在和未知，不认领用户文件或删除来伪造回滚。App为唯一组装点，usecase只依赖domain/ports，sidecar仓储按job owner/token/state/数据库有效租约及当前绑定复核后提交结果；M7-07显式批量入口按下述独立批次合同组装，不改变已有文件执行围栏。
后台任务payload只含version=1、source_event_id/file_id/unit_id；快照以UTC数据库JSON的SHA-256绑定当前事件、有效文件/映射/事实、显式TMDB命名空间和版本、健康根的首个成功扫描物理身份。普通根scan_token、mutation_seq、updated_at和展示名不改变该授权；路径、代际、健康版本、marker及源文件证据仍参与绑定。`Validate`/`Complete`重建并比较完整DTO，不能只信调用方拷来的摘要。job设置max_attempts=1；App持有并等待受控worker，原owner/token续租失败或取消时等待同步I/O返回，过期或未知结果转dead/review_required，不自动重排可能发生的物理操作。仅新建staging使用0600，完整内容落盘后将本次新inode设0644，再从固定FD无覆盖发布并同步目录；既有条目权限和内容不变。正常创建、保留既有普通文件分别记录，未发布、不安全条目及已发布后不确定均不得记为正常成功。独立复跑入口为`mise exec -- task test-m7-sidecar`；本地App/PG18.6/race/原生文件验收已完成；外部图片与下载到达由受控原生协议夹具提供，不作为真实外部传输实证。

**M11-01 Fanart艺术画边界**：复用上述`media_sidecar`任务、当前入库事实、物理根槽、逐文件授权复核与无覆盖发布，不另建下载或文件执行运行时。`fanart.api_key`使用现有设置主密钥加密，公开设置、审计与原键回执只返回配置状态；`fanart.artwork`仅接受布尔`enabled`，默认未启用。配置保存不创建任务、不请求外部服务；原键仍恢复历史结果。所有公开设置列表、保存和回执响应（含错误）使用`private, no-store`。每次侧写读取独立配置快照，在原生响应返回后与每项艺术画发布前复核开关、密钥版本及内容摘要；配置变化或不可读取时停止剩余艺术画，已完成文件结果仍按原合同记录。取消仍等待实际I/O结束。
电影仅使用已绑定的正TMDB编号调用Fanart电影接口；剧集先用原TMDB编号读取`external_ids`，校验响应TMDB编号后取得正TVDB编号，再调用Fanart剧集接口。无映射、身份错配、坏包或网络失败不猜测编号，不改历史媒体/订阅/冻结授权JSON，不以艺术画请求授权任何业务写入。原NFO及海报流程独立保留。Fanart响应身份和全部受支持候选都需验证；图片仅允许固定`assets.fanart.tv`主机及对应媒体类型/编号路径，不携带API密钥、Cookie或Authorization，不跟随重定向。JSON限2MiB，图片限8MiB、每边4096像素及总1600万像素，单请求最多10秒；实际解码后输出PNG或JPEG，PNG保留透明度。电影写`logo.png`、`clearart.png`、`banner.jpg`、`disc.png`；剧集只写前三项，不制造原生协议没有提供的DiscArt。作品目录仍需原命名/映射证明，结果绑定真实inode；已有文件、符号链接和目录不覆盖、不认领，失败不回滚已完成入库。
**M11-02 TVDB备用元数据边界（本地验收通过，证据见开发计划）**：TMDB继续提供规范作品及逐集身份，TVDB使用独立合同、编号和来源证据。`tvdb.credentials`将API密钥与可选PIN作为单个加密配置保存，`tvdb.fallback`为默认关闭的版本化开关；保存不发起外部请求，公开读与原键回执不返回凭据。原生v4登录只发送密钥/PIN，后续请求使用返回令牌，不把API密钥当Bearer，不自动重登或重放；固定主机、禁重定向，分页/响应大小和时间均有限界。只接受明确official编号和完整、连续且同作品/同季的分页；未知日期、绝对号和集数保留未知，score不当作用户评分。
缺集补充复用当前授权的订阅刷新入口。先恢复原键，再读取外部数据；TVDB作品由TMDB外部身份映射证明，候选逐集必须经TMDB原生Find取得唯一真实逐集ID及规范坐标，再读取该TMDB单集确认身份和日期。无可靠交叉映射不生成单元或Wanted，不用TVDB日期/绝对号替代未知规范事实。备用会话在网络前后复核配置版本与凭据摘要；schema117在原刷新UoW内再次锁定配置并检查当前账号权限、媒体版本和既有绑定，新增单元、独立`tvdb_series_identity`/`tvdb_episode_evidence`、Wanted、审计、事件及回执一起提交。既有身份不可改绑，补充只追加、不声明完整季，不改既有日期/绝对号；后续主源遗漏不能自动退休有独立补充证据的单元。取消等待物理读取返回，原键恢复不依赖当前上游可用性或开关。
译名降级仅组装到公开只读详情。用TMDB完整translations响应确认zh-CN字段为空，而不是根据拉丁字符或原名相等猜缺译名；TVDB的zho译名只补缺失展示字段，身份、评分、季集数、日期、图片及已有译名保持主源事实。返回独立`translation_source`，包含TVDB编号、语言与实际补充字段；备用读取失败、无映射、迟到配置或不完整证据保留原详情，主源失败不伪造详情，调用方取消仍返回取消。创建订阅、侧写、命名与冻结回执使用原主源提供者，展示译名不隐式改名、写库或授权执行。

**M7-07 媒体库批量补齐边界（2026-10-20）**：schema94保存`media_scrape_batch/item`，命令键以当前账号为范围，GET可按`command_key`只读恢复原批次；原键重发先返回原批次，不因后来文件或事实变化追加任务。批次提交先持有事务级提交互斥，等锁后复核当前活动管理员用户会话与恢复隔离，再锁根并在同一事务选择当前有效imported事实、活动映射及真实`media.imported`事件，按file/unit去重并绑定最新事件。批次、一次性sidecar jobs、item与成功审计同事务提交；尾失败全部回滚。候选状态包含原自动任务与所有后续批次任务，排队、执行中与待人工复核不重排；已成功的任务可经用户再次明确提交检查缺失产物，执行仍完全复用M7-01物理根槽、当前绑定/租约复核及无覆盖发布，所有既有文件内容和权限不变。结果不明/过期仍转人工，批量按钮不能借新键盲重放。GET不做文件/网络探测，不修改媒体或需求；徽标明确表示已记录的任务状态，历史成功不认证当前NFO/海报存在。POST严格接受单一`all:true`，拒绝重复/未知字段、null、超限与尾数据，响应private,no-store；成员/Token/非用户主体禁止。媒体库面板在账号/角色变化后隔离旧响应，提交前持久化账号范围原键，未知结果只读恢复、不自动POST，明确重试仍用同键；数据库读失败清除旧计数与操作就绪。迁移降级如已有批次历史则拒绝丢弃原键回执。公开入口与浏览器库存为明确SQL准备，原生Execute独立证明真实NFO/JPEG及用户文件保留；不把展示状态当作真实文件证据。复跑`mise exec -- task test-m7-media-scrape`与`mise exec -- task test-m7-media-scrape-ui`。

**指定文件受控重刮削（schema141）**：普通侧写仍只补缺，不覆盖已有文件。只有真实新建/发布且附带内容的普通结果，在原完成事务按原job、根/目录、文件/单元及内容摘要记录生成归属；历史成功、缺失内容或人工文件不补造归属。管理员Cookie对选定file/unit显式预览当前可信元数据生成的规范NFO与poster.jpg，文件、目录、归属或旧内容不一致均held；明确确认后以冻结预览、原键和永久许可执行有界原子替换，保留旧内容保护及并发变更检测。网络和文件I/O在事务外，当前元数据/配置/权限/根/事实与租约在发送和完成时复验，延迟约束后再检查有效性。取消仍等待实际物理返回；部分冲突或结果不明保留排他许可并转review_required。安全回执只含文件名、状态和内容摘要，不含NFO文本、图片或凭据。页面未知提交仅GET原键恢复，不自动生成新命令；可选Fanart产物与人工/历史无归属内容保持原样。
**M8-01 字幕续作边界（2026-10-20，本地实现与Shooter实网验证通过，支持边界见本节）**：`PublishImported`在同一入库事务安排独立`media_subtitle`任务，按file UUID/当前mapping_revision去重。先单独锁文件、下一条语句用新READ COMMITTED快照查询全部历史，再匹配现有active部分唯一索引；同版本不能因前任务成功或死信又自动排队。新kind在Load/Validate/Complete及专属过期恢复中明确绑定，复用已提交imported事实快照但不借`media_sidecar`租约和结果权限；成功审计、实际文件结果及job完成在短事务提交。原NFO/海报完成合同与历史JSON不改，不追加下载、库存或需求成功事实。任务max_attempts=1，过期/不明结果转人工，App取消或续租失败后仍等待同步物理I/O与清理返回。
字幕只针对当前可信视频文件，真实根槽内按同一FD核对dev/inode/size/mtime后读4个4096字节块，再核对FD及重新打开的源路径身份；源与根命名空间、marker、父目录和数据库绑定在网络/写文件前后重查。Shooter MD5仅是上游检索协议，不授予文件/媒体权限。偏移保留Subliminal2.1.0及既有实现的4096、2*floor(N/3)、floor(N/3)、N-8192；其他实现对第二块的取整有分歧，不未经独立证据改签；小于16KiB不查询。下载只接受固定API同源明确SRT链接；完整校验所有包/文件的URL与数量边界后，按上游返回顺序选择首个SRT（不要求整个响应只有一条，非SRT和后续项也不能绕过信任校验），保留签名query、拒绝重定向和跨源，不发送本地路径；API限128KiB，字幕限2MiB。HTTP200单字节0xFF或明确空数组是无结果；错误、取消、429及畸形响应不得伪装空结果。字幕需通过编码/时间轴/内容校验后输出UTF8，Han是语言线索，不认证翻译质量；ASS不改后缀冒充SRT。发布只允许视频实际同目录、相同基名的`.zh-cn.default.srt`，复用FD/no-replace/fsync与新inode0644，原NFO白名单不扩展。已有常规字幕可只观察保留，不请求网络、不认领所有权；符号链接、冲突或不确定发布保留保护并转人工。回执路径是根相对路径，原生发布只接收其basename与已验证父目录。
播放器兼容性按实际配置分别验证，不从后缀推断通用支持。固定版本mpv的默认`sub-auto=exact`只接受影片同名与语言后缀，本次实测未发现带`.default`标记的字幕；独立播放器验收改用其官方`sub-auto=fuzzy`同影片名前缀自动发现模式，保留原文件名，禁止通过`sub-file`/`sub-add`手动装载冒充自动发现。仅在真实App生成、播放器自动选中和seek后解码中文cue均通过后记录该配置的识别证据；不泛化为mpv默认配置、Jellyfin或其他未测播放器均可自动识别。
**字幕候选查询与确认**：管理员可对已入库文件显式查询Shooter候选；查询绑定原file/mapping_revision、指纹和当前根身份，使用独立有效租约及物理根槽，取消仍等待实际I/O返回。新查询在既有最多8份输入内按规范化SRT字节精确去重，保留首次出现顺序；对白或时间不同不合并，任一非法输入仍失败而非伪装无结果。候选只公开不透明ID、来源、语言提示、格式及条数/最早开始/最晚结束毫秒摘要；摘要不证明影片时长、匹配度或翻译质量。URL不离开适配器，有界SRT字节使用主密钥加密保存；摘要在当前权限下从缓存计算，不持久化明文、不发起网络或文件操作。读取旧文件版本在解密前返回当前版本的未查询状态，不展示旧候选；错误密钥或损坏内容返回安全错误而非零值。历史候选ID和记录不重写，去重仅在新查询生效。确认必须显式选择同一版本候选，永久许可冻结选择，写入继续完整验证、当前权限与文件/根身份复核及无覆盖发布；已有字幕不覆盖，不确认的候选不落盘。结果不明或确认中只恢复原状态，不盲目重放发布。自动续作原规则独立保留，不据候选查询触发整理/入库。

协议依据为Subliminal2.1.0 Shooter实现和公开测试。原生接口支持多个候选，当前选择规则已见前文，不能将它误写为“必须唯一候选”。本轮默认TLS/原生客户端以公开历史协议哈希匹配下载并校验69806字节中文SRT，1父项/0skip，未下载关联影片，也不授予本地文件/媒体权限；真实API/文件下载无mock，不能与自有HTTP App落盘案例合并描述成同一实片的实网全流程。授权Sintel官方1,180,090,590字节版本四块精确范围hash的原生请求曾TIMEOUT，后续已取得明确NO_MATCH；无结果不等于服务故障，也不算成功下载。源码公开协议正例、App/PG围栏/实际文件落盘、指定mpv自动加载及中文解码分别验收，具体终态见开发计划M8-01。本轮可用来源为Shooter；SubHD公开prepare-download曾403且未确认稳定免验证API，未启用，不猜临时链接或绕过会话验证。其他提供者/播放器需独立实证，测试数据不授予业务执行或文件组织权限。
**M8-02 公开想看读取与身份准备边界（2026-10-20，本地验收通过）**：独立`DoubanWishProvider`读取明确指定用户的`movie.douban.com/people/<uid>/wish`，不复用Frodo榜单签名来假设用户API可用，不自动展开关注用户、收藏豆列、在看或看过。请求固定movie/TV筛选、grid/all/time及15条偏移窗口；禁Cookie与重定向，HTML限2MiB并用DOM解析，用户绑定profile规范ID（数字请求可由同profile可信头像证明数字ID），标题必须明确想看/所选类型/同一总数。条目只取可信subject数字ID、标题别名和intro开头年份；不按集数、标题猜类型。`WindowEnd`表示上游声明的窗口终点；可见作品少于声明数量时`WindowComplete=false`且下一游标仍取声明终点，不补造条目。单窗口完整不证明全列表一致快照，分页重复、变动、上限或中断均不得驱动退订/删除。
自动身份准备使用独立`WatchlistIdentityMetadata.SearchMediaIdentityCandidates`，复用TMDB原生请求、预算和类型解码，仅在明确page/total_pages/total_results证明首屏即完整集合时标Complete；原显示搜索语义不变。解析器汇合全部别名的同年同类型候选，Unicode组成、大小写和空白归一化，但保留标点与符号；未知年份、候选集不完整、不同别名指向多身份或详情复读不一致均不产出可用TMDB身份。不把展示榜单映射或该身份结果当执行权限。原`POST /api/v1/sync/douban`曾直接走未经证实的Frodo路径和首个搜索结果，当前返回安全503（匿名401）、零数据库/上游I/O；持久配置、源版本、租约、匹配依据、稳定原键与成功关联必须在现有订阅创建同事务复核后才能启用自动写入，不能伪造会话或绕过暂停/配置保护。
**M8-02 持久配置边界（schema95，2026-10-20，本地验收通过）**：每个当前用户账号只管理自己的公开想看配置，显式movie/TV根与版本；管理员不因角色读取其他账号命令，Token/系统主体禁止。`GET/PUT /api/v1/sync/douban/config`与`GET /api/v1/sync/douban/config/commands/{command_key}`分别读取当前设置、保存设置和只读恢复历史回执；未配置返回明确null，读取失败503而非空成功。PUT要求原键、expected_version和严格JSON，拒绝null/未知/重复字段、非法类型/键、额外query与尾数据；包括认证失败均private,no-store。仓储按恢复隔离、当前活动账号锁及有序根锁复核，配置、不可变原键回执、成功审计和配置事件同事务提交；相同键/体返回原结果，不因当前版本更新或停用重新保存，异体/旧版本409，显式缺失根404、错类型或启用时离线400。历史快照核对完整输入/摘要/owner/key/id/version，当前时间用Time.Equal校验同一时刻后仅对齐内存副本再全量比较，存储原回执不重写。配置与历史禁止DELETE/TRUNCATE，历史回执禁止UPDATE，非空schema95降级拒绝丢失历史。保存与GET不抓取上游、不排下载/同步任务、不改订阅/Wanted/文件事实；`enabled`表示已记录意图，尚不是自动调度成功，旧POST继续503，执行管线与M8-07前端另验。
**M8-02 持久执行边界（schema96，2026-10-20，本地验收通过）**：单独run/page/item台账复用`douban_wish_sync`队列，每job只处理一个15条窗口；页完成与后继页任务同事务，movie末页再进入已配置TV首屏。配置启用后由App维护循环按当前账号/配置版本/在线目标根派生任务；同版本终态轮次24小时内不重复排队。可见条目持久化原始类型/别名/年份及摘要、匹配状态/摘要和稳定原键，缺页/重复/总量变化/未匹配项形成partial而非全量成功；不制造缺项退订。每次执行登记一次execution_id、原job owner/token及专属PG会话PID，并实际验证该会话拥有配置锁；取消、续租失败和关闭均等外部调用返回后释放，失联/过期/失败页进入review_required，不盲目重排或覆盖未知旧执行。网络I/O在事务外，使用同一受控许可。
豆瓣想看调度逐配置独立事务、每项最多15秒；单项失败回滚并上报首错，同批其余配置继续排队。每次最多64项、最多两次候选读取，按(owner_id,id)实例内游标循环扫描，已处理的失败/不可用配置推进，取消中未完成项不推进；末尾回绕、重建从起点重扫，原数据库冻结与去重保持唯一依据。同实例重叠调用让出扫描。恢复成功后，排队错误不阻断已提交任务认领；恢复失败或调用取消仍停止认领，实际执行继续校验当前配置、权限、原来源和物理许可，未知执行仍转review_required。
专属`NewDoubanWishCreationStore`复用原`SubscriptionCreationService`及原事务，先锁原job再按现有账号/策略/身份/根/订阅锁序复核；运行、配置版本、当前账号、页/项归属、source/match摘要、稳定原键、显式根与新鲜TMDB详情都必须对应。只确保default策略/all范围，保留已有绑定和暂停；意图不同则conflict待核对，不同步提权或覆盖配置。媒体/单元/订阅/Wanted、原创建回执、成功审计/事件和item成功关联同事务；匹配原创建回执时也在本事务提交本轮项关联，不另发成功回写。关键写入有COMMIT时钟约束，尾部租约或许可失效整笔回滚；原有无同步围栏的HTTP创建和只读历史恢复保持原合同。App拥有worker、15分钟单页预算和2分钟续租，所有退出路径等待真实执行结束。本地原生App已验证配置→页面/TMDB→订阅/Wanted、失败与停用围栏、重启/原键恢复。已有created历史必须先验证同一原创建回执及其owner/config/source/match，否则元数据前拒绝，缺失回执不得补造成功。配置绑定切片追加可选bindings（indexer_ids与downloader_id），沿用schema95不可变input/snapshot及schema96的config/version外键，不新增平行状态。首次省略为无绑定、编辑省略保留、显式空站点列表且无下载器清空同步配置绑定，非空站点必须配下载器；请求规范排序后计算原hash，继承绑定仅写有效snapshot，原省略JSON和历史回执不重写。读取与执行验证同版本回执，省略继承逐版本核验至明确输入或初始空配置，缺失/损坏失败关闭。创建输入必须与冻结绑定完全一致；已有暂停/等价意图保留，不同明确绑定和清空已有绑定均要求原订阅编辑入口，不由同步覆盖。端点共享锁位于原业务锁之后，提交前重新复核当前可用性和租约；只读历史恢复不依赖端点当前存在。最新本地App已验证首次保存含绑定的同步设置直接完成原字节单次Add、完整观察和同inode入库，未再调用逐订阅bindings写入；最终配置与创建PG矩阵、最终冻结hash一致的创建/App补验和全仓门禁通过，M8-02本地验收完成。现有端点删除保护不额外把不可变配置快照当永久引用；删除/停用端点后当前执行再次验证并拒绝失效绑定，需用户显式更新设置，历史回执仍只读可恢复。同步配置成功仍不等于任务或入库成功，前端归M8-07。

**M8-07 设置交互合同**：豆瓣想看设置使用schema95/96的版本配置与原键回执，显式选择电影/剧集根、站点和下载器；不调用已停用的即时同步入口，不把保存配置称为同步、补缺或下载成功。设置页及站点页的签到控件复用同一管理员表单和原键恢复逻辑，要求明确UID、固定NexusPHP 1.10.3 profile、站点时区、每日时间和来源总版本，不推定时区、不提供立即签到或结果不明时的重发入口。先读当前配置才可写；读失败撤销就绪状态，来源版本变化重新核对启用意图。账号/角色/来源切换卸载旧请求并丢弃迟到结果。浏览器恢复记录仅含主体、用途、原键、版本、无凭据输入的SHA-256及可选来源版本/明确拒绝标记，不持久化表单、UID、完整地址或凭据；保存前无法持久记录即不发送。丢回执、403及版本冲突保留原键，刷新后先读原回执，不自动PUT；明确重试仅接受原输入摘要。版本冲突须确认原回执404并重新读取现态后由用户结束，不再次保存。签到历史仅读持久记录，prepared/未知不显示为成功，GET不访问站点。

**M8-04 签到协议与副作用边界（2026-10-20，本地验收通过）**：首个原生合同固定为NexusPHP v1.10.3（`xiaomlove/nexusphp@31c176acae9dab3da038f72d15629d7c5bbf6eec`），不由M8-03的v1.5统计模板推定签到支持。官方`public/attendance.php`以当前认证`$CURUSER['id']`处理POST；部署未启用签到验证码时不需业务参数，遇验证码/挑战拒绝自动处理。关闭验证码时GET该页也可能签到，因此纯观察只允许固定根`index.php`。官方`include/functions.php`的唯一info_block欢迎行绑定当前UID/登出链接，并按当前UID、服务端`date('Ymd')`查询后输出三语正向已签或待签标记；不使用全页通用“签到成功”文字、不以缺少按钮推定成功。
原生观察只返回明确账号、原生pending/attended标记和本地请求起止时间。首页没有绝对日期字段，服务端TIMEZONE可配置，默认PRC不证明实际部署时区；不得把注入Clock日期或HTTP Date冒充原生签到日期。持久层必须明确站点时区、冻结日身份，修改配置版本/profile/时区不能给当天增加副作用机会；日期无法绑定、跨午夜、身份不符或结构缺失时保留未知并转人工。提交只确认原生“已签”状态，不能归因本次新增或把服务端added_time当真实发生时刻。POST派发后断连、超时、解析失败等均可能已产生副作用；不能据可重试错误重发，恢复只能对冻结日身份做纯首页观察，不能重新GET签到页。客户端必须禁止重定向及HTTP传输隐式重发，接口区分未派发与结果不明，调用方不能传任意目标URL或伪造成功标志。schema100的独立持久许可、共享来源预算/物理门禁、真实COMMIT围栏与owned worker已在对应纵向闭环验证；原生已签观察不归因为本次新增。

**M8-04 持久设置边界（schema99，2026-10-20，配置切片通过）**：签到设置是独立的版本化意图，记录明确预期UID、固定NexusPHP v1.10.3 profile、显式站点时区、每日HH:MM、enabled及来源总版本。日程语法在纯domain校验；时区名称存在性由PostgreSQL实际`pg_timezone_names`精确验证，不读取宿主时区、不使用Local/Factory/posix规则、不自动替换别名。当前固定Debian PG18.6镜像未收录PRC，需明确使用Asia/Shanghai等已注册名称；注册表只证明本系统能解释该名称，不证明对端站点实际使用该时区。配置入口限活动管理员用户会话，锁后复核当前账号状态/角色，拒绝成员、Token与system；配置、不可变原输入/hash/snapshot回执、审计和事件同事务，尾部再次检查来源/配置/回执完整性。原键恢复先于最新版本和来源密文检查，保留历史时区文本与同一时刻的原时间表示；新命令必须匹配当前设置及来源版本。读取是数据库只读操作，保存不观测、不签到、不排任务；schema100由独立worker解释设置并单独取得每日发送许可，不得把保存成功解释为已签到。

**M8-04 持久执行边界（schema100，2026-10-20，本地验收通过）**：`site_checkin_day`以来源UUID、明确UID和站点本地日为永久唯一身份；`site_checkin_execution`记录一次job租约的执行证据。prepared指针及许可不可删除、清空或重绑，唯一发送资格不因配置/来源版本、时区或租约改变而增加。首次prepare与启用设置共用来源配置聚合锁，检查该来源同一UID的全部prepared历史以冻结时区；不同UID独立，切回旧UID仍受原历史限制。PG按明确时区计算日界与HH:MM到期，不用固定24小时或宿主TZData处理DST。配置时区仍是运维声明，不是远端时区认证。
来源物理门禁与搜索/RSS/统计共用`indexer:`会话锁。预检GET单独预留共享预算；pending后在门禁内有界等待下一份预算，POST预算与发送许可同事务并经过deferred COMMIT围栏，只有明确提交成功才允许一次POST。准备前中断最多沿原job取得三次租约；prepared后任何结果都保留原资格，只允许一个新观察job读取首页，无法在冻结日内确认则保留review，不重发POST。窗口、数据库时钟、owner/token/state/有效租约、来源配置指纹和原物理会话均复核；来源健康投影不改变配置指纹，会话时区不改变同一来源指纹。取消后等待同步原生调用返回，统一退出路径等待owned worker。
管理员历史GET只读安全日台账，按`(calendar_day,id)`稳定分页，游标绑定来源；不返回Cookie、执行nonce或来源指纹，不预留预算、不建job、不请求PT。恢复隔离释放额外检查未决签到资格，job已dead也不能单凭通用说明放行。离线按DayID记录`retain_review`及当前restore ID与证据，与最终恢复审计同事务；不把未知改为成功，也不重置发送指针。已人工接管的日不再自动恢复旧观察job，后续新日继续遵守原永久身份和时区约束。

**M8-03 账号统计采集边界（2026-10-20，本地验收通过）**：首个支持范围限显式配置的传统NexusPHP数字UID与原生`table#info_block`模板，依据官方源码固定版本`xiaomlove/nexusphp@a3ba82be64e7c1cbfa72230d6b0dfb35dd3df459`的首页登录、欢迎区当前用户、`mksize`及分享率函数；不推定Torznab/RSS/passkey-only、现代UUID或定制模板可用。读取配置根`index.php`，同一可信welcome行中的唯一当前UID与logout结构须匹配明确预期账号；正文其他用户详情链接不授权切换身份。上传/下载采用站点显示十进制字符串及KB/MB/GB/TB/PB单位，保留小数位并标approximate；源码二进制单位已舍入，不换算为精确累计bytes，不使用浮点或搜索/下载器默认统计。魔力值保留一位显示十进制字符串，分享率以站点原值区分finite/infinite/undefined/unknown，不从舍入后的流量重算。缺失值保持未知，不补零。
HTTP只携带该配置的Cookie到固定根，禁止重定向/跨源和超限响应；401认证、403访问被阻、429真实有界退避、超时、坏页面及取消分别归现有ports安全错误，不持久化原HTML或低层URL错误。统计属于管理员管理的全局站点账号，采集设置与读取限当前活动管理员会话，不因公共模板可读而向成员暴露上传量/魔力值。纵向运行已复用来源物理许可与共享预算，以总来源版本、采集配置版本和原job owner/token/租约绑定提交，GET只读已持久观测；失败保留上次有效显示值及原时间并标陈旧。运行、持久化/API和最小卡片均已完成本地验收，协议范围及外部联调边界以本节为准。
schema97采集设置与不可变原回执以明确UID、固定profile、15分钟至24小时周期和来源总版本授权；schema98独立schedule/attempt不改写冻结设置。后台按全局持久设置执行，不借用保存者的旧Actor；持有与搜索/RSS/探测相同的`indexer:`会话物理锁，按原job owner/token和两分钟租约校验，单次GET前复用共享预算。采集失败、429退避及成功观测与job/下次调度同事务，过期/中断进入review_required而非自动重放。执行摘要只含来源配置/密文/协议和同版本设置回执，不将健康投影或预算刷新误当配置变更。caller取消不提前关闭持锁PG会话，仍在事务尾部拒绝取消并由App等待实际请求返回后关闭许可；使用既有2秒lock_timeout、30秒statement_timeout及提交时真实时钟/会话锁约束。采集结果不存原HTML、执行URL、Cookie或底层错误文本。
管理员`GET /api/v1/indexers/{indexer_id}/account-stats`只读已持久观测及当前授权，显式null代表尚未配置，缺值不补零；失败保留上次有效值/原时间，版本不符、过期、停用和失败标陈旧，预期账号改变时隐藏旧账号数值。认证错误前也设置private/no-store，成员及Token拒绝。站点卡片默认折叠，展开/刷新仅GET；组件以账号/角色/来源ID和版本隔离，失权、读取失败、来源切换和隐藏页面清理数值并拦截迟到响应。卡片不把保存设置、排队或读取成功当作采集完成；M8-07的设置/签到整合不在本切片扩展。本地原生三父验收已通过，真实Chrome及375/320/768/1440四宽度视觉验收通过，六图复核完成，业务写入为零，未进行真实私有PT联调。

采集设置已接入schema97两表与窄`SiteAccountStatsConfigRepository`：以来源ID为全局配置主键、明确预期数字账号/profile/900–86400秒周期，配置版本与来源总版本独立绑定；不持久化明文执行URL/Cookie。每个配置版本有不可变input/hash/snapshot原回执，保存配置、成功审计和配置事件同事务，提交前复核管理员、来源/密文配置及完整回执。配置缺失明确返回null，读取失败503；历史原键重放先查原回执，不依赖当前来源仍可解密或保持同版本，不改写旧时间表示。只读原回执限原保存账号且必须仍为活动管理员；其他活动管理员可读全局当前配置。启用需同一锁定来源明确NexusPHP协议与可解密非空Cookie、安全根URL，停用仍要求版本匹配但不依赖凭据可用。`GET/PUT /api/v1/indexers/{indexer_id}/account-stats/config`与`GET .../commands/{command_key}`均private/no-store，Token与成员拒绝；严格JSON/参数前置验证。schema97历史禁止UPDATE/DELETE/TRUNCATE及非空降级，当前配置保护删除/截断。保存只记录配置意图，不预留来源预算、不联网、不建采集job；后台采集worker按启用设置独立调度，持久观测通过前述管理员GET读取。
**M7-02 豆瓣榜单边界（2026-10-20，本地验收通过，含真实服务只读实测）**：端口`DoubanRankingProvider`仅暴露`hot_movie/hot_tv/weekly_chinese/weekly_global/top250`五个固定榜单，分别对应豆瓣`subject_collection`的`movie_hot_gaia/tv_hot/tv_chinese_best_weekly/tv_global_best_weekly/movie_top250`，以`start/count`取窗口。请求按豆瓣客户端协议带`apiKey`、`os_rom`、`_ts`（日期）与`_sig`（对`GET&<转义路径>&<日期>`做HMAC-SHA1再base64）。API key与签名secret是运行时配置（环境变量`PULSE_DOUBAN_API_KEY`/`PULSE_DOUBAN_API_SECRET`优先，其次加密设置`douban.api_key`/`douban.api_secret`），仓库不带默认值、不进日志与响应；缺失时在发起网络前返回不可用。适配器视供应商数据为不可信：禁用重定向与Cookie，响应限2MiB/100条，ID必须为数字串，年份/评分/类型严格校验（年份缺失时仅取`card_subtitle`首字段的四位年份，否则保持未知），海报只保留`img*.doubanio.com`的HTTPS图并过滤占位图，错误仅映射为`ports`哨兵，不含带签名的请求地址。`usecase/ranking`只做展示数据：豆瓣身份不当作TMDB身份，仅当同媒体类型、同年且标题/原名一致的TMDB候选唯一时才附`tmdb_id`，歧义、无年份或查询失败的条目仍可读但不可一键订阅；结果带10分钟TTL缓存，失败或含查询失败的部分结果不缓存，查询并发≤4且调用返回前等待全部worker；页数上限top250 13、热门5、周榜1。`GET /api/v1/discover/douban`沿用元数据读取权限，非法list/page在零上游I/O下返回400，上游失败或未配置凭据返回无细节503而非空列表。该读取不创建订阅、任务或媒体行。**凭据来源风险**：所用key/secret取自豆瓣Android客户端（与MoviePilot相同），不是公开授权的API凭据，由使用者承担服务条款与失效风险。

**M7-04 站点模板库边界（2026-10-20，本地验收通过）**：纯领域包 `internal/domain/indexer` 定义 24 个知名 PT/BT 站点与聚合器（Prowlarr、Jackett、M-Team、HDSky、CHDBits、TTG、HDChina、OurBits、OpenCD、SpringWood、HDHome、PTerClub、SoulVoice、Audiences、HaiDan、LemonHD、KeepFrds、DiscFan、CarPT、RedLeaves、IPTorrents、TorrentLeech、FileList、1337x），无任何外部包依赖。模板包含唯一 ID、名称、主域名、协议（torznab/nexusphp/rss）、规范默认 URL、正整数推荐分类切片、分类中英文映射列表、站点描述与认证指引。`BuiltinTemplates()` 与 `FindTemplate()` 均执行切片与映射的防御性拷贝，防止调用方污染全局模板数据。`usecase/search.ListTemplates` 仅允许管理员或成员用户会话读取，禁止匿名、非用户类型或持有范围的 Token 访问。HTTP 端点 `GET /api/v1/indexer-templates` 遵循 OpenAPI 规范输出 `IndexerTemplateListResponse`，未授权返回 401，越权返回 403。前端站点管理面板在管理员表单中提供分组下拉列表（按 Torznab / NexusPHP / RSS 分组展示），选择模板即时自动填充站点名称、协议、默认 URL 及推荐分类代码（若处于编辑模式且勾选保留地址，则不覆盖原地址），并展示该模板推荐分类编号对应的含义标签及认证提示。模板仅作表单填写助手，实际站点保存仍走既有 `POST /api/v1/indexers` 和受控加密入库与审计。

**M7-05 发现页豆瓣榜单流与多源切换边界（2026-10-20，本地验收通过）**：发现页提供 TMDB 影视流与豆瓣精选榜两级数据源切换。当处于豆瓣源时，类别映射到 `GET /api/v1/discover/douban` 的 5 个专用榜单（`hot_movie`、`hot_tv`、`weekly_chinese`、`weekly_global`、`top250`），条目转换为包含 `rating_source: "douban"`、`douban_id`、`tmdb_id` 的 `MediaItem`。卡片组件以专用绿色徽标展示豆瓣评分（`.badge-rating.douban`，`X.X 豆瓣`）。即览即订规则：仅当条目在用例层建立并返回唯一确定的正整型 `tmdb_id` 时，激活一键订阅按钮；未匹配 TMDB 身份的条目保持展示和检索功能，但一键订阅按钮明确禁用并提示先搜种，防止向订阅用例提交无效或猜测的身份。两数据源间切换即时隔离并中止旧请求，不交叉污染当前渲染列表。

**M7-06 实时日志流边界（2026-10-20，本地验收通过）**：`GET /api/v1/system/logs` 是只读的 text/event-stream 端点，仅当前管理员用户会话可读；匿名返回401，成员或非用户主体返回403，未装配 hub 返回503。每条记录为 `event: log` 加 `{at,level,line}`，客户端可用 `level` 查询参数选择 DEBUG/INFO/WARN/ERROR 下限（未知值回退 INFO，不得因畸形过滤放宽可见性），连接时回放最多300条并每15秒发送保活注释。回放前及持续推送期间至少每秒复核原会话、活动账户和管理员角色，单次复核期限1秒；会话撤销/过期、账户禁用/降权或读取失败即关闭，取消后释放订阅。权限变更至关闭存在最长约2秒的复核窗口，默认HTTP写超时不作为授权依据。日志进入 hub 的唯一路径是 `logging.WithSink`：它在既有 RedactingHandler 之外包一层 tee，sink 只收到经 RedactString 处理过的单行文本，因此无论 App 是否由调用方注入自定义 logger，凭据、令牌、下载链接与 DSN 都不会进入浏览器；App 在创建 HTTP 路由前建立 hub 并把 logger 镜像进去。日志发布只写日志环形缓冲并推给日志订阅者，不触发活动失效，活动流仍只承载 reset 提示。前端 `LiveLogPanel` 在浏览器内存中持有最多500行，切换等级会重连并重过滤当前缓冲，暂停只停止追加而不断开连接，滚动仅在用户已处于底部时粘底，清空只清本地视图；页面不读写 localStorage/sessionStorage，也不做任何写请求。

**身份否定继续修复（2026-10-10，movie-candidate-v3）**：TV 编号解释同 RR 使用当前已绑定 TMDB series ID（内部字段不序列化）；任何明确 foreign/multiple/overflow 标记否定对应，匹配标记仍仅 coverage-only、execution_allowed=false。电影匹配同时检查全部 TMDB/IMDb 显式 ID，以及已知目标年和资源年的矛盾，不能由第一个匹配 ID 遮住后续冲突；候选规则 v3 失效旧规则缓存。纯独立反例、真实 TV 映射 HTTP/PG 与独立 PG18.6 全量及关键 race 通过，原样本标签不变；不因此开放 TV 获取。

**持久 TV 候选解释实际切片（2026-10-10）**：现 `explain-release` 请求必须 `name` 或 `candidate_id` 二选一。name 仍只是输入实验；UUID 分支从真实 release_candidate 读取，同单 RR 查询当前绑定、源 config_version、online 健康及30分钟内成功 indexer_query/QC 关联、已批编号映射与标准 episodeID/UUID/version；未绑定/旧配置/过期来源清覆盖。标准 mode 只接受清晰一集且 canonical provider episodeID 唯一，退休/未知/多集/绝对号/片段不猜；已批 absolute/manual 继续原不可变对应。独立质量来自保存 Profile+当前订阅不可变策略版本，缺事实hold，不靠标题1080p升品质；目标复用同 RR 有效拥有/日期/skip/根/占位投影规则，不读旧绿色。普通候选摘要含原来源/密文sha/品质/全部canonical单元和完整目标输入，观测时钟不作版本；公开不含密文/链接/原始过滤词，只静态原因。真实PG/HTTP验证严格XOR、成员拒、未知UUID、private no-store、配置/TTL/绑定失效、标准provider正例/退役、unknown→known品质改变摘要、skip保留编号但拒目标，零远程重查/零job。独立PG18.6全量/关键race和生成零漂移通过。通知32条容量夹具与真实3秒窗口解耦，固定新fixture期限，不修改生产窗口或已冻deadline；32必封存、33rd逐收件人新桶断言保持。以上只解释，execution_allowed恒false，未开放TV任务/文件操作；作品身份审核、最终锁内提交/发送/整理围栏、UI实际候选入口仍待。

每步稳定 op_key；prepared → started（先持久化）→ 执行 → 校验 → succeeded。文件发布结果、imported 事实、文件索引、需求完成及事件按短事务提交；执行权已失效则回滚，留待观察恢复。正常入库与原计划恢复都在文件检查前取得数据库观察时间，末尾提交再次按数据库当前时间验证五秒有效窗口，等锁或写入不能刷新观察凭据。原计划预览、确认及回执读取先在短事务锁定当前活动用户，随后释放事务执行文件观察；提交时再次核验当前用户和原冻结版本。旧同步批准入口不能绕过任务/计划租约直接执行。App.Run重启扫描可能先登记已创建的目标硬链接；文件观察先验证device/inode后，整理入库仅在根锁内复用inode指纹、size与mtime完全一致、未退休且无单元映射的扫描记录。文件变更水位、映射及imported事实同事务提交；身份不一致或已有业务映射转复核，不能借用路径相同的文件。

单电影或已审核同单集 hardlink 的人工目标修订生成后继冻结计划，原计划、步骤、品质原文及来源证据不改写。入口仅允许当前管理员操作本人在原根/单元上的 pending 需求；原整理任务须已结束，旧计划不再执行，源身份仍与冻结证据一致，原目标与新目标均明确 NotExist。相对目标须规范化且扩展名不变，不覆盖现存或无法访问的目标；已有原硬链接继续使用原计划恢复。受控单集沿原策略/订阅→根→单元→获取范围锁序复核编号映射、来源与完整单视频 Inventory，新目标只允许保留原 canonical 季集键；标准编号与已批准绝对号来源适用同一围栏，换集号、多集及绝对号目标不能提交。只读预览显示原/新相对路径、源大小与原单集键，观察在文件 I/O 前取得数据库时间；文件 I/O 在事务外，提交按既有根→单元→获取范围→任务/计划锁序复核，前后均核验五秒观察窗口。后继计划/步骤、原下载任务的计划绑定、新整理 job、审计、事件及幂等回执原子提交；202 仅表示排队，实际文件发布由既有租约与物理根槽保护的 worker 完成。修订 job 的 revision_plan_id 必须匹配任务当前计划，不能借用旧 job 执行新计划。成功重放先校验当前用户并恢复原回执，不重新创建计划。界面按用户与原 operation 保存未确认命令，冻结原 plan ID、目标、摘要和幂等键，后继计划出现在活动查询后仍能恢复同一命令；修改目标立即清除旧预览，公开预览和回执为 private, no-store。


单单元升级退休旧文件前，必须在旧文件行锁下确认该文件恰好只映射本次替换单元；同一文件还关联其它单元时，不能整文件退休或撤销全部历史事实，须保留原文件与事实并转全集合人工复核。该围栏在实际文件操作前和入库事务中均复核，不以其中一个映射匹配代替完整集合检查；它本身不授予升级执行权限，当前策略、品质证据和冻结升级授权仍须独立成立。

默认同文件系统硬链接；reflink 需能力探测，copy/move 显式开启。不覆盖冲突，默认 skip，另有 keep_both/replace_if_better；新文件经验证入库后，旧版本移入回收目录并提交退休，默认保留 7 天。硬链接不原地改写内容，避免破坏下载源/保种。

恢复充分后置条件：hardlink 比对 device/inode；copy/reflink 检验完整内容或可靠操作凭据；mkdir/recycle/sidecar 分别定义判据。大小或部分哈希只是筛选，不是完成证明。目标缺失且源完整、旧执行已结束时才重做；无法访问/冲突/不确定转 manual_review。

**显式文件动作与完整内容证据（schema81）**：受控单电影的人工目标修订可明确选择 hardlink/copy/reflink/move，省略动作仍保持原硬链接合同；原计划、步骤和历史批准证据不变，后继计划与确认动作、原账号和源根物理身份共同冻结。reflink 在事务外探测实际源/目标能力，失败不回退 copy；单集沿用原已审核编号与硬链接路径，不能凭动作字段绕过其独立冻结授权。预览结束不产生拥有，确认仍同事务校验当前账号/需求、原计划/文件观测与材料戳，排队、审计、事件及幂等回执一起提交。界面动作或目标变化清除预览；丢响应后按用户、原 operation、原动作/body/key 恢复原回执，兼容旧省略 action 的未确认命令。

完整 SHA-256 在受控 worker 持有两根物理槽位期间计算，并在发布前首次封存到独立 `transfer_content_evidence`；该表按原 step 绑定，禁止 UPDATE/DELETE/TRUNCATE，不能补写原不可变 target_evidence。文件写入按 step UUID 隔离的独占临时文件，完整校验和必要 fsync 后无覆盖发布；中断阶段文件解绑后独占重建，禁止截断可能关联源文件的硬链接。copy/reflink 要求完整源与目标内容一致；move 另需确认原路径缺失、私有隔离源已安全清理，删除前再次核对完整源内容。文件索引与事实使用目标实际 inode/size/mtime，不能借用源身份。长操作按 owner/token 续租，取消/失租后等待真实 I/O 返回才释放根槽位。空间不足通过 ports.ErrFileNoSpace 保留原步骤和摘要以供后续重验恢复；身份冲突、内容变化及不确定结果转人工。完整内容观察在事务外，提交事务再次核验当前授权、根/单元/版本与观察戳；新文件、映射、imported 事实、审计和标准入库事件原子提交。已有内容或动作历史拒绝 schema81 降级，空迁移可往返。真实私有挂载与 PG 验证 EXDEV copy/move 及 ENOSPC 原步骤恢复；能力不足的平台/卷保持不可用，实际 NAS 能力及生产集成归 M4 验收。

### 7.5 物理独占与路径安全

每根一个保守物理操作队列，扫描和整理默认串行；涉及多个根按稳定物理根身份（device/inode）去重并固定顺序取物理槽位，避免同一目录的不同逻辑根/别名导致死锁。root ID 仍用于业务根与权限范围标识。逻辑 context 超时后槽位/路径独占保持到系统调用实际结束或确认旧进程退出；根 stalled 暂停新操作，不能依赖租约过期启动第二个写入者。

源与目标均限定已配置根；任务请求/下载器观察到的 save_path 不能自行授予本地文件能力。远程路径先按显式配置映射为本地根，再约束其相对路径。目标根需探测 marker，并冻结/复核目录物理身份，防止把卸载后的空挂载点当作媒体库。硬链接后的检查须重新打开配置路径，核对当前目标根、marker及源/目标文件的冻结身份；仅校验旧目录FD不能证明该目录仍占据原路径。命名空间或文件变化则保留原物理结果并转人工，不提交入库事实。用 ports 定义的根能力/目录 FD 合同防穿越、符号链接逃逸和 TOCTOU；不支持该能力的平台返回不可用，不回退至不受控绝对路径。复制写临时文件、校验、原子发布，按持久性边界 fsync；R3 写入由受控 root worker 执行。

**目录变更提示（M8-05）**：Linux inotify 仅提供重新观察的提示，不证明文件写入完成、可信下载清单、拥有、缺失或整理权限。监听范围必须来自校验完整的本地配置投影与已固定的根能力，不从下载器返回的任意路径授予权限；监听期间保持根物理身份，并重新打开当前配置路径核对，不能仅依赖旧FD。监听不终身占用扫描/整理的物理独占槽，生命周期由App持有并取消、等待实际读取结束。初始化及新建/移入子目录需有界递归注册，队列饱和、超额、事件溢出、目录移走、卸载或根替换均显式结束覆盖，不能静默丢失后宣称完整，更不能据此制造文件缺失。

即时提速只能触发现有受控观察：每条路径保留下载器身份、完整字节/文件清单、预算、原授权及有效执行许可；完成事务成功后才能唤醒已持久排队的整理任务。目录提示不能唤醒带首次Add或R3发送权限的通用提交循环，不能改写不可变观察/回执时间来绕过专用TV/升级的调度合同。未关联合法任务或目标意图的外部文件须经扫描、识别及确认，不凭关闭/重命名猜测身份自动入库。验收须分别记录事件接收、观察调度、提交和入库时延，不能把原生事件毫秒级到达表述为数据库与文件I/O的固定时延；远端NAS写入不保证产生本机inotify事件，不将本地Linux验证冒充NAS即时通知。

NFS、SMB等远端文件系统不承诺远端写入的本机通知，原生监听明确返回不可用，外部目录发现沿原周期扫描继续，不能伪造事件或将发现当作完成证明。文件整理仍须固定物理根、明确身份确认、完整内容摘要及独立永久许可；私有暂存目录和文件必须满足当前UID与0700/0600权限。目标发布及move源隔离使用原子不覆盖重命名；若文件系统返回EINVAL、ENOSYS或EOPNOTSUPP，保留原错误并归为文件能力不支持，不回退到会覆盖目标的重命名。已有永久许可的重试仅观察后置条件，不能再次复制、发布或删除源；失败保留源、许可和原回执，不能发布入库事实。真实NFS41已验证远端写入经原60秒周期发现、明确确认后hardlink入库；本次NFS不支持原子NoReplace，copy/move验证保源无目标无入库及有限只读恢复，不声明其成功整理或NAS即时通知。

当前普通下载路径复用schema100，不新增监听任务框架或数据库迁移。监听清单只投影已有合法普通tracking需求对应的配置根/完整本地路径映射，目标库根须有当前generation最早成功完成扫描的物理基线；映射全量校验、最长完整前缀匹配后生成配置绑定，缺基线、非法映射或恢复隔离时不授予监听能力。来源与任务分别使用keyset分页，提示游标绑定来源、提示种类及相对路径；有界续跑不会用配置刷新替代下载器轮询。每次网络读取前后及写回事务提交前复核同一来源/需求/元数据绑定，历史NULL提交fence须有原operation/unit/candidate/task/root代次匹配、非冲突且未释放的冻结获取占位与source_stamp，不能将任意空fence解释为授权；获取占位的完整身份和保护状态也纳入绑定摘要。正常进度不改变授权摘要；本次网络观察另以任务版本防止旧结果覆盖更新。共享完成事务仍唯一排队整理，成功后仅唤醒原整理消费者。监听控制器与实际原生读取/下载器调用均由App取消并等待，持有上下文的状态留在运行函数局部。专用TV、整包及升级不进入这一普通入口；未关联合法任务的外部文件不因事件自动取得导入权限。

整包TV使用独立的schema101提示证据与专用tracking入口，不进入普通观察器。只有已成功部分观察的精确前驱才可被新的close_write/moved_to提示提前唤醒；来源清单先取得DB时刻，App在串行转发时冻结监听实例、序号、本机接收时刻、清单时刻和源物理身份，分页不得刷新或将旧提示重绑到更新前驱。源映射、当前generation最早完成扫描的基线、任务/原准备路径及完整原授权均须匹配；Rescan只作普通受控重协调，不授予提前许可。提示证据与原格式tracking job同事务发布，每个operation/前驱至多一个提示后继，持久化后不可删除、重绑或借重试增加机会；不改写前驱、回执或周期调度时间。隔离围栏先于账户锁，事件根先于冻结单元锁；原物理会话/permit与下载器预算保留。提示摘要加入原seal，网络每次读取前后在事务外重开配置根及marker并核对当前物理身份，任务完成与延迟约束执行后复核同一证据及完整保护。App只唤醒tracking认领，不执行提交或R3恢复，取消后等待实际I/O返回。正常周期任务保留原30秒门槛；整包完成仍为review_required，不因提示授权整理、释放占位或发布入库事实。自动TV现有next_poll_at为2秒，升级的30秒门槛另受数据库与持久claim约束，均不得套用整包提示例外。

自动TV沿用schema101与原有观察任务，不新增事件表或复用整包的提前例外。来源须已有成功且不需复核的partial前驱，DB清单时刻、原准备文件路径、完整映射、当前根代次与最早成功扫描物理基线共同限定监听范围。仓储签发不可伪造且限定本实例的capture，冻结原operation/task/send attempt、精确前驱、授权/准备/保护摘要和原next_poll_at；Rescan不签发执行机会。未到期只返回剩余时长，调用者以可取消timer等待后提交同一capture，不能重新取前驱或改写created_at/next_poll_at。精确调度与周期scheduler共用原preparation互斥，在原2秒门槛之后才排入原三键payload的观察job；入队前、延迟约束执行后复核完整资格、历史与保护，活动物理执行或后继/终态使旧capture失效。网络仍由原专用observation consumer持原租约、物理permit和预算执行；只有原完成事务成功且无需复核时唤醒既有整理消费者，事件不授予Add、R3重发或入库权限。

电影升级只用目录提示精确选择原30秒门槛之后的正常tracking工作，不套用整包提前许可，沿用schema101。来源须有confirmed发送和成功tracking的精确前驱；监听与capture绑定当前配置、完整路径映射、原准备文件路径、DB清单时刻、当前根代次最早完成扫描基线、原授权和三个占位/旧文件事实。40秒有效期和DB截止时刻签发后固定，未到期只返回DB剩余时长；调用方在独立35秒观察预算内可取消等待，累计等待不超过原30秒且不换前驱。每次排队尝试先只读验证私有同实例capture，再在事务外重开源/目标根并检查物理身份和marker。精确排队共享原周期入队事务与单键payload，隔离锁先于账户/业务锁，原授权加载后和延迟约束执行后复核同一capture、完整原图及本次新增job。保留原排队、admission和数据库30秒断言，物理会话只try-lock，不持业务锁等待物理槽。App提示仅唤醒tracking认领，原网络执行仍受原租约、物理许可和预算约束；完成仍为organization_review_required，不授权整理、释放占位、退役或回收。此路径只减少到期后的额外周期扫描等待，不承诺提前30秒或毫秒级入库。

**未跟踪目录的显式配置与确认边界（schema102）**：管理员会话可保存账户所有的版本化目录范围，明确选择已配置下载器的远程映射及目标库根；完整映射、配置版本/摘要、当前根代次最早成功扫描基线与marker均须有效。来源独立探测物理身份，不能借用目标身份；源、目标在事务外打开并重新核对当前配置路径。仓储签发仅本实例可用的15秒capture，提交按稳定ID锁住目标及与源路径双向重叠的库根，复核当前账户/对象权限、原配置与基线；配置、不可变原键回执、审计和事件在同一事务提交，延迟约束后再次校验。原键重放先验权，只恢复原回执且不探测文件系统；同选择停用保留已冻结的源/目标基线，不依赖在线文件系统。设置请求不能自行提供本地路径或物理身份，也不触发下载器、发现任务、整理或入库，响应的execution_allowed始终false。

曾启用范围的历史回执构成永久确认边界，后续停用、改映射或改目录不能将旧范围的文件重新授权给遗留自动识别链。配置提交与旧识别按同一库根锁顺序串行；旧movie_recognition排队前及MovieLink写回事务的根锁后、提交尾都检查历史范围，受管文件不得直接生成imported事实。无scope历史表的旧schema不引用新表，维持原识别合同；有历史时禁止删除/改写/截断和破坏性降级。schema102只提交配置和永久边界；启用范围的发现由后继schema103受控运行时异步执行。识别建议、明确确认和首次外部整理仍须独立实现，不能用文件关闭事件、文件名或匹配首项替代授权。
**未跟踪目录的持久发现（schema103）**：复用已有监听控制器和job队列；目标以显式scope ID/版本分派，禁止伪造download operation/task。scope绑定额外冻结来源物理身份，新增零值字段不改变既有下载绑定的JSON字节及历史摘要。只投影当前有效、启用且所属管理员仍有权限的范围，清单分页固定DB时刻和范围版本。监听提示、初始化重扫和60秒周期全扫只请求同一范围的受控发现；持久dirty sequence合并活动任务，扫描期间的新请求由完成事务原子排入后继，重启使用有限R0恢复，旧租约回调不得写回。

发现用例持有来源与目标的FD物理槽，取消、超时或续租失败后等实际系统调用返回才释放；每批最多128条正向视频观察，复用纯视频格式规则跳过NFO、字幕等文件。批次写入前重新打开当前配置路径，检查独立来源基线、目标代次/最早扫描基线和marker，复核逐文件device/inode/size/mtime及当前租约/配置/账户；目录size/mtime自然变化不使物理身份失效。幂等采样只更新持久观测和版本，批次及任务完成在延迟约束后再校验；遍历中断保留已提交正向观测而不封存完整覆盖。事件丢失、离线、部分遍历、根替换或空列表不制造缺失、不删除旧观测，也不创建library_file、映射、Wanted、整理任务或imported事实。

管理员Cookie通过`GET /api/v1/library/untracked-scopes/{scope_id}/discoveries`只读分页查询自身范围的持久观测；GET不访问文件系统、不调用下载器、不调度任务，使用private/no-store。观察展示原scope版本、文件revision、身份及观察时间/次数；查询页耗尽不等于目录清单完整，多次观察不证明写入结束。`inventory_complete`与`execution_allowed`始终false，确认或整理必须另行冻结并复核真实媒体身份、目标意图、当前权限、文件和根证据。

**未跟踪文件识别建议与明确确认（schema104）**：管理员Cookie显式请求识别，冻结范围版本、观察文件revision、媒体类型及可选明确TMDB ID，并以原键将请求、不可变回执、审计、事件和唯一`untracked_review`任务同事务保存；目录发现或GET不自动请求元数据。复用纯文件名解析和带完整性标记的TMDB身份搜索，自动建议只接受完整、唯一且详情一致的同类型身份；不完整、歧义、未知年份和未证明覆盖保留`review_required`，不取搜索首项。明确ID仍须读取精确typed详情；电影与TV命名空间不得混用，TV仅接受真实完整季元数据中的明确标准集号，保留稀疏编号、episode ID和未知日期；未知播出日期保存为null，已知日期须为合法年份的UTC午夜并独立复制，外部识别不据此授予自动获取资格，不从集数补造单元。可确认结果为`ready`且证据reason为`ready`；队列任务本身仍为`succeeded`，`dead`只读展示且不能携带候选。证据绑定解析版本、原文件稳定身份、范围与真实覆盖；常规观测次数和时间不改变材料摘要，文件revision或配置变化使旧建议失效。

识别worker在现有受控生命周期内持有两根FD物理槽；文件与根命名空间在网络前后复核，元数据I/O在事务外，固定原DB租约截止，撤权、停用、版本变化或取消后的迟到结果不能提交。确认先在事务中重验当前主体并恢复原键回执；新确认只引用已持久化的候选key/材料摘要、范围和文件版本，冻结明确操作方式和规范目标相对路径，事务外独立探测源、目标与文件。仅本实例私有的15秒确认capture在提交时再次检查当前权限、配置、根基线/marker、文件证据及原材料，延迟约束执行后复核完整账本。确认、不可变原回执、审计和事件同事务保存，状态仅为`confirmed_pending_organization`、`execution_allowed=false`；不创建整理任务、下载、媒体单元、Wanted、映射或imported事实。历史原键恢复在源离线后仍不访问FS或元数据，先验当前权限再恢复原内容；保存历史的表禁止改写、删除、截断和破坏性降级。外部整理须另接独立授权与执行合同，不能将确认回执当作物理执行许可。

**外部文件显式规范季集**：作用域列表只返回当前管理员自己的配置；页面先选择受控目录和已观察文件。无法靠文件名确定规范季集时，可显式选择已有作品的真实季、episode ID及canonical unit；请求冻结文件revision、作品/单元元数据版本、规范身份和季集组合，旧自动识别输入继续兼容。schema138允许已具备真实标准季集身份、同时带有补充绝对集号属性的单元按显式规范映射整理；Go校验与数据库提交守卫都核对原规范UUID、作品/单元版本、季集及TMDB集身份，不从绝对集号推导映射。自动识别仍拒绝这类单元；绝对集号变化同样推进元数据版本并使旧确认失效。存在这类整理历史时拒绝降级回旧守卫。识别、确认、整理准备及排队均复核当前规范证据，版本变化不能借旧确认创建新计划；历史原键只读恢复原结果。整理书签只保存账号绑定的定位ID，刷新后显式GET恢复；原回执明确404后才提供原键提交，不自动重发、换键或把迟到响应交给另一账号。

**同目录剧集批量确认（schema140）**：批次最多20个已观察文件，限定同一受控目录、作品及季；各行仍显式选择真实规范集、操作和目标，分别冻结已有识别证据、文件revision与作品/单元版本。同文件、物理inode别名、重复规范集/目标、歧义或不完整覆盖拒绝，不按行号或文件数量猜集号。创建不可变批次、审计与原键回执同事务，仅保存清单，不授予执行；按行分别确认与排队，复用原确认和整理事务及永久许可。每行使用批次绑定的固定键，过期行不影响其它当前有效行；GET汇总原确认、整理和完成回执，不重发。真实HTTP仅返回现有scope、文件观察、复核和整理公开合同，不暴露存储快照的内部布局。页面在提交前持久化原清单键，未知响应刷新后仅读原回执，账号/角色切换隔离迟到结果。


**整理过滤与排除（M8-06）**：路径分类与实测大小分开，扩展名、隐藏路径及完整词边界的sample/trailer/preview等标记属于纯规则；Sampler等包含子串的正常文件不得误杀。`organization.min_media_bytes`/`PULSE_ORGANIZATION_MIN_MEDIA_BYTES`是进程启动时读取的正整数字节下限，部署默认1字节以延续既有小文件行为；阈值包含边界，零字节、负值或尚未测量的大小不能当作合格。生产显式传部署值，不使用纯规则零参数的200MiB缺省代替已部署配置。过滤决定是否进入识别/整理，不删除扫描库存、不制造缺失、不撤销既有映射或事实。电影目录先校验完整附件路径、总字节、唯一性和完成度，再只选择唯一合格正片；被排除附件仍保留在原证据里，多正片歧义或部分附件不能通过过滤被掩盖。

扫描遗留识别在排队、元数据前和Link事务当前快照上同规则校验；排队从锁定根和文件的稳定ID分页，跳过前排排除项不会饿死后续正片，每页/每个产生任务的根仍最多25条，不给排除项伪造job或识别历史。普通、自动TV、完整包和独立外部整理在首次计划或物理发布前校验实际文件大小；多单元文件和整包不能静默删掉一个被排除正片的覆盖或消费其Wanted/占位。新复用确认同时校验源和目标相对路径，预览仅隐藏不可执行候选，不改质量事实或已拥有状态。历史原键先重验权限、恢复完整原回执，之后才处理新请求的当前过滤规则。规则收紧后，未发布的pending/running计划不得再发布；已有物理结果只能按原冻结身份/内容观察，硬链接须同inode，copy/reflink/move须原完整内容及动作后置条件，move还须源已移除；没有旧内容证据不能新授内容许可。外部永久许可和所有现有权限/根/租约/事务尾校验继续有效。

**未跟踪文件独立整理（schema105）**：管理员Cookie以不可变确认ID显式提交整理命令；确认回执及其`confirmed_pending_organization`、`execution_allowed=false`保持原内容。新操作有独立operation/plan/step/job和完整材料摘要，不能将新摘要等同旧确认摘要；摘要规范化时间并排除常规观察计数，仍绑定当前来源、目标、精确候选及覆盖。新请求在事务外持有两根FD物理槽复核源文件，在同一事务创建真实外部操作、精确canonical单元快照、一个物理步骤、原键排队回执、审计与事件，不构造download requirement、下载task或候选。原键恢复先验当前主体并校验原始完整账本，不重新探测离线目录或请求元数据。目标路径同时受遗留识别器的排队前和事务内保护，避免物理发布后、最终写回前被另行导入。

**同目录外挂字幕随片整理（schema136）**：视频整理完成后，管理员可显式发现并勾选与原视频唯一匹配的SRT/ASS；目录枚举有界，拒绝符号链接、歧义视频和被替换的源视频。原视频已移动时仍按冻结的原目录/名称查找附件。每个附件单独冻结源身份、目标名及复制/硬链接方式，使用完整内容证明、根槽、唯一持久许可与无覆盖发布；始终保留源字幕。附件逐项记录成功、冲突或待复核，不改变已入库视频结果。取消等待真实调用返回；重启、失联或缺少原内容许可均不能再获得一次物理发布机会，GET只读恢复，显式执行也不得盲目重做。

**下载器显式分流（M9-01）**：规则只影响新自动选择，不由名称、显示文本或列表顺序推断。策略v1手选优先；自动匹配按站点与媒体同时命中、站点、媒体、默认依次取更具体规则，同级不同目标拒绝，命中目标不可用不降级另发；仅一个合格端点时可无歧义回退。部署`download_routing.rules`/`PULSE_DOWNLOAD_ROUTE_RULES`提供最多64条明确media_type/indexer_id/downloader_id规则；`download_routing.enabled`/`PULSE_DOWNLOAD_ROUTING_ENABLED`显式启停，未指定开关且无规则时保持旧路径，显式启用且空规则时仅允许单合格端点回退。环境JSON拒绝未知字段/尾数据/无效ID及重复scope，错误不回显输入。新电影scheduled轮次在当前有效来源、订阅/根锁下，按每源冻结选择ID、算法版本及数据库配置摘要，同事务落轮次；候选选定后取该来源冻结结果，预约/占位/需求/发送job与成功审计沿原子路径提交并复核下载器摘要。新字段omitempty保持旧/手选快照JSON；冻结后部署规则变化不重定向旧轮次，发送与恢复不得再猜选。自动TV保留原v1单client授权图，仅新round、显式启用且尚未绑定时，在原owner/policy/subscription/root/unit/Wanted/indexer锁后选择；有效来源须收敛同一目标，否则不绑定。自动绑定、订阅意图+1、全部Wanted意图刷新、独立system成功审计/事件与新轮次同事务，旧手选和历史原键回执字节保留；后续Load/Finalize/发送/R3继续原sub.downloader_id=client_id及快照摘要约束，不重新选。TV源首页及未保存的后续页均先在事务和物理许可外，按原轮次deadline进行可取消的共享预算等待；实际admission仍复核原owner/token/有效租约、当前配置与物理独占，不因等待授予发送或重放许可。


**rTorrent原生普通下载（M11-04，schema119）**：第三下载器通过HTTP(S) XML-RPC接入既有配置、普通下载工厂和首授权冻结路径；端点认证、密文及CAS不形成执行授权。原始种子以base64二进制参数加载，保存目录与独立`pulsemedia.tags`在同次RPC设置；不拼接shell命令、不跟随重定向、不重试有副作用请求。解析器限制响应字节、深度、节点及请求累计体积，检查fault与完整类型，不回显远端错误或凭据。列表必须完整且hash唯一，路径/完成/字节事实不明不授予入库；不存在只由成功的完整列表证明。普通发送仍先持久化原下载器摘要、任务及租约证据，丢失回执仅观察原任务；端口移除仅`d.erase`保留文件，拒绝删除文件。自动选择仅将rTorrent列入普通电影候选，自动TV/整季/升级、全局速率及自动做种移除继续各自能力合同，不能以工厂可构造替代专用证据和授权。迁移回退不得抹除第三类型配置；设置页只显示安全摘要并沿原版本冲突与明确连接测试控制。

执行消费者只认领`untracked_organization`；最多三次有限任务认领不等于三次物理执行机会。首次写入前在根槽内计算完整内容摘要，重新验证配置/权限/文件/根、job owner/token/state/有效租约与单元元数据，再事务性保存不可清除或重绑的永久物理许可和独立内容证据。已有许可的后续租约只核验原动作后置条件，禁止再次发布文件；硬链接核对同inode及完整内容，copy/reflink/move使用既有FD传输和只读观察，move还须证明源路径已移除。根替换、内容变化、权限或配置失效、已有不相关目标及结果不明都不能凭重试获得覆盖或入库权限；取消、超时和续租失败仍等待实际I/O返回再释放物理槽。

最终观察、目标library_file、全部精确映射/imported事实、成功审计、`media.imported`与独立sidecar/subtitle后续任务、原plan/step和job完成在同一窄事务提交，延迟约束后再次复核完整账本。一个稀疏多集视频只有一个物理步骤，多个真实episode单元同事务关联；不生成Wanted或下载，不消费其他业务的需求、占位、任务或入库事实。物理目标已发布但事务回滚时保留原许可和执行身份，由新观察租约验证原目标后恢复提交；不能清除许可重写文件。五类外部账本及冻结plan/step字段受不可变、完整图和历史降级保护；排队原回执不随完成状态改写，状态通过独立只读API查询。

<a id="database"></a>
## 8. PostgreSQL 数据模型

### 8.1 原生类型与关系规则

唯一数据库为 PostgreSQL 18，当前开发/CI 精确版本由 [.mise.toml](../.mise.toml) 的 vars.postgres_version 固定为 18.6；无其它数据库方言或兼容层。使用 pgx/v5、pgxpool、sqlc postgresql、goose。

| 类型 | 用途 |
|---|---|
| UUID | 内部主外键；UUIDv7 由 Go 注入 IDGen 生成 |
| TIMESTAMPTZ | 观察、创建、租约与到期时间；会话 UTC |
| DATE | 仅日期的播出信息；API 为 YYYY-MM-DD |
| BIGINT | 大小、序列、版本、令牌与 mtime_ns |
| JSONB | 版本化策略、快照、结构化属性与进度 |
| BYTEA | 密文、哈希、字节缓存 |
| BOOLEAN / TEXT + CHECK | 开关与可演进状态；外部 ID 为 TEXT |

身份、外键、状态、版本、查询索引字段显式建列，不藏进 JSONB。历史被引用数据优先归档/失效，不能用级联删除抹掉未决外部动作与审计。

### 8.2 核心表合同

表名与关键字段用于指导迁移，不保留未发布的全量 DDL 副本。

| 表/关系 | 关键合同 |
|---|---|
| app_user / app_session / api_token | 用户名 lower 唯一；凭据仅哈希；会话/Token 归属用户，过期/禁用可检查 |
| audit_log / idempotency_record | Actor、动作、目标、结果；actor identity + key 唯一，请求摘要与响应/资源指针 |
| setting / policy / policy_revision | 配置 version；策略 current_revision；immutable `(policy_id, revision)` |
| media / media_identity / media_unit | 外部身份唯一；`(media_id, unit_key)` 唯一；retired、播出信息、metadata_version/facts_version |
| metadata_cache | provider/resource/lang 键、ETag/TTL、强类型解码 |
| subscription / subscription_indexer | 意图/控制 version、目标根与策略 FK；indexer_mode=all/selected，selected 有关联 FK；可选 download_client_id |
| wanted_item | subscription/unit 唯一；skipped 与版本化进度投影 |
| library_root | path、身份/挂载证据、健康/health_revision、baseline、generation、scan_token、mutation_seq、命名与能力 |
| library_root_server | root/server/library ID 与路径映射，决定刷新目标 |
| library_file / library_file_unit | 文件 incarnation、root/相对路径、presence 与 match_state 分开、质量属性、内容证据、变更水位/缺失观察；多单元映射 |
| unit_fact | unit/stage、task/file FK、观察/到期/失效；来源阶段 CHECK；Schema 47 增加 `source_task_id → download_task(id)` 与 `(source_file_id, unit_id) → library_file_unit(file_id, unit_id)` 的可延迟 FK，提交前须完成来源与映射；NOT VALID 保留既有历史行但不认证旧来源，新/变更的来源键受约束；有事实引用的映射保留历史并以 retired_at 失效，读侧仍校验当前映射 |
| scan_run / scan_observation | run 的根身份/令牌/水位、状态/完成证据/游标；逐路径/文件观察与针对缺失的证据 |
| indexer / indexer_health | 加密配置、config_revision、端点预算、健康与 not_before |
| search_round | 订阅/策略/配置版本、目标快照、deadline、决策状态 |
| query_execution / query_page / query_demand | 活动执行去重；execution/page 唯一；round/execution 多对多 |
| release / release_unit / evaluation / release_block | guid 唯一、加密下载引用、解析/候选版本、覆盖、版本化规则结论与拒绝原因 |
| download_client / download_task | 配置与映射；稳定 operation ID、候选/覆盖快照、规范化 infohash、提交状态、外部绑定生命周期 |
| download_task_unit / download_requirement | 获取覆盖；多个订阅或手动命令到目标 root/unit 的需求及终态 |
| acquisition_reservation | task/unit/lane、released_at；业务排他而非短租约 |
| transfer_plan / transfer_plan_policy / transfer_step | 冻结文档、显式策略 revision 引用、步骤/op_key、started/后置条件/结果 |
| media_server / notification_channel | 类型与加密配置、启用状态；消费者身份细到配置实例 |
| job | kind/payload/dedupe、R1–R3、状态/退避/attempts、owner/token/lease、进度/终态 |
| outbox_event / outbox_delivery | event UUID 与 identity seq；aggregate；consumer 独立状态/租约/重试 |

选 all 时取已启用且健康的索引器；selected 不隐式扩张。显式选择不可用下载器时等待/提示，不把 unknown 操作换到另一个下载器。配置/路径映射变更增加版本，冻结操作不暗中套用新映射。

下载器和媒体服务器的 Web 管理由 `EndpointConfigurationRepository` 承担窄事务，HTTP 不直接读写配置表。创建、编辑、删除和连通探测要求当前 active 管理员且 Actor 无 Token scopes；取得账号锁后重新核对角色与状态。列表读取也核对当前账号，数据库失败返回安全 503，不能伪装成空列表。两类编辑均必须带原 `expected_updated_at`，完整替换 URL 也不能绕过版本检查；过期版本返回 409。创建与编辑拒绝未知、重复、尾随 JSON 和缺失必填字段。空编辑凭据保留原密文字节；只有提交未改动的脱敏 URL 且版本匹配才保留原完整 URL，含占位符的替换 URL 不可保存。路径映射仍仅由原配置合同维护，不改写冻结操作。

连通探测先授权并冻结当前配置版本及完整配置摘要，再在事务外同步调用实际原生客户端；返回后重新核对当前账号和配置，过期结果不发布。不存在返回 404，存储或凭据不可用返回 503；实际连接失败与成功分别用 `alive=false/true` 表示，低层 URL 与凭据错误不进入响应。探测成功回执、审计和事件同事务提交；不以测试连接为由改写配置版本。生产和验收复用 App 显式原生工厂，保留 qBittorrent、Transmission、Jellyfin、Emby、Plex，未知协议不猜测。页面读取失败时禁用旧配置编辑；保存确认后等待新列表提交，409 提供显式重读并放弃草稿，传输结果不明时只允许人工读取核对，不自动重发修改。账号或角色变化清除旧配置与探测结果，URL 和凭据不写入浏览器持久存储。

策略 current_revision 必须引用存在的 revision（可用延迟复合 FK 处理初次创建）；冻结引用不可清理。事实的 task/unit、file/unit 必须与相应映射一致；关系纠正保留历史并失效旧事实。

### 8.3 关键索引

以下为实现约束示意，不是可单独执行的完整迁移：

```sql
CREATE UNIQUE INDEX query_execution_active
ON query_execution(indexer_id, query_hash, config_revision)
WHERE state IN ('queued', 'running');

CREATE UNIQUE INDEX acquisition_reservation_active
ON acquisition_reservation(unit_id, acquisition_lane)
WHERE released_at IS NULL;

CREATE UNIQUE INDEX library_file_current_path
ON library_file(root_id, rel_path)
WHERE retired_at IS NULL;

CREATE UNIQUE INDEX download_task_external_binding
ON download_task(client_id, infohash)
WHERE infohash IS NOT NULL AND binding_released_at IS NULL;

CREATE UNIQUE INDEX job_dedupe
ON job(kind, dedupe_key)
WHERE dedupe_key IS NOT NULL AND state IN ('queued', 'running');
```

另需媒体身份/单元、文件单元、需求、分页、幂等、step.op_key 等唯一键与 FK。按 ready/过期租约、wanted、聚合顺序、文件根/路径建立针对性索引，观察后优化，不提前分区。

### 8.4 保留与清理

默认候选 30 天、查询执行/扫描观察成功后 7 天、普通 job 终态 14 天、dead 90 天、全部投递完成事件 7 天、失效事实 180 天、审计 365 天。未决状态、冻结引用、外部绑定与关键幂等操作指针不按普通 TTL 删除。

批量清理先选有限主键再 DELETE，无 DELETE LIMIT；保留 autovacuum，观察膨胀/锁等待。恢复/历史追溯与配置信息先确认可重建再清理。

当前生产自动清理采用有界安全子集：App启动后及每分钟执行一批，整个UoW最多5秒、单条SQL最多2秒。只删除14天前已完成且租约已过期、没有active/latest/last-good指针的成功或失败全局速率观测，并在同事务删除其原终态job；abandoned、排队/执行中和结果未决记录保留。每批最多200组。另清理30天前完全无查询链接、预留、下载、轮次选择、审核、阻断和升级授权引用的候选，每批最多200条；选择主键并锁行，SKIP LOCKED避免等待在用记录。schema145仅追加对应保留索引，不放宽任何不可变证明触发器。其它受保护分页证明、业务任务、投递、事实、审计及关键回执继续保留，不将上述默认TTL当作删除许可。

<a id="reliability"></a>
## 9. 持久任务与 Outbox

### 9.1 可靠性级别

| 级别 | 完成/恢复语义 | 示例 |
|---|---|---|
| R0 | 即时提示可丢；重查状态 | SSE 进度 |
| R1 | 持久执行进度可保留，任务可周期重建；不含破坏性文件操作 | RSS、元数据、扫描 |
| R2 | 业务提交后至少一次投递 | 通知、媒体库刷新 |
| R3 | 多步外部变更，检查点与观察恢复 | 下载提交、整理、删除 |

动作必须声明级别。R1 扫描依然遵守完整性协议，不因“可重建”就允许不完整扫描失效事实。

### 9.2 原子认领与防护

```sql
-- ClaimJob；参数：kinds、owner、正数租约毫秒
WITH candidate AS (
  SELECT id FROM job
   WHERE state = 'queued' AND not_before <= statement_timestamp()
     AND kind = ANY($1::text[]) AND attempts < max_attempts AND $3::integer > 0
   ORDER BY priority DESC, not_before, id
   LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE job AS j
   SET state = 'running', lease_owner = $2::text,
       lease_until = clock_timestamp() + ($3::integer * interval '1 millisecond'),
       lease_token = j.lease_token + 1, attempts = j.attempts + 1,
       updated_at = clock_timestamp()
  FROM candidate AS c
 WHERE j.id = c.id AND j.state = 'queued'
RETURNING j.*;

-- LockJobForWrite；写回/续租先在同一事务获得行锁
SELECT id FROM job WHERE id = $1::uuid FOR UPDATE;

-- FinishJob；查询内部先锁定，再物化当前执行权，避免等锁前判定
WITH locked AS MATERIALIZED (
  SELECT * FROM job WHERE id = $1::uuid FOR UPDATE
), authorized AS MATERIALIZED (
  SELECT id FROM locked WHERE state = 'running'
    AND lease_owner = $2::text AND lease_token = $3::bigint
    AND lease_until > clock_timestamp()
)
UPDATE job
   SET state = 'succeeded', finished_at = clock_timestamp(),
       updated_at = clock_timestamp(), lease_owner = NULL, lease_until = NULL,
       result = $4::jsonb
  FROM authorized WHERE job.id = authorized.id
RETURNING job.*;
```

认领短事务提交后才执行。写回/续租先获得执行权行锁，再按当前 DB 时刻检查：可在业务 UoW 中用两条顺序语句，也可在单条 SQL 中通过物化的 locked → authorized 顺序完成；不能只在 UPDATE/SELECT FOR UPDATE 的 WHERE 中加 clock_timestamp()，因为行锁等待前已评估的条件未必在未修改元组上重算。任务 Finish/Fail/Renew 的 SQL 和提交 guard 均在取得行锁后检查 owner/token/state/严格有效租约，续租与退避从获锁后的当前时刻计算。业务结果与完成同事务，完成更新 0 行则整体回滚；已持有的锁防止新执行权并发接管。token 不混用 attempts，回收递增 token 并清空旧所有权。delivery 写回同样遵守该规则。

Queue 的时长在转为 int32 前验证毫秒范围：正数不足 1ms 或超过 2147483647ms 明确拒绝，不截断成默认或较短间隔；Claim/Fail 的非正输入保留原默认，Renew 必须显式正数。SQL 也拒绝非正原始间隔。认领的 lease_until 从实际写入 clock_timestamp() 计算，使表锁等待和查询协议差异不消耗认领后的租期。

每 lease/3 续租；失败取消逻辑 context，不宣称物理 I/O 已停。R1 可重建、R2 可重投、R3 先观察；耗尽移 dead，人工重试不倒退 token。任务去重不能代替单元占位或资源预算。

默认预算：元数据 4 + 令牌桶、每索引器端点 1 + 间隔、每下载器 2、每存储根物理操作 1、CPU 池 GOMAXPROCS。暂不可用退避+抖动，429 优先 retry_after；R3 根据动作结果确定性选择恢复，不按 error retryable 盲目重发。

TMDB 的预算由 app 显式持有并通过 ports 注入客户端，密钥轮换保留同一预算。四个物理槽位覆盖请求开始到响应体关闭完成，取消逻辑 context 不释放仍执行的物理操作；释放幂等。令牌桶初始容量4，每250ms补充1个令牌（每秒4个），等待可取消并计入原HTTP超时。429 的 retry_after 在同预算所有客户端间共享，较短冷却不得缩短既有期限，不阻塞无关资源；此进程内预算不声称跨重启持久化。索引器端点使用既有持久预算与数据库会话物理锁。

下载器预算由 app 组装的 Manager 按客户端 UUID 持有，每客户端两个物理槽位。连通性检查、提交、对账和观察共享同一预算；工厂重建或替换同 UUID 的客户端不会增加容量。提交先取得槽位，再准备 submitting 事务；取消等待不生成提交任务，也不进入提交授权事务。已准入的调用同步持有槽位至回调和完整文件清单读取返回，逻辑 context 取消不代表忽略取消的旧客户端已结束。不同 UUID 独立，释放后合法提交和原操作结果复用继续可用；此 Manager 内预算不声称跨进程或跨重启持久化。整理的源/目标根则由既有目录 FD flock 按 device/inode 排序独占，先取得物理锁再认领逻辑执行权，取消等锁不释放其他 owner 的锁。

提交后本地唤醒 + 最多每 5 秒查库；LISTEN/NOTIFY 若后续引入只作为唤醒。任务认领使用五分钟就绪等待门槛限制优先级饥饿：`not_before` 已满足且等待至少五分钟的任务先按 `not_before` 服务，同一就绪时刻按优先级与 UUID 排序；未达到门槛的任务仍按优先级、就绪时间与 UUID 排序。排序不修改持久 priority、不提前执行未来任务，也不绕过 `FOR UPDATE SKIP LOCKED`、重试上限或资源隔离；它限制就绪队列中的优先级饥饿，不保证被锁住或物理 I/O 未结束的任务在五分钟内完成。周期 RSS/健康调度仅将持久 job 与下一次调度时刻原子提交；实际网络由有 owner、租约、取消及等待责任的消费者执行，重建调度器不重复入队。

### 9.3 Outbox 顺序与边界

通知管理创建/列表经ports.NotificationChannelRepository；所有通道创建/列表、启停/停旧建新、批次/投递观测与重放/跳过先取得当前用户锁，验证活动会话管理员后才读业务记录或回执。账户变化在锁等待后重新校验；管理事务不释放正在发送的物理租约。创建原键回执、安全审计/事件和通道cipher同事务；secret请求摘要使用主密钥域分离HMAC，拒绝摘要字典泄露。外部Webhook/Telegram错误不带请求URL/凭据，分类保留超时、取消、认证、429、服务和格式。

**多通道通知（M10-04，schema116）**：生产通知工厂支持Webhook/Telegram及`wechat`/`dingtalk`/`feishu`/`bark`；使用现有独立`notify:<channel_id>`投递、聚合顺序和dead显式重放，不新增反射注册或运行时。四类机器人使用管理员明确配置HTTPS目的地，企业微信/钉钉/飞书限定官方机器人路径，Bark可自托管。URL/签名密钥及可选图片配置一并加密，只在新的通道命令内存保留；替换必须停旧建新，旧投递不改投。钉钉按毫秒、飞书按秒生成原生HMAC；拒绝跳转，十秒调用上限，64KiB响应上限，缺失/null/重复或类型错误的业务成功码不能判为送达。认证、限流Retry-After、超时、取消、坏包和上游失败保留类型，但不保存URL或响应体。平台回执丢失后仍遵循至少一次语义，可能重复，原Event-ID不变。
通知429退避统一限制为最多24小时：有效正整数秒数先限幅再转换，HTTP日期按当前时钟计算并向上取整到毫秒；超过上限的有效值使用上限，不退回短默认间隔。缺失、非法或非正建议沿用通道默认值（Webhook/机器人1分钟，Telegram10秒）。Dispatcher将结果写入原投递not_before，重建或恢复不能提前认领，到期仍使用原Event-ID。
可选图文配置`image_url`仅允许TMDB公开`https://image.tmdb.org/t/p/w342/<file>`且无凭据/查询/片段；企业微信使用news文章，钉钉使用markdown图片，Bark使用icon。飞书使用已有`img_`图片键的interactive卡片，不上传图片。应用不为通知读取图片或额外请求元数据，无图时保留旧消息模板；Webhook和Telegram不接受这些图文配置，既有消息正文/HMAC保持原合同。
管理员POST`/api/v1/notification-channels/{id}/test`提供完整`expected_updated_at`和原键；事务先核验实时会话用户，再恢复同账号同键回执，新命令锁原通道核验启用/未替换和版本。固定安全测试event、仅原consumer投递、审计和202回执同提交，尾部失败完整回滚。202只代表queued，不代表delivered，POST本身不调用外部服务；原键恢复不新增事件。设置页按钮先持久保存不含秘密的账号/通道/精确时间戳/原键，存储失败不POST，丢回执显式同键恢复，确定403/409停止并要求复核；账号/角色切换重建面板，取消并丢弃晚响应。通道所有管理响应private,no-store，JSON深重复/大小写别名/未知字段/非法query在绑定前拒绝。schema116空历史可退115，有机器人通道历史时拒绝降级丢弃。

**个人浏览器推送（M11-05，schema120–121）**：Web Push不复用管理员全局机器人收件人。浏览器原生订阅永久绑定当前活动用户、端点摘要和VAPID公钥版本，端点/p256dh/auth整体加密；撤销或过期后不能恢复、转属或改投。注册/撤销与成功审计、事件和原键回执同UoW提交，新命令才检查部署版本并加密，历史原键先恢复；API只回设备ID、状态和时间，所有响应private/no-store。部署默认关闭，启用前验证P-256公私钥与联系subject；私钥仅在部署秘密中，公开配置只返回公钥和版本。

`PublishImported`仅在显式启用时，在原导入事务冻结`webpush:<device_id>`独立投递及原user/VAPID/unit/file/root证据；必须有有效映射和imported事实，成员还须有自己的当前订阅/Wanted范围，管理员按有效事实可见。新设备不能成为旧事件收件人。发送会话持设备数据库会话级物理锁至实际I/O结束，事务外解密和网络读写；发送前两次检查当前设备、账号、范围、事实、VAPID和delivery owner/token/state/有效租约，写回再次校验租约。404/410停用设备与跳过投递同事务，429保存Retry-After；R2至少一次恢复可以重复，固定事件标签只是浏览器合并提示，不作严格去重保证。VAPID换代、设备撤销或失去可见性使旧投递跳过，不能重定向。

浏览器仅在用户点击启用后请求权限，单次申请未结束前不重复；恢复记录只含账号、命令/设备ID、版本和摘要，不含端点/密钥。响应丢失先查询原键，存储不可读/不可写停止HTTP写，读失败清空旧设备并撤回就绪；登录/退出/401清理浏览器订阅。独立Service Worker不截获fetch、不缓存账户数据；通知只显示通用提醒和固定event tag，点击固定本站根路径。服务端HTTP201只确认推送服务接受，实际浏览器通知对象另验。

**演职员关注（M11-06，schema122–126）**：关注是当前活动用户持久保存的独立意图，固定TMDB人物ID及actor/director角色，显式指定最早上映日、巡检间隔、电影/TV目标根、质量策略版本及可选站点/下载器绑定。人物原生combined_credits验证人物ID，演员仅来自cast，导演要求Directing/Director关系；电影/TV身份分离，日期未知不猜测，角色证据不授予下载或入库权限。配置、不可变历史、成功审计、事件及原键回执同事务；保存不排任务，不访问元数据。公开配置按账号隔离，禁缓存并实时检查权限。
人物关注调度每批最多64个配置，逐配置独立事务；单项失败回滚并保留首个错误供上报，其余本批配置继续排队。调用取消立即停止后续排队，已提交任务不回滚。恢复扫描成功后，排队阶段的单项错误不阻断已提交任务的认领；恢复扫描失败仍停止认领，执行阶段继续按当前权限、配置历史与物理围栏复核，不将调度失败解释为执行授权。
跨批候选按(owner_id,id)有界游标循环扫描，已处理的失败配置也推进位置，取消中的未完成项不推进；末尾回绕以重新检查失败、变更及新增配置。游标仅存于显式构造的存储实例，同实例重叠调用让出扫描且不阻塞等待；重建从起点扫描，持久任务、配置冻结与数据库去重仍为唯一执行依据。每次最多处理64项，最多两次候选读取，不以扩大批次或新增调度授权绕过失败前缀。
每轮冻结账号和配置历史，单配置最多一个活动任务；一次人物响应封存为不可改写的来源和有序候选，按64项分页，续页及新租约使用原来源。早于起始日期或目标未配置保留排除记录，未知日期及证据冲突留待复核，不自动取消已有订阅。物理执行绑定job/owner/token/数据库会话，取消等待真实I/O返回后记录释放；租约过期仍持锁只能等待，会话消失但无释放证据转review_required，不以配置停用清除未知边界。恢复逐配置加锁并沿稳定job游标扫描，原job/run/payload/退避时间不重绑；已释放且授权仍有效才允许原任务恢复。
人物专用创建桥复用现有订阅创建服务，事务锁顺序为账号排他锁→配置→任务；原物理会话继续持锁。前置和COMMIT复核人物来源/候选、账号、当前配置版本/历史、根、策略、绑定、执行证据和有效租约。作品详情用真实TMDB身份，TV使用原生季集与稀疏编号；订阅/Wanted/候选成功结果/审计/事件/原创建回执同事务。配置版本、作品类型/ID和根决定稳定创建键，同一版本跨轮次复用原回执，不改变已有暂停意图；已成功候选缺原账本停止，不能重建成功记录或重复审计。schema126只放行持原执行物理锁的独立创建事务更新created候选，提交时要求订阅身份/根/账号及原回执一致，来源和待复核写仍限原执行会话。App显式构造服务，使用既有队列、心跳、取消和等待生命周期；无下载绑定只建订阅与需求，不承诺立即下载。

**已实施管理边界（2026-10-08）**：`GET /api/v1/integrations/deliveries` 为当前业务管理员无 payload/config/raw error 的有界历史观测，每页默认100、最多100条；状态在数据库分页前筛选，按updated_at/id降序续页，游标绑定状态及可选原event_id，二者均在数据库分页前筛选。翻页不是跨页冻结快照，更新可能把记录移至前页，回到最新页可重新读取；收件人筛选仅限本页，GET不发送、不写业务。重复/未知参数、跨状态游标、请求体和命令键均拒绝；POST `/{id}/replay` 必须显式接受至少一次重复风险并提供 dead 观测的 token/attempts/updated_at 与 Idempotency-Key。窄 DeliveryManagement UoW 角色先于重放，锁后检查 dead、无执行租约、原收件人仍可用/启用；只重排原 event/consumer/seq，token++、attempts 重置，不新建发送副作用。投递改变、审计、事件、稳定资源回执同事务；running/pending/delivered 或旧快照拒绝，禁用/缺失收件人不得悄悄改投其它通道。公开回执仅 pending，真实 App.Run 外部回执后才 delivered。Webhook 仍依赖接收端 Event-ID 去重，Telegram 明示可能重复。UI 未确认写入保持同 key/不可变草稿；GET 列表不充当其成功回执，已确认写但刷新失败只读恢复。配置 URL/HMAC/token/chat 只在当前未确认命令内存与服务器密文，不写浏览器持久存储或返回客户端；新增通道仅进入后续事件快照。原收件人的版本化启停已按下述边界实现；媒体级合并/固定中文v1与审计跳过已按下述边界实现；目的地/秘密更换已按停旧建新实现；多语言选择仍待，不以界面入口存在虚报全阶段。


**通道启停实际切片（2026-10-09，schema29，无新增表）**：`ports.ChannelStateRepository` → `notify.ChannelStateService` → `ChannelStateStore` 窄 UoW，管理员 PUT `/api/v1/notification-channels/{id}/state` 提供原 UUID、完整 `expected_updated_at` 与 Idempotency-Key。GET 的 TIMESTAMPTZ 保留数据库微秒；更新使用 `GREATEST(clock_timestamp(), old+1 microsecond)` 保单调，不能用前端 Date/毫秒重建命令版本。锁后复核原配置状态戳，成员/旧观测/指纹不同拒写，启用无已加密配置的旧通道拒绝。state、成功审计、管理事件、稳定安全 receipt 同事务；不编辑目的地/密文字节、不重建 delivery，不把后来新增通道改投给旧 event。旧 DELETE 已 deprecated 且固定 409，无 204 假成功。
禁用停止新 claim，原 pending/consumer/event/seq/dead 保留，不消耗重试预算；已 claim 尚未 Send 的通知在实际禁用观测后，先锁 delivery、再用当前数据库时间核验 owner/token/state/lease/consumer/禁用，同事务回 pending、token++、退本次尚未执行的 attempt。lease 等锁期间到期拒退回。client 构造后/发送前再读禁用状态；**已开始 Send 可完成**，控制命令不撤回网络副作用、不释放执行物理槽或 delivery 租约，仍只能外部成功回执后 finish。恢复仅认领原 pending；dead 仍须原有明确风险重放。App.Run 本地真实 HTTP/HMAC/PG 已验证禁用收件人不耗次数且另一收件人独立完成、恢复原 Event-ID、发送中禁用不逻辑释放。
设置页启停采用服务器观测，不做乐观已送达；父列表刷新/503不丢未知命令原 key，确认后读取失败只重读，409清旧状态并要求重新读取；现桌面/390px、44px触摸与正常cookie/CSRF→真实PG已验证。不等于 Telegram 外网、NAS、编辑目的地或 M3 全阶段验收。

**媒体通知合并实际切片（2026-10-09，schema30–31）**：原 `media.imported`/unit 事件和刷新保持原 file/unit 合同，不重写任何历史通知；导入 UoW 另为当次启用原 channel 持久保存 `(media_id, channel_id)` 分组及 source_event/unit/file/root UUID 证据。固定 3 秒窗口（不滑延）、最多32成员；后来新建/启用通道不接收旧成员，收件人独立，成员和截止时间不可改。满32转 ready，否则 deadline 到期；App 每秒有界扫描最多16批，批行 `SKIP LOCKED` 后按同 media/channel 未封存前驱顺序，安全成员 payload v1、新 `notification.media_imported.v1` event / `notification.media` 媒体级 aggregate、仅原 `notify:<channel_id>` 投递和 sealed 指针同事务。SQL 单批失败保留原批等待、同轮排除失败 ID继续其他媒体/收件人，不把失败标 delivered。业务导入和封存/发送分离：封存失败不撤销已提交 imported，导入成员写失败则整个导入回滚。源 event/封存 event/成员受数据库不可变与关系一致保护；有批历史拒降级移除证据。
旧 unit级 notify 未完成记录（含 dead）仍阻同 consumer/相关媒体的新 batch claim，聚合命名变化不绕过旧失败，其他媒体/收件人独立。封存后不能重读变化事实重新组包；Webhook Event-ID/正文/HMAC 稳定，HTTP成功→写回失败仍可能至少一次重复；Telegram 重复风险不变。旧unit级事件保持既有英文渲染，不通过本地化改变历史重放正文/HMAC；封存媒体批次v1 固定中文摘要只呈现唯一单元/文件/库的历史提交计数及媒体UUID，绝不复制原事件 title/path/URL/config，重放不是当前拥有声明；未知版本/非法UUID/重复source/额外JSON字段拒发，不静默降级。多语言选择仍待；目的地/密钥更换使用下述停旧建新合同。
GET `/api/v1/integrations/notification-batches` 为管理员最近100条同SQL快照观测，无消息正文/密钥/路径；collecting、sealing_pending、pending、delivering、delivered、dead分别展示，sealed≠delivered，禁用保原pending。现设置页与正常cookie→HTTP/PG/390px、读取503清旧状态已验证。实际 App.Run 恢复原 open 数据库窗口，自然 deadline 封存→本地真实HTTP/HMAC→delivered；原投递丢成功落库后重发 Event-ID/正文/HMAC逐字节相同。跨根/TV成员数使用明确合成PG事实probe，不代表真实NAS或TV自动整理放行。

媒体通知批次的「查看原投递记录」只把原event_id带到同一管理面板，查询该持久事件的现有通知/媒体刷新投递；没有封存事件时不提供入口，不依据批次状态推断送达。投递列表event_id与state在SQL分页前取交集，游标同时绑定二者，旧无事件v1游标仍可继续原范围。清除定位显式回全量；状态切换、续页、失败重读和成功命令后的观测均保留事件范围。未确认命令禁止跳范围，原键回执与当前角色校验保持不变；错事件响应丢弃，成功回执后的读取失败仅重读，不重放。

**审计跳过实际切片（2026-10-09，schema32–33）**：新增独立 `skipped` 终态而不是伪 `delivered`；POST `/api/v1/integrations/deliveries/{id}/skip` 为管理员窄 DeliverySkip UoW，精确 expected token/attempts/完整 updated_at、Idempotency-Key、accept_delivery_loss 与固定枚举 reason 必需。锁后仅 dead、NULL执行租约、无delivered_at可提交；禁用/不存在原收件人仍可明确选择跳过，但不重定向、不调用外部发送。skip state、token++、skipped_at/reason、安全成功审计/事件/原资源回执同事务；旧actor权限仍先于cache，指纹冲突409。skipped记录不可修改、删除或再重放，CHECK明确非空reason防SQL unknown穿透，delivered_at始终NULL，有审计skip拒降级弱化历史。
普通聚合FIFO及旧unit→新媒体batch barrier仅允许 `delivered` 或明确 `skipped` 前驱放行；跳过不自动让后继送达。实际 App.Run 本地HTTP/HMAC测试证明只发后继原media batch、被跳原Event-ID不发送。UI replay/skip共单owner/互斥不可变命令，丢回执→GET仍同action/reason/key；已确认后503只重读，409清旧观测，缺显式lossAck按钮不开放，只有dead显示动作。skipped不显示可重试时间或送达绿，原cfg不可用观测仅隔离该条不阻整列表，不泄坏consumer字符串。正常cookie/PG/浏览器/390px已验；实际用户历史跳过仅在其明确命令后发生。

**用户决定与停旧建新实际切片（2026-10-09，schema34–35）**：通知地址/HMAC/Telegram token 更换采用用户明确选择的「停旧建新」，不冻结旧凭据继续自动发送、不用新凭据重签旧事件。管理员 POST `/api/v1/notification-channels/{id}/replacement` 需要完整原 `expected_updated_at`、Idempotency-Key、新name/type/完整secret config和 `accept_old_pending`。usecase只依ports窄仓储和系统SecretProtector，keyed HMAC请求摘要与加密写入不含明文审计/cache；数据库actor/key命令锁→原channel锁，锁后版本/未替换状态复核，原is_enabled=false/replaced_by、全新UUID/cipher启用、成功audit/event/原安全receipt同事务。相同key只建一次且返回原receipt而非live状态；原事件/batch/member/delivery/seq/token/lease完全不搬移，只有之后的导入快照包含新channel。原replaced_by不能撤回，旧不能重新启用或再替换，channel destination/cipher不准原地修改；两侧身份不能删除，有replacement关系拒downgrade弱化历史。
替换不取消已开始网络Send：其owner/token/lease保持直到自身真实HTTP回执；尚未发送的旧pending仍等待旧通道、不消耗attempt、不标delivered，dead可原审计skip，不自动补发到新通道。App.Run以channel同步阻塞原HTTP测试，新media batch自然deadline封存到新UUID且用新HMAC取得自己的204/PG回执，原物理发送仍delivering；原204之后才delivered，旧另一个pending留在原consumer/0attempt。
设置页复用现表单显式原UUID、完整新配置和旧pending确认；不预填旧地址/秘密，只有当前未确认命令内存冻结action/key/body/stamp。丢201→GET不能确认命令，仍原key草稿重试；确认后503只读、实际旧戳409清旧列表与秘密；已替换旧行无enable/replacement入口。正常cookie/PG/390px/desktop及最新embed已验，实际API新密文与对应系统decryptor相同；网络模拟仅明确本地协议源，不宣称liveTelegram/NAS完成。

业务同事务插入 event 与各 consumer delivery。consumer 为 notify:<channel_id>、mediaserver.refresh:<server_id>及显式启用的webpush:<device_id>；每渠道独立失败/重放。SSE 进度不写持久投递。入库事件在导入 UoW 快照已配置媒体服务器；启用通知通道以原收件人批成员在同事务保存，并按上述封存后生成通知投递；其它业务事件只快照具体通知通道，不写无消费者的 scheduler/download_manager 伪投递（获取使用持久 job，追更由需求表恢复）。没有配置就没有该类 delivery，不把后来新增配置反向当成旧事件的收件人。

app Run 显式构造刷新/通知适配器并拥有投递消费者，取消后等待退出，外部发送并发上限为 4，每次发送有独立期限；实例离线不会阻塞其它 consumer，也不撤销 imported。认领前回收过期 delivering、增加 token，耗尽转 dead。发送前与完成/失败写回均先锁 delivery 再用数据库当前时钟校验 owner/token/state/有效租约及具体 consumer，网络 I/O 不在事务内；刷新还须从有效文件映射与 imported 事实推导路径，禁止任意请求路径。
协调器每秒只发现已到期pending或租约已过期的delivering消费者；每个消费者单独登记，认领不到任务或无法取得物理许可时退出并从活动集合删除，有新到期投递再由下一节拍创建。future-only、已完成、dead消费者不保留循环，不因完成通知立即重复发现造成忙循环；已有聚合顺序、owner/token/租约写回和物理执行释放规则保持。
Outbox时长在缩窄为int32毫秒之前验证；正数换算结果必须在1..MaxInt32范围，越界及不足1ms返回ErrInvalidDuration且不改状态。零/负值沿用认领30秒、失败退避5秒的默认值。SQL拒绝非正间隔，认领租约和失败退避用数据库clock_timestamp建立，数据库等锁不提前消耗租约。消费者发现对pending复用认领资格：排除停用通道、耗尽尝试、同聚合未决前驱及历史逐集通知阻塞；expired delivering仍独立进入恢复，不能因停用而遗留失效租约。发现只作调度提示，实际认领和发送仍重新验证。


M10-05媒体服务器闭环：Emby使用`X-Emby-Token`，保留部署URL前缀；`Test`要求有效`System/Info`中的非空ServerName/Version，HTTP200或HTML登录页不算探针成功。只向官方`POST /Library/Media/Updated`发送原imported文件映射出的Created路径，不使用全库`/Library/Refresh`。查询通过递归Video清单本地精确匹配，8MiB上限、总数等于实际条数、唯一Id和唯一精确Path全部成立才确认，分页/重复/缺字段/JSON重复键或大小写别名不能提供确认权；无匹配与上游失败分开。10秒上限、禁止认证跳转，底层URL/凭据/响应正文不进入错误。Jellyfin沿用相同路径更新及原Authorization协议；Plex按目录边界选择最长且唯一的库路径，所有目标先验证，目录去重后发送局部刷新；未知或歧义路径不回退全库。共享刷新用例将原生Retry-After写入现有独立delivery退避；刷新送达不自动产生confirmed事实，确认仍复用文件/根/映射/配置快照和入库事实的事务复核及物理排他。
通知配置使用同一主密钥加密，公开管理 API `/api/v1/notification-channels` 只限业务管理员，不以 Token scope 提权；创建要求 Idempotency-Key，摘要及通道 UUID/成功审计与配置同事务保存，权限先于重放，响应、缓存和审计不存秘密配置。schema 18 清除并禁用无法安全加密的旧明文配置，将无具体收件人证据的旧通用投递隔离为 dead（不伪称投递完成）；降级不复活秘密。Webhook 必须配置 HMAC secret；通知与媒体服务器客户端禁止重定向认证/签名，Telegram 必须实际 `ok=true` 且响应有界，不从 HTTP 200 推断成功；429 按外部重试期限调度，错误不保留 URL/token/响应原文。

delivery 用 FOR UPDATE SKIP LOCKED + UPDATE RETURNING 原子认领，提交后发送；写回检查 delivering、owner、token、有效租约。仅选择无未完成前驱的同 aggregate/consumer delivery；dead 阻塞该聚合后续，管理员重放或审计后明确跳过，其他聚合不受影响。

**identity seq 不是提交顺序。** 同聚合发布先锁聚合行或一致键 transaction advisory lock，再分配 seq；分发按 delivery 状态扫描，不以 seq 高水位跳过迟提交事件。

投递为 at-least-once：Jellyfin 重复刷新安全；Webhook 带稳定 event ID/HMAC，接收方去重；Telegram 允许少量重复。发送后落库前崩溃的窗口不能由本地 sent 标记消除。

### 9.4 命令幂等

唯一键包含 Actor 类型/ID与 Idempotency-Key，校验规范化请求摘要；同键不同请求 409。查旧结果前仍检查当前权限；新操作再执行配置版本/对象状态校验。成功审计、响应状态/资源指针与业务同事务保存。


设置命令实际实现：command 共享纯会话前置检查和版本化 action/target/payload 摘要，事务不通过通用 pgx/sqlc Tx 暴露；SettingsRepository 在自己的窄事务锁当前用户并核验活动与角色，再串行读取原回执，按目标身份序列化首次创建。设置、脱敏 setting.updated 事件、成功审计及幂等结果一起提交。新摘要绑定对象；历史 payload-only 回执只在摘要、资源、状态与完整响应元数据可验证时恢复，不改写或重签原记录。秘密在事务外加密，不进入明文审计/事件/结果。GET /api/v1/settings/{key}/commands/{request_key} 仅为本人当前管理员会话返回无秘密元数据；404 表示尚未确认，不能被当作未提交证明。设置页面使用当前配置版本、Cookie CSRF 和原请求键；恢复记录仅持久化绑定用户的键与版本，秘密只留当前页面内存，刷新后通过原回执恢复，未确认时重输原值沿原键重试，读取失败清旧状态并禁止写入。
手动电影获取当前用户合同：带键及无键 `AcquireMovie`、显式选择预览/确认/回执都只接受当前活动管理员或成员会话；API/system与带scopes凭据在事务及元数据I/O前拒绝。存储事务锁当前用户并在等锁后复核当前角色；预览网络前后分别使用一致快照，确认和回执读取持用户锁至事务结束，与账户变更按锁顺序串行。电影根/单元仍属于管理员与成员共享的库范围；公开手动入口不能借内部SubscriptionID需求，不引入root ACL或改变内部自动reserve入口。带键命令按Actor/键串行，显式版本化摘要覆盖全部业务意图及内部冻结版本；操作、占位、需求、任务/job、事件、成功审计与202回执同事务提交，当前权限有效时可恢复原操作，不重新要求历史根仍在线。同键不同意图409、当前权限失效403、未预期事务失败安全503；Cookie会话被停用时HTTP认证返回401并清除私有UI，客户端保留原未确认命令，恢复会话后只能继续原键/目标。无键历史不补签回执；显式确认audit/receipt末尾故障连新媒体/单元与执行任务整体回滚。

异步命令保存 202 和稳定 job/operation ID，短事务结束时记录已 done；不提交长期 in_progress 等待外部动作。响应丢失重放同结果；失败事务不留下占位。拒绝/失败审计单独记录，不伪称已随业务提交。

关键操作响应内容可到期清理，但保留请求摘要和操作指针/墓碑；旧键不能静默创建第二个获取。需新动作时使用新键，重试现有动作保持 operation ID。登录、连接测试和纯预览不强制长期幂等记录。

<a id="integrations"></a>
## 10. 外部集成

窄端口定义在 ports，所有调用带 context，结果类型区分“无结果”和“失败”。结构化错误在 ports 定义：timeout、unavailable、auth_required、rate_limited（retry_after）、blocked、bad_response、not_found、rejected。原始底层错误只供脱敏诊断，不直接输出 API。

电影详情按电影类型、TMDB ID 与语言保存原始响应及对应 ETag，TTL 为 24h；返回独立元数据副本，包含 Genres 切片。中文字段不足时在调用者副本上组合英文回退，英文响应独立检查 TTL 与 ETag，不由中文 304 延长其有效期。详情响应 ID 必须等于请求 ID，否则返回 bad_response 且不进入缓存；无原始缓存或请求验证器时的 304 同样拒绝。同键请求按开始顺序获得写回序号，迟到的 200/304 不覆盖较新请求的缓存；304 返回的新 ETag 用于下次验证。缓存不改变鉴权、请求预算或识别权限。

电影外部 Find 只接受规范 IMDb（tt 加 7–10 位数字且数值非零）或正整数 TVDB ID，非法输入在 HTTP 前返回 rejected，错误不反射原输入。有效空 movie_results 为 not_found；缺失/null 集合、多个结果（包括重复行）、无效 TMDB ID、非条件请求的 304 为 bad_response，不以排序首项作为确定身份。单一正 ID 必须通过电影详情的请求/响应 ID 校验；IMDb Find 还要求详情 IMDb ID 与请求一致（输入大小写规范化）。不确定外部身份转复核，不回退标题搜索，不改写既有电影、文件映射或事实；TVDB 的单结果解析不构成绝对集号映射授权。

电影文件的标题识别使用 MovieIdentityProvider 的显式完整候选合同，与浏览第一页结果分开。TMDB search/movie 的 page 必须为 1，total_pages 为 1（空集合可为 0），total_results 必须等于实际集合大小；缺失/null 分页、额外页或计数不一致仍可供浏览，但不能认证文件身份。缺失/null results、无效 TMDB ID、错误媒体类型、非法非空 release_date 或非条件 304 返回 bad_response。领域规则要求可靠的电影标题与年份，并在完整集合中只有一个规范化标题或原始标题及年份精确匹配；同名同年、重复身份、同名但年份未知的竞争者和不完整来源转复核，不取排序首项。低置信输入不发搜索请求；失败保留端口错误类型，和完整无结果区分。识别结果不代表质量或下载授权，既有文件、映射和事实不补签。

电影文件识别的持久化使用 MovieLinkRepository，Matcher 不依赖 pgx/sqlc/store；ID 由存储实现显式注入，持久时间使用数据库时间。元数据网络请求前冻结源文件 UUID、根配置/健康/generation、大小/mtime/指纹、水位与映射/单元版本，网络在事务外。关联事务按根、规范身份通道、排序单元、源文件获取锁，再核验当前根、文件、映射以及目标单元退役与元数据版本；旧响应不能关联替换或变化后的源。媒体、身份、单元、映射、imported 事实及根/文件水位原子提交，任一步失败全部回滚。相同文件/电影映射与当前同根同质量事实的完成重放只读返回，不重复事实或水位；并发根共用一个规范身份。不同电影映射、历史未认证质量、退役关系或其它根事实不借重试补签，转复核并保留历史。此关联不表示下载授权。

电影文件识别任务使用独立的 movie_recognition_work 持久范围，绑定 job UUID、电影根、文件 UUID 与根 generation；文件/根组合外键拒绝跨根来源，同源同 generation 只产生一个任务并保留终态。调度取得根锁后用新语句检查候选，只接受健康且基线已完成的电影根中未映射、未退役且指纹明确的文件，每根每批至多 25 个。关联端口显式接收 JobLease，源读取与事务写入重新核验 kind/state/owner/token/有效租约及来源范围，等锁后不得继续使用已过期授权。关联业务、linked 结果、任务成功、系统审计与持久事件原子提交；review 结果也与任务、审计及事件同事务，不把未确认结果标为已关联，不存储原路径或异常原文。终态不能被旧消费者重写或自动重复入队，末尾错误整体回滚；事件持久化不等于投递成功。任务合同与生产识别消费者已组装，实际文件检查另见下述合同。

App.Run 显式组装电影识别的持久调度和两个独立消费者，复用应用持有的 TMDB 请求预算；关闭时取消并等待消费者及尚未结束的物理操作，再释放数据库和数据目录锁。MovieRecognizer 持有同一受限根物理槽贯穿源读取、元数据网络与关联写回。网络请求前与提交前均读取最近成功封存扫描的目录 device/inode，并核对打开目录、当前路径目录、marker 及文件 inode/大小/mtime/stat.v1 指纹；标记和源必须为可安全检查的常规文件。检查起点使用数据库时间，严格存储在取得锁后再次核验扫描 ID、根 generation、目录身份及不早于当前五秒的检查时刻，未来或缺失凭据也拒写。文件/目录替换、消失、符号链接或权限不足转 source_unavailable 复核，不制造关联事实；服务失败保持可重试，低置信不发元数据网络。物理文件检查失败不退役原文件或撤销其历史事实，缺失失效仍由独立的二次扫描合同处理。取消不能提前释放仍在使用的根物理槽；已排空的未完成任务在租约到期后由新 token 恢复。
| 适配器 | 能力与合同 |
|---|---|
| MetadataProvider / TMDB | 主身份、Search/Details/Season/Find、人物作品；详情TTL24h、季12h/播出中6h、语言回退与ETag；季集保留真实稀疏编号和未知日期，缓存不授执行权 |
| TVDB / Fanart / Shooter | TVDB 只补充已交叉证明的规范身份/译名，不授季退役权；Fanart 独立侧写且不覆盖用户图片；字幕使用 Shooter，SubHD 不启用 |
| Douban / Bangumi | 豆瓣榜单和公开想看、Bangumi 七日周历；无可靠 TMDB 身份时仅浏览或显式选候选，不借来源编号创建获取 |
| Indexer / Torznab | Capabilities、Search 分页、Recent/RSS、Fetch、Test；外部 ID 优先，跨页证据不能冒充上游快照 |
| NexusPHP / DMHY / CookieCloud | 明确协议的搜索与取源；传统 NexusPHP profile 的统计/单次签到；CookieCloud 只更新已配置且匹配的 Nexus 站点 Cookie，不创建站点或覆盖 Torznab/RSS 凭据 |
| MediaServer / Jellyfin、Emby、Plex | Jellyfin/Emby 按入库文件精确路径刷新，唯一完整路径证据才可确认；Plex 按最长唯一库目录局部刷新，歧义不回退全库，刷新不等于 confirmed |
| Notifier / Web Push | Webhook/Telegram/四机器人按渠道独立至少一次投递；Web Push 需部署 VAPID、用户授权及账号/设备/密钥版本绑定，不承诺严格一次送达 |
| Local storage | 根身份、受限 FD、完整扫描、动作能力探测、全量内容校验与观察恢复；本地事件只加速观察，NAS 无通知时回退周期 |

<a id="integration-capabilities"></a>
**专用Seerr入站**：独立配置固定本地归属账号和稳定来源身份，启用开关、专用凭据及版本由当前管理员Cookie会话维护，凭据加密且只返回配置状态。仅精确POST /api/v1/integrations/seerr使用单值Authorization专用秘密，不接受普通会话或API Token替代；正文有界，处理实例限并发和频率。来源request ID绑定规范媒体身份与正文摘要持久去重；重复不会新建本地请求，相同ID不同正文冲突。仅接收MEDIA_APPROVED与MEDIA_AUTO_APPROVED；两种外部批准事件都只创建现有media_request的本地pending请求，仍需本地审批；不采信报文账号、角色或任意URL授予权限。接收时重验当前配置、配置管理员和归属账号，创建、审计、事件和回执同事务；关闭或撤权后拒绝，即使持有旧凭据或回执也不能补发。

下载器能力必须由对应业务的生产白名单和冻结授权共同决定；实现 `DownloaderClient` 或连接测试成功不能扩大专用权限：

| 业务入口 | qBittorrent | Transmission | rTorrent / ruTorrent |
|---|---|---|---|
| 普通电影 Add、原任务观察及硬链接入库 | 支持 | 支持 | 支持 HTTP(S) XML-RPC；SCGI 需外部桥接 |
| 普通任务原生暂停/恢复 | 原任务绑定、共享需求确认与持久命令 | 同左 | 同左；精确hash单次启停，未知结果仅显式观察 |
| 手工标准/审核映射单集 | 沿现有专用授权 | 不据普通电影能力开放 | 不开放 |
| 自动 TV 普通季标准单集 | 支持；真实完整清单及单元身份围栏 | 支持同一专用围栏 | 不开放 |
| 整季包获取/显式整理、电影升级/退休 | 独立授权链 | 同一独立授权链；完整原清单与前后身份观察，无torrent导出假设 | 不开放 |
| 全局上下行采集 | 显式部署开关启用后采集 | 同左 | 同一受控采集链；原生XML-RPC字节速率 |
| 活动详情显式做种只读观察 | 普通原任务，当前对象授权 | 同左；未知时长不当零 | HTTP(S) XML-RPC原任务分享率；时长未知，不授权自动移除 |
| 做种达标自动移除 | 仅具完整发送/入库证据的普通任务，固定保留文件 | 同左；原生未知值不当零 | 不开放 |

专用自动 TV、整包和升级任务不能借普通任务的做种移除许可；历史缺少端点或入库证据的任务也不能补造授权。所有下载器 Add 结果不明时只观察原任务，不重发或换资源。硬链接要求同文件系统；copy/move 要求完整内容校验和原子无覆盖发布，reflink 不支持时不静默回退。实际 NFS 能力不足时保源、无入库；未知日期、部分扫描和失联不制造文件缺失。部署诊断及复验入口见[部署指南的能力与复验矩阵](deployment.md#integration-verification)，实际环境与验收记录仅以开发计划为准。

下载器配置在选择类型时显示本矩阵对应的支持范围、专用业务限制和目录动作前提，未知类型不推定为支持；提示不触发连接探测或业务提交，也不代替服务端授权。活动详情按持久状态展示核对与恢复指引：读取过期优先刷新，下载结果不明只核对原任务，整理复核关联原计划并保留源文件，普通成员联系管理员。指引本身只读，实际恢复继续使用原命令、原回执及已有权限/新鲜度围栏。

Jellyfin刷新、查询与连接测试统一输出ports安全错误，错误文本不携带原始URL、凭据或运输异常；当前取消、超时、401/403、429和服务失败分别保留合同类型。可选文件确认要求FindByPath返回唯一有效ID及与请求完全一致的Path，查询显式请求Path字段；缺失/null结果集、多条身份、矛盾总数或不匹配路径不能确认文件。响应完整JSON最多8MiB，拼接/超限/读取错误均拒绝。空Items只表示未索引，不等于服务失败；只有明确路径证明并通过本地有效文件/映射提交检查才产生confirmed，失败不撤销imported。

确认及刷新用例通过显式构造的窄存储端口读取文件、映射与服务器连接，不携带pgx/sqlc类型或直接导入store。确认在网络请求前读取一致快照，提交按服务器→根→单元→文件顺序取得锁，重读文件版本、指纹、退休状态、原imported/映射、根身份及服务器连接/路径配置。stamp与完整连接、来源及远端路径一起匹配，不允许借用stamp替换参数。首次confirmed、根水位、单元事实版本、审计与事件同事务；相同文件/单元已有有效confirmed时只读复用，不重复推进版本或补签旧品质。

每个媒体服务器刷新消费者使用独立PG会话物理槽；应用先取得该槽再恢复/领取逻辑投递，槽保持到实际HTTP调用返回，取消或租约过期不能提前释放。不同服务器独立推进。每次刷新请求冻结持久事件对应的有效文件及当前服务器配置，在发送前与完成短事务中重验；完成校验owner/token/state/当前有效租约，并校验原事件类型、单元与文件，不能借用同服务器其他投递凭据。迟到旧配置/旧文件响应不能完成投递；失败按原事件重试且不撤销imported，刷新成功也不等于文件confirmed。非空本地路径提示只允许等于数据库解析的规范路径，不授予文件或投递权限。

qBittorrent 原生 HTTP 边界为六个操作统一输出 ports 安全错误：构造或传输错误不携带 URL、凭据或外部异常文本；取消与超时区分，401/403、429及5xx分别保留认证、限流和服务失败合同。429的秒数建议仅接受正数且不超过一天；无法解释的建议不伪造等待时间。认证失效清缓存，由下一次调用重新登录，不在当前调用内重发 Add。Get/List 响应上限8MiB，Files仍1MiB，完整读取后校验单一JSON，不把null、拼接或超限响应当空结果。HTTP状态或读取错误不证明Add未执行：提交失败保留unknown与占位，后续只观察原哈希/操作标签和原路径，不能换资源或盲目重复发送。

qBittorrent 的Get和Remove只接受单一40或64位十六进制标识，拒绝all、多哈希与错误格式后才允许登录或网络请求。Get结果必须唯一且匹配所请求标识；List中大小写等价重复标识不能建立观察。Hash、SavePath、Progress、Size、Downloaded和State必须存在且非null，坏响应不得补成零值事实。字节数按原生int64保存；Progress虽沿用ports的float32展示字段，完成边界要比较原始JSON数值：小于1的数值不能因float32或float64舍入变成完成。异常观察不释放unknown或占位，后续仍只观察原操作；任务完成及文件来源还须满足原有字节、路径和Inventory合同。

文件Inventory的Progress同样校验原始JSON数值，不能先解码成float64再以舍入后的1授权完整清单。Files保留float64中间进度及int64字节精度，真正未完成的近1值投影为小于1；坏类型、超界与负数下溢拒绝。下游全清单验证仍要求每个文件progress恰为1、索引与规范相对路径唯一、总字节相符，并在冻结证据前后复核原任务与客户端版本；尚未完成不制造manifest、整理任务或入库事实，也不释放占位。

HTTP 客户端统一超时、代理、限量响应、脱敏与指标，只自动重试幂等读取。下载/删除等方法不能因一个 timeout 就假定未发生；须有明确的结果确定性与恢复协议。

Torznab Search/Fetch 的 HTTP 边界只返回安全的 ports 错误，不拼接请求 URL、API key、passkey、响应正文或 XML/网络异常原文。401/403 为 auth_required，404 为 not_found，429 携带 Retry-After（正整数秒或 HTTP 日期，最长 24 小时，无有效建议默认 10 秒）；5xx 为 unavailable，其余非 200（含禁止跟随的重定向）为 bad_response。取消保留 context.Canceled，context 或 HTTP 客户端/响应体超时为 timeout；其它传输/响应读取错误为 unavailable。Search 原始响应上限 5 MiB、Fetch 上限 10 MiB，读取至上限加一以拒绝超限，而不是返回截断成功；空响应拒绝，所有已取得响应的退出路径关闭 body。此合同只保证安全完整的 HTTP 响应，种子内容与执行许可仍由相应用例核验。

Torznab 原生客户端提供独立 Capabilities 合同，以 `t=caps` 读取搜索模式、支持参数与可选分页 limits；错误根元素、无效限额及 XML API 错误不得成为成功能力证据。公开 caps 不证明当前凭据可用。手动 Test 与定时健康任务均显式进入探测模式：先要求 movie-search 支持 q，再发送 limit=1 的凭据搜索，两个网络请求各预留一次共享端点预算，同一物理槽覆盖整个探测，返回条目不写候选。第二次预留在短事务内锁端点；持久任务同时锁 job，并在锁后与预算写入后按数据库时间复核 owner/token/state/有效租约、任务范围和 config_version。配置变化、冷却或权限失效不发送后续请求；网络在事务之外。健康汇总沿现有 online/offline 与 ok/unavailable/rate_limited/not_checked 原因合同，安全的具体错误类别保留在查询记录；两阶段都成功才恢复在线，配置版本过期的结果不得覆盖新配置。

Torznab 单页读取只证明所请求的 offset 窗口，不代表整个搜索完整。RSS 必须有且仅有一个 channel，文档尾部不得拼接第二个根元素；条目数不得超过正 limit。第一页允许兼容无 response 元数据的旧 RSS；后续页必须提供唯一 response，offset 为非负十进制整数且与请求一致。可选 total 若存在必须可解析且与 offset/条目数一致，超出总数的空页合法，声称仍有结果却返回空页不作为成功。负 offset/limit 在适配器发网前拒绝；用例页码限制为 0 至 MaxInt32/50，在数据库、预算和网络前拒绝无效输入，防止 page×50 溢出。元数据缺失不能用于推断已读完全部结果；聚合浏览现有有界续页合同如下；持久轮次已通过下述页回执恢复续页；Torznab 缺少稳定跨页快照凭据，不能据此证明跨页完整性。

聚合查询的 partial 必须依据持久分页证据，不能以一次 HTTP/查询成功代替完整检索。Schema 53 把每页的 offset、limit、条目数、offset 是否经响应验证及可选 total 与候选关联和查询成功同事务提交；提交时核验条目数等于唯一候选关联数。证据及其查询范围、候选关联不可事后改写；旧查询不补签，已有证据阻止破坏性回退。原生 Torznab 通过窄 PageClient 传递证明，旧 slice 合同或无分页元数据的返回只代表未知完整性。当前只有已验证 offset=0 且 total 等于实际条目数的第一页可返回完整，total=0 正确证明完整空结果；还有下一页、无 total 或未知空页均返回 partial=true。前端不完整空态明确说明尚不能判断是否有匹配资源，并保留重试/检查站点的提示。搜索入口冻结本次选择的端点配置版本。候选请求预留预算前取得端点行锁，重新校验版本、健康和 RSS 开关；候选提交按端点→查询的顺序锁定，复核原版本、运行状态与查询范围，失效只留下安全失败记录，不改变旧候选或分页历史。聚合等待后以一个只读 repeatable-read 快照读取当前端点资格、分页完整性与同源候选，剔除已失效端点，不在原聚合中自动改用新配置。聚合浏览可以有界续页，持久轮次续传采用下述 Schema 54 合同，跨页快照一致性仍无法由上游证明，不以本页凭据声称全量搜索已经完成。

聚合浏览可按持久分页证据有界续页：当前窗口必须验证 offset、有明确 total、包含满 50 条唯一关联且尚有后续，才请求下一页；固定原端点配置与关键词，最多四页/200 条，首个成功页后的续页阶段限八秒。读取数据库 next_query_at 与冷却时间后等待，不在等待期间持有物理槽，发网前仍经过原配置和预算的锁后校验。total 变化、跨页重复候选或缺失分页凭据在新页候选写入前拒绝；后续失败保留已验证页作为部分结果，共享查询也需重新验证分页和候选关联后才可纳入聚合。取消、配置失效及超过等待上限的冷却不继续创建查询。Torznab 没有稳定跨页快照凭据，跨页遍历即使到达 total 也只能返回 partial=true，不能声明全量完备。持久轮次续页采用下述页回执与任务重新入队合同。

持久轮次的端点消费者使用窄 SearchEndpointRepository，app 将持久 job 转为 ports 任务并显式注入存储实现。执行范围必须同时匹配 job kind、payload、规范去重键、轮次与 pending 端点；配置、标题、年份、截止时间与快照摘要在网络前冻结，不能重新读取后悄然切换来源。结果提交按轮次→任务→端点配置取得锁后重验范围和有效租约，核验冻结快照及所引用查询的端点、配置、关键词、类型、页码与完成时刻；端点结果与任务成功同事务，任何末尾失败整体回滚。已关闭轮次或失效来源不能挂接查询。共享 R0 查询可独立完成并由其他有效消费者引用，单个任务租约失效不释放仍在执行的物理槽；本合同不把一个成功页提升为跨页完整搜索。

持久轮次续页使用 Schema 54 的不可改写页回执：每次端点任务只取一个冻结范围页面，回执绑定 round/indexer/page/query 与实际任务 token；页回执与原 job 重新入队同事务提交，not_before 沿用端点持久预算，成功页不消耗失败重试次数。新消费者恢复页码、total 与已见 GUID，不重新取第一页，最多四页/200条且遵守原轮次 deadline。下一页失败保留已验证页并可重试，到期后使用已验证候选收口；total变化、跨页重复和缺少凭据在写入新页前拒绝。候选读取联合已保存页和旧合同的最终查询。历史回执不允许更新/删除，旧轮次不补签，已有回执阻止回退Schema54。任务重新入队或结束后的提交末尾以当前数据库时间再次检查原租约，防止触发器等待消耗租期后提交。跨页没有稳定快照凭据时，仍不能宣称全量完整。

下载 payload 的 infohash 按真实编码/格式计算，不能重编码或截断后冒充身份；V1 必须对支持的 torrent/magnet 格式做契约测试，未知格式不自动提交。下载器路径映射后仍限于已配置源根。
下载器与媒体服务器的路径映射先验证全部配置：来源前缀和目标均须为无NUL的规范绝对路径，等价来源前缀指向不同目标时拒绝整个映射，指向同一目标可合并；随后按最长完整路径前缀选择，根前缀`/`有效。未匹配的合法绝对路径不因此获得文件权限；非法映射不退回原路径。映射后的来源仍以已配置目录能力打开和检查，符号链接不能穿透来源根，映射目标本身是显式配置来源根。不能确认映射或文件来源时保留原获取保护并转人工复核。


缓存键含 provider/配置/规则版本，凭据或 passkey 不明文落缓存；元数据图片只允许已知图片主机。未来新增提供者复用能力接口，但不保证不经版本演进就能支持任意外部系统。

<a id="security"></a>
## 11. 权限与安全

### 11.1 用户与对象范围

| 能力 | member | admin |
|---|---|---|
| 发现、查看共享媒体进度、发起订阅 | 是 | 是 |
| 修改/暂停订阅、查看关联任务 | 自己创建且有对象权限 | 全部 |
| 手动下载 | 对自己需求、许可的 root/client，经同一业务校验 | 同样校验 |
| 任意文件路径输入、整理批准/复核、删除数据 | 否 | 是，确认与审计 |
| 集成/策略/系统设置、全量审计与用户管理 | 否 | 是 |

Token scopes 不能超越所属用户当前权限；Actor 从认证结果构造，不从请求 Actor 字段提权。系统 Actor 只由内部入口创建。成员状态 API 不暴露原始路径、下载 passkey、凭据或内部任务 payload。

**M11-03审计时间线读取边界（验收状态见开发计划）**：`/api/v1/system/audit`直接读取既有业务事务的audit_log摘要，不在查询中生成新的审计或业务任务。每页先锁定并复核当前活动管理员，再以数据库只读事务查询；管理员会话及精确read scope Token沿相同当前角色限制，成员、内部Actor和无效scope拒绝。响应只给id/actor_type/actor_id/action/target_type/target_id/created_at，不投射历史任意JSON payload。支持操作人、动作、目标精确匹配及左闭右开时间过滤，时间精度不超过微秒；默认50、上限200，以(created_at,id)降序续页。游标绑定账号和筛选但不授予权限，也不承诺跨页数据库快照；前页已返回的游标记录因保留策略清理后仍可续读。读取失败返回安全Unavailable而非成功空列表；所有响应禁缓存，未知、多值、空值和别名参数拒绝。schema118仅追加分页/筛选索引，业务成功审计继续与原事务同提交或回滚，独立Record原型不作为业务成功凭证。

当前 V1 API Token 切片只支持精确 `read` scope 的 GET/HEAD 查询；自身身份与认证状态可在有效凭据下读取。API 写操作必须改用用户会话，`write`、角色名、通配符或大小写变体不授予命令权限。命令执行器先于角色判断、幂等成功回执和事务拒绝非 nil scopes，防止同用户凭据读取会话命令的缓存结果；系统 Actor 只由内部构造。允许的查询仍须所属用户当前角色与对象权限；认证每次读取当前 scopes、撤销、活动状态和用户角色，不提升权限，不改写旧凭据。

家庭账号管理仅允许当前管理员Cookie会话创建、启用/禁用与调整角色；本人改密、列会话和撤销自身会话使用当前有效会话。最后一个活动管理员不能禁用或降为成员；数据库序列化保护并发角色变化。成功变更、凭据/会话撤销、审计和安全回执同事务；当前角色与活动状态先于旧回执复用重验。密码、动态验证码和恢复码不写回执、审计或浏览器存储，未知写结果通过原键GET读取安全元数据，不自动重发。

本人可通过`POST /api/v1/auth/sessions/revoke-others`集中撤销其它登录会话，重新验证当前密码及已启用的第二因素。复用当前Cookie、CSRF、严格JSON、独立安全预算及账号锁；因子消耗、其它会话删除、审计/Outbox和原键回执同事务，保留发起会话及全部API Token，不影响其他账号。响应中的session_id记录原命令保留的会话。与登录共享账号锁，删除只覆盖该事务时已存在的其它会话；命令之后建立的会话仍可正常使用。原键重放先读取安全回执，不再次消费因子或删除新会话；当前活动状态和会话有效性仍需重验。界面结果不明时先读原回执，显式重试沿用原键且重新填写凭据，不自动提交或保存秘密。

管理员协助成员恢复密码使用`POST /api/v1/accounts/{id}/password`：必须是当前有效管理员Cookie会话，重新验证管理员本人的密码及已启用MFA；仅允许其他当前角色为member的账户，不能重置本人或管理员。与角色调整复用确定顺序的账号锁；目标密码、全部会话/Token撤销、未确认MFA清理、管理员因子消耗、审计/Outbox与原键安全回执同事务。保留目标活动/停用状态及已启用MFA的计数器、恢复码，不能借密码恢复解除双因素。原成功命令重放不再次消耗因子，输入或目标变化冲突；当前管理员权限先于回执读取/重用重验。复用独立安全操作预算，非管理员在密码计算前拒绝。

TOTP可由本人重新验证密码后准备，秘密加密且只在首次准备响应返回；确认有效验证码后才启用。首次准备响应的otpauth地址可在浏览器内生成绑定二维码，编码不访问外部服务；手动密钥与二维码默认隐藏，过期、完成确认和账号切换后移除。仅编码与本次秘密匹配的PulseMedia TOTP地址（SHA1、6位、30秒），缺失或不一致时保留手动方式；回执不补发二维码或秘密。登录仍是一次POST，可附OTP或单次恢复码，失败统一拒绝，不自动再试；独占账号锁将验证码计数器/恢复码消耗与会话创建同事务，回滚不消耗因子。恢复码只返回一次并保存摘要，准备/确认回执重放不重现秘密。改密撤销其它会话、清理未确认准备，保留已启用MFA；禁用MFA要求当前密码和有效因子。主密钥丢失不能通过重新初始化绕过，恢复遵守现有备份边界。

已启用MFA的本人可通过`POST /api/v1/auth/mfa/recovery-codes`重新生成恢复码，必须重新验证当前密码及有效OTP或恢复码。复用当前Cookie会话、CSRF、严格JSON和独立安全预算；独占账号锁下同事务消费因子、替换全部十条恢复码摘要、撤销其它会话和全部API Token、保存审计/Outbox与原键回执。保留当前会话及TOTP秘密（OTP验证只推进计数器）。新码只在首次成功响应返回，回执GET和同键重放无秘密；旧成功回执不代表当前MFA状态。结果不明先查询原键，查询尚无回执时可重新填写凭据并显式使用原键提交；不自动重发、不换键，404不证明原事务已经结束，原键摘要和账号锁仍防止第二次替换。首次发送明确被限流拒绝时不保留未提交命令；原键重试被限流只能证明该次重试未进入事务，仍保留之前结果不明的命令记录。

自有 API Token 管理只接受用户会话，列表、创建和撤销均限定认证用户本人，管理员也不能借此管理他人的 Token。新 Token 固定 read，客户端不能提供用户、Actor、scopes 或哈希。窄仓库锁定当前活动用户后序列化同用户同幂等键，原子保存哈希、审计、事件及元数据回执；明文只在首次创建响应返回且禁缓存，不写回执、事件或审计。丢失响应后重试恢复原对象元数据；需要新秘密时撤销原 Token 再创建。撤销保留记录与时间，不支持重新启用；旧创建回执重试读取当前撤销状态，不能恢复凭据。过期判断使用注入时间和数据库当前时间，当前用户禁用先于缓存重放拒绝。

账户页 /account/tokens 使用生成的 API 类型及现有 Cookie 请求客户端，令牌秘密默认遮罩，只在当前页面内存临时展示与显式复制；离开、刷新、重新读取或读失败即清除。恢复记录仅包含绑定当前用户的幂等键和原请求元数据，不保存秘密。未知结果不能创建新键；重试可能取得无秘密元数据，需要新秘密时先撤销再创建。列表读失败清除旧集合，撤销需确认，成员与管理员都只能管理自己的令牌。缓存回执读取先于新命令的到期校验，故已提交创建在令牌到期后仍可恢复元数据；新键过期命令不得写入。

成员订阅列表与详情只返回目标库名称、稳定 root_id、范围及进度，省略原始 root_path；管理员保留原路径。会话与 API Token 查询使用同一对象归属检查，当前角色变化立即影响跨对象权限。系统设置列表只供当前管理员读取实际有序配置，秘密设置只暴露 configured 状态，不能返回密文或原始秘密；存储格式不符合 API 对象合同则读取失败，不伪造空列表。
订阅列表为纯本地、有界只读快照：默认50、最多100条，按创建时间与UUID稳定倒序，服务器执行文本/媒体类型/精确TMDB身份/本人筛选并返回同快照总数和类型计数。摘要只聚合当前页进度，不返回逐集Wanted；海报来自身份、来源与元数据版本匹配的media_artwork，不在HTTP读链路访问TMDB。详情按选中订阅读取。日历使用真实air_date，日期范围最多42天；已排期和未知日期分开查询、各最多100条逐集记录且独立分页，跳过退役与范围排除，保留用户跳过和暂停状态。不冻结跨请求offset页，期间的创建或状态变更可能改变后续页；客户端筛选/翻页/身份切换取消旧请求，迟到结果不得覆盖新页。查询拒绝未知/重复/空参数；会话失效401、范围不足403、存储不可用503，成功和失败响应均private,no-store，失败不能伪造空集合或零计数。

首次管理员初始化：未配置部署令牌时，入口同时要求直接客户端连接为loopback且Host为localhost或loopback地址，避免仅凭连接地址接受非本地主机名。配置PULSE_SETUP_TOKEN时要求单值X-Setup-Token头，恒定时间比较；成功初始化后入口由持久用户状态关闭，不再接受令牌重建管理员。初始化与登录即使尚无Cookie，也在写入凭据前验证所提供Origin为同源序列化HTTP(S)来源；多值、userinfo、路径、查询和fragment拒绝。无Origin的CLI流程保留；普通Cookie写请求仍要求Origin，Bearer不能绕过初始化/登录的来源检查。
认证状态查询的数据库失败不能返回成功的未初始化状态。前端只有成功读取后才进入初始化/登录或账户路由；失败清除旧身份与私有内容并提供重试。AuthProvider拥有生成客户端的Cookie CSRF/401中间件注册、移除和读取取消；同源写请求附带当前CSRF Cookie，受保护401清身份与应用壳，403保留认证身份。认证操作推进代次，迟到的状态读取或旧401不能覆盖新登录。退出结果不明时先清私有内容并显示待确认状态，重新读取服务端状态后继续，不能伪造退出成功；凭据只存在安全Cookie或当前表单内存。

首次初始化通过本地受保护入口/一次性 setup token，事务内唯一保护创建首个管理员并消费 token；初始化后关闭入口，不开放远程抢占，不打印密码/令牌。

Web 使用 HttpOnly + SameSite=Strict 会话 Cookie、HTTPS Secure 与写请求 CSRF；密码 argon2id，仅接受本系统写入的完整格式（v19、m=65536 KiB/t=1/p=4、16字节salt、32字节digest及规范无填充Base64）。校验前限制编码总长度并逐项匹配固定参数，存储值不能选择运算内存、迭代、并发或派生输出长度；异常/不支持格式返回无效凭据，不改写原哈希、不签发会话或Cookie。会话与 Bearer Token 只保存哈希，禁用/过期立即生效。

会话与有期限 API Token 必须在数据库读取过滤及查询后校验中仍有效；expires_at 等于当前时间即过期。拒绝过期认证不能更新时间/活动记录、签发 Cookie 或新会话。无期限 API Token 的 NULL 有效期合同不变，禁用用户的两类凭据均不能认证。

初始化/登录的请求体上限为16KiB，整个有界正文必须读取后才能解码；每个Router共享两个密码计算槽位和容量10、每6秒补充一次的准入预算，不以可伪造转发头或变换地址扩充预算。认证响应禁缓存。密码计算在事务外完成，签发会话前锁定并重读当前账户，禁用或密码变化拒绝签发，角色以锁后记录为准。退出只有确认数据库会话已撤销后才清Cookie；读取/删除失败返回不可用，不伪造成功。系统状态保留成员与read Token的基础读取合同，只向当前管理员返回data_dir。

索引器、下载器与媒体服务器的列表/创建/更新响应只返回安全URL展示值，移除userinfo并遮蔽秘密查询参数，普通参数及顺序保留。编辑索引器携带config_version对应的expected_version，下载器/媒体服务器携带updated_at对应的expected_updated_at。展示值与当前安全URL精确一致且版本匹配时，事务内保留原URL；显式新完整URL沿原合同更新，若提供版本也须匹配。旧版本或缺少必要保留版本返回409，修改过的占位URL或以占位值创建返回400。先锁当前活动管理员，再锁配置；留空密码/API key保留原密文，非空才替换。

### 11.2 密钥与出站请求

敏感配置用 XChaCha20-Poly1305 加密，主密钥来自 PULSE_SECRET_KEY 或本地 secret.key（0600）；单独备份。磁盘密钥仅接受 32 字节、0600 的普通文件，数据根内受约束打开且不跟随密钥符号链接；损坏、不可读、权限不符或特殊文件拒绝启动，不覆盖、不改权限、不生成替代密钥。首次创建先写入并同步0600暂存文件，再以不能替换既有目标的原子硬链接发布，并同步目录；并发创建读取同一获胜密钥，正常退出前清理自身暂存。文件系统不支持所需发布/同步时拒绝，不能回退到覆盖写。密钥初始化失败不得报告 ready。API 只返回是否配置与脱敏显示，替换密钥使用明确写操作。

数据根逐级以NOFOLLOW打开并核对返回目录能力的device/inode；父分量不能绕过符号链接限制。磁盘主密钥和本地锁的最终文件分量通过已打开数据根目录FD的内核openat打开，并强制NOFOLLOW/NONBLOCK/CLOEXEC；只接受单文件名，拒绝NUL、跨目录或点路径。锁文件必须为单链接、0600普通文件，取得非阻塞flock后再次核对文件名与FD身份，再持久写入PID/时间；争用诊断从同一FD有界读取，错误不回显数据路径。无效锁拒绝启动且不触发主密钥初始化，不改写链接目标、权限或特殊文件；释放不删除锁inode，重复或并发释放不关闭别的所有者资源。原生根Close只释放一次，回收边界检查先规范化相对路径。

日志/错误/通知脱敏 apikey、passkey、token、Cookie、DSN 和下载链接。出站主机来自管理员配置/明确白名单，限制协议、重定向、响应体与解析大小；私人下载器/NAS 地址允许显式配置，不能接受普通成员任意 URL 代理或跨主机取凭据。

内置 NexusPHP/Torznab/Universal RSS 的原生取源只允许配置端点的同 scheme/host/有效端口，拒绝 URL userinfo、fragment 及跨源候选；管理员明确配置的回环/私网地址仍可使用。客户端私有副本禁止 HTTP 重定向，NexusPHP 的 Cookie 不向其它来源发送，响应体上限 10MiB；请求/读取错误映射到 ports 安全错误，不附原始 URL、Cookie 或底层错误。配置的有效来源变更必须经过管理入口，返回的 RSS/HTML 链接不能授予其它来源的网络访问权。

普通电影/单集的HTTP候选不能直接交给下载器取源：存储链按candidate绑定的indexer及配置版本读取当前端点与解密凭据，原生有界取源并检查现有v1 metainfo/infohash合同，再执行最终PrepareSubmission重验需求、来源、权限和租约，最后发送精确字节。magnet保留BTIH、BTMH及hybrid身份，拒绝重复/未知身份及xs/as/ws等额外HTTP元数据来源。两个下载器的通用Add均拒绝HTTP URL。qBittorrent Add和Transmission RPC关闭GetBody隐式重放；收到请求后的连接或HTTP/2 reset维持不明结果对账，不能让一次Do隐藏两次修改。Transmission的409 session协商仍显式构造新请求，不能混同执行结果不明后的重试。

结构化日志在输出前解析 LogValuer，并对预绑定属性、Group 和受支持的映射递归检查敏感键；错误诊断和消息中的 URL 参数逐项脱敏，多个 URL、编码参数键、重复参数或非法 URL 路径不能取消保护。未声明字段结构的对象输出占位，调用方用明确属性或 LogValuer 暴露安全字段；递归深度限16，循环映射不能使记录进程失控，不修改调用方对象。这一行为不赋予原始底层错误直接进入 API 或通知的权限。

配置加载在路径规范化前拒绝空或纯空白数据根，应用构造再次校验后才能建立目录、锁和密钥。YAML 解析错误与非法配置值只返回字段/文件位置和格式要求，不挂接含原始标量的错误链，也不回显输入值；配置优先级与合法显式路径不变。

文件安全、提交恢复与外部幂等边界分别遵守[存储](#storage)、[获取](#acquisition)、[可靠性](#reliability)，不能用权限校验替代路径安全。

<a id="api"></a>
## 12. HTTP 与实时接口

基础路径 `/api/v1`，JSON；UUIDv7 字符串，时间 RFC 3339 UTC。API 规范先行：M0 验证 OpenAPI 版本与 mise 固定的 oapi-codegen 兼容性，再使用同一规范生成 Go strict server 与 TypeScript 客户端，禁止手写重复类型。

订阅详情经 `SubscriptionDetailsReader` 返回单一RR只读投影：先锁定并复核当前活动账户、实际角色和read scope，再读取订阅所有权、媒体、根和Wanted。成员无绝对根路径，真实不存在404，失权403，数据库故障/取消/超时503；账户锁等待的40001最多重建三次完整读取事务，命令及外部动作不重试。旧 `POST /transfer/scan` 和 `POST /indexers/{id}/checkin` 均已废弃，认证后返回稳定410 `ENDPOINT_RETIRED`，不读取客户端路径/站点或发送请求；分别使用未跟踪目录作用域的观察/识别/确认和持久签到配置与历史流程。

客户端 X-Request-ID 仅接受单个、1至128字节、首字符为 ASCII 字母数字、后续为字母数字及 `._-` 的标记；非法或多值输入生成新 ID，响应头与错误体共用。`/api` 及 `/api/` 下未知入口返回 JSON 404，不能回 SPA。固定生成器的 `task api-probe` 实际验证 strict Go、UUID、required-nullable JSON 往返及 TS 类型拒绝样例，兼容结果仅对这些样例有效；现行规范继续使用 OpenAPI 3.0.3。
- 游标分页默认 50、上限 200；过滤/排序为显式参数。
- 配置写入有 version/If-Match，冲突 412；ETag 必须对应实际配置表示，不把变化的进度当作不变强 ETag。动态状态命令校验当前状态/版本。
- 有副作用的业务命令使用 Idempotency-Key；响应 202 表示接受，返回稳定资源与 job ID，不表示完成。
- 错误采用 RFC 9457，稳定 code、request_id、字段错误；前端本地化，不直接展示底层异常。
- HTTP 错误统一返回 `application/problem+json`，以 `about:blank`、HTTP 标准 `title`、实际 `status` 和公开 `detail` 表示问题；`code`、`request_id`、兼容 `message`（等于 `detail`）和必要的字段 `details` 为扩展。规范中的 JSON 媒体别名保留生成客户端兼容，所有成员由同一 OpenAPI 生成。禁止将请求 URI 放入 `instance` 或传回底层异常；无法识别、格式损坏或超过 64KiB 的错误体使用公开 HTTP 提示。成功响应和 SSE 不缓冲；`readyz` 的 503 保留类型化健康报告。健康端点仅接受 GET/HEAD，其余方法为 405；HEAD 无正文。

**家庭媒体请求与审批（M9-05，schema107）**：`media_request`与正式订阅分离。成员提交只保存TMDB媒体类型/ID、明确季范围、特别篇选择和原始pending/v1快照，不请求元数据、不创建Wanted、下载、整理或文件事实；现有直接订阅权限保持其原合同。成员只读本人请求并可撤回，管理员可拒绝或批准；状态仅允许pending到approved/rejected/withdrawn一次迁移，终态不可重开、删除或截断，有请求历史时拒绝107降级。原键位于当前账号命名空间，外人命令键404，明确外人对象或游标403；Token（包括空scope）不能进入该Cookie入口。

批准仅接受执行配置（目标根、品质策略、站点、下载器），媒体身份、归属和季范围从已锁定的请求派生。专用创建仓储复用正式订阅创建规则，网络/元数据准备在事务外，准备前与提交时核对当前管理员、当前请求账号、pending版本与原键。批准、订阅、真实稀疏集号的Wanted、两种成功审计/事件、正式创建原结果同事务；延迟约束验证订阅归属和原创建账本，约束执行后再检查双方实时权限，无写入尾巴。既有暂停订阅不激活、不换绑定或改变用户跳过意图。批准只使正式调度消费可用Wanted，不直接调用Add。

请求命令与审批提供独立只读原键回执；历史提交回执始终为原pending/v1，当前状态另读。恢复先核对原结果，不因当前元数据或配置不可用而重新准备；缺失/篡改成功账本不重做。所有请求路由含错误响应均private/no-store，HTTP在鉴权后、生成字段绑定前严格拒绝重复键/大小写别名/多值/未声明字段，输入有界，未知下层错误归一Unavailable。页面在POST前持久化冻结命令，未知结果只显式GET恢复，404后可人工同键同内容重试；409经当前请求读回后可明确结束本地冲突，失败不换键。账号/角色、跨标签和生命周期围栏阻止迟到响应恢复旧身份或清除未确认命令；新模板与质量规则不提供请求审批授权。

| API 组 | 核心入口（字段与完整路由由 OpenAPI 定义） |
|---|---|
| setup/auth | setup status/创建、login/logout、me、Token；管理员 users |
| discover/media | 搜索、resolve 外部身份、详情/units、刷新元数据 |
| subscriptions | CRUD、pause/resume/archive、wanted、候选、立即搜索 |
| releases/downloads | 交互搜索创建/按 round ID 查询；下载创建、详情、retry/cancel |
| transfers | preview、创建、详情、approve/cancel/retry、人工 resolve |
| library | roots、scan、files、手动 match；删除订阅不删除文件 |
| integrations/policies | 内置类型/能力、各配置 CRUD/test、策略 revision/preview/rollback |
| jobs/inbox/audit | 状态、待处理、受控 retry/cancel、投递重放、审计 |
| system | 状态、设置；`/healthz`、`/readyz` 为最小无敏感健康信息 |

SSE 为 `GET /api/v1/stream`：job.progress/finished、download.updated、transfer.updated、subscription.updated、inbox.changed、integration.health，按对象权限过滤。

SSE 是 R0，仅有界内存缓冲。Last-Event-ID 只尝试恢复当前实例的缓冲，缺口/重启发 reset 并要求重查；不能把 event seq 或内存序号承诺为持久续传。客户端退出清理缓存，状态仍以 GET 为准。

**M1-10 活动实际切片（2026-10-14）**：`GET /api/v1/operations` 使用只读RR快照按需求对象范围返回持久摘要，active/review/all、最多50条和created_at/UUID keyset；游标绑定当前Actor/role/view，共享需求去重。列表与详情均区分未发送的排队、全需求撤回和物理unknown；已撤回逻辑需求不隐藏在途/unknown任务。失败503不返回缓存或私有存储字段。活动页选择操作接入已有受控撤回、原硬链接恢复和同目标单元修订；列表清空/退出清理cursor与未选择对象状态。

活动列表与详情的当前读取实现复用同一对象可见性：普通需求按用户归属，自动 TV 按执行授权的 owner/operation/unit/root 精确关联，管理员可查看其范围内的记录；当前账户、角色与显式 read scope 在同一个只读 RR 快照复核。`GET /api/v1/operations/{id}/timeline` 读取当前选定整理计划的真实步骤，默认50、最多100条，按步骤顺序和UUID进行keyset分页，游标绑定账户、角色、操作及计划。仅返回安全文件名、动作、状态与数据库时间，不暴露绝对路径、凭据或原始错误。列表、详情、时间线 GET 均只读数据库，失败503，读取本身不取得执行或完成许可。

schema93 的 `download_rate_observation` 保存可选的下载器字节/秒及专用显示比例，独立于冻结任务、原生观察 JSON 和历史成功回执。普通、自动 TV、整包与升级沿各自既有原生观察、物理槽及锁序发布；样本与原写回同事务，绑定任务身份/版本、当前下载器配置指纹及业务来源，并在提交前保持原 owner/token/有效租约检查。未知或无效速率保存空值，不能以旧成功或累计流量补造；速率通过十进制字符串传输，保留观测时间，超过45秒标为过期。自动 TV 进度只来自匹配的已验证字节，少一字节不可显示100%；其他比例明确标注下载器记录，不授予拥有或入库事实。整包与升级的显示比例使用独立样本，升级的阶段跟随真实跟踪结果，原任务即使冻结在 submitting 也不改写；索引器配置变化不取消对既存下载任务的物理观察，实际下载器或任务来源不匹配仍拒绝旧结果。

schema106 的全局传输速率独立于上述任务观察：qBittorrent读取原生`transfer/info`，Transmission读取`session-stats`，使用当前全部传输的瞬时上下行字节/秒，不从已跟踪任务或累计流量推算。部署显式启用后，由App持有3个有界消费者；每源仅一个活动采集任务，网络读取位于事务外，配置摘要、原owner/token及有效租约在写回和提交时复核。逻辑取消、租约过期或停机均不提前释放尚在执行的物理调用锁；过期任务只在原调用返回后恢复。失败记录本次状态与安全分类，保留最后成功值及其原时间，不把历史值当作当前成功。

`GET /api/v1/download-clients/global-rates`仅允许当前有效管理员用户会话，在只读RR快照再次复核数据库权限，成功及401/403/503均禁止缓存。接口不调用下载器、不创建采集或业务任务，不授予发送、完成或整理许可。每个方向独立保留未知与已知零；当前配置的全部来源均有45秒内有效值时才返回该方向全局合计。整数按任意精度合计后输出十进制字符串，历史10分钟按10秒格点取当时已存在的样本，失败、配置不匹配和过期形成缺口，不补零或用未来观测回填。首页仅管理员可见，账号/角色变化立即撤下旧数据，隐藏时暂停轮询，当前读失败不保留实时成功外观；最后成功值和时间单独标明。

公开`/api/v1/stream`的首个协议只发送`reset`与空JSON，连接/重连（包括旧Last-Event-ID）要求完整范围GET；成功写命令后和周期提示同样触发重查；精确POST /api/v1/subscriptions/{id}/upgrade-previews只读比较不广播reset，避免销毁自身比较结果，不把例外扩展为任意同后缀路由。仅有界进程内提示，不承诺业务事件重放、缓冲续传或完成事实；成员/read Token流不携带对象、payload或日志。旧`/events`管理员日志单独限制。App停止时先关闭事件中心，再排空HTTP，断开/退出移除订阅，客户端清除查询与连接状态。人工处理与retry/cancel只沿对应业务窄合同执行；当前已验证范围见下述原提交重试及开发计划。

原提交的人工重试使用 GET/POST `/api/v1/operations/{id}/submission-retry`。预览不改变状态；当前活动成员/管理员的用户会话须提交原 job UUID、完整输入 stamp、明确确认及 Idempotency-Key。窄事务锁当前用户、原任务和业务范围，检查原 job 已耗尽 dead、完整单电影冻结证据、本人原根/单元活动需求、目标/配置/输入版本一致，且没有任何 download_task；原 job 重新入队、token 推进，原 payload/尝试历史保留，审计、事件及回执原子提交，当前用户校验先于回执恢复。物理 submitting/unknown/tracking/completed、TV/不完整旧合同、非耗尽、变化或溢出不授予重试。执行沿现有物理槽/租约 worker 复核；排队不表示 Add 完成。界面丢回执只恢复原键及无秘密命令。撤回、硬链接恢复和目标修订各沿原窄合同；unknown 无强制重发/删除/解除保护入口。

<a id="frontend"></a>
## 13. 前端边界

React 19 + TypeScript strict + Vite；react-router-dom，React Context 管认证会话/全局状态；openapi-typescript + openapi-fetch 严格类型化 API 客户端。设计系统采用现代化原生 CSS 变量令牌，Lucide React 图标体系。版本由 web/package.json 与 pnpm-lock.yaml 固定。

页面与设置标签使用静态可分析的React.lazy动态导入，仅在对应路由或标签挂载时下载。加载中提供可访问状态，模块失败显示可显式重新加载页面的错误；不自动重放命令。内容边界按账号、角色与路由/设置标签重建，身份切换卸载旧页面，迟到模块不能恢复旧管理界面。正式核心浏览器入口验证入口JS预算、未访问页面不下载、延迟模块下退出/换账号和加载失败后的重载恢复。

主导航为发现、订阅、活动、媒体库；首页优先待处理，设置为次级入口。活动统一下载/整理/任务时间线；详情显示接受/拒绝原因、冻结路径预览与期望/实际差异。固定内置集成使用类型化表单，不建通用 JSON Schema 表单引擎。

活动列表、详情和步骤从生成 API 读取，账号/角色/操作切换同步隐藏旧数据并隔离迟到响应。可见页面每五秒刷新持久观测，不可见时不发起观测查询；步骤分页由用户显式请求。进度与速度显示来源、观测时间以及未知/过期状态；时间线只呈现已存步骤。相同身份与版本的后台读取保留已经确认的人工上下文，显式刷新、权限拒绝、读取失败或计划变化撤销新操作就绪。人工审批、撤回、恢复和目标修订继续使用各自窄用例、原幂等键及回执，不由轮询启动命令。

发现 GET `/api/v1/discover` 使用显式榜单：trending、movie、tv、top_rated、now_playing，热映请求原生 `movie/now_playing`，页码1至500。会话member/admin及具read scope的用户Token可读；非法输入400，缺少来源/原生失败或坏响应503，只有真实有效空列表200，不猜电影身份。页面按账号/角色/榜单/路由隔离读取，切换立即隐藏旧结果、取消并围住迟到响应，权限拒绝清旧数据；题材仅用提供者标签，不从简介推测。榜单显示不授予订阅、审核或下载执行权限，既有命令与原键恢复入口保留。
元数据详情只读入口区分真实不存在404、非法输入400、无读权限403与来源不可用/坏响应503。电影与TV按明确类型和正TMDB ID读取；演员 `/api/v1/media/tmdb/{media_type}/{id}/credits`、季集 `/api/v1/media/tmdb/tv/{id}/seasons/{season}` 通过独立窄用例复用当前动态TMDB配置与物理请求预算。演员按来源顺序最多24项，画像只能用固定TMDB图片投影；空演员与读取失败分别显示。季集必须为完整来源数组，保留真实正编号与缺省日期，不从episode_count推算E01…En。演员和单集浏览资料不写入旧创建冻结JSON，也不授予下载或业务身份许可。弹窗按账号/角色/类型/ID同步隔离旧结果并围住迟到响应；自定义选集仅在所有选中季完整读取且所选集仍存在时允许提交，调用时再次验证；整剧模式仍由服务器权威准备季集。一键订阅和资源检索复用原创建命令/审核入口，未知回执保留原键，不以关闭弹窗取消或重发。
媒体库 GET `/api/v1/library/catalog` 在只读 RR 快照中按共享媒体根模型校验当前活动用户/role及显式 read Token；默认50、最多100条，file/unit UUID keyset游标绑定Actor/role。仅返回未退休文件/单元、标题/年份/季集键、根名称/健康、相对路径、十进制字符串大小及有效 imported/confirmed 记录，不返回绝对根路径、下载引用或凭据。错误使用安全完整RFC9457；浏览不授予执行或品质许可。MediaView移除演示影片/容量/inode/NFO与假刮削成功，按已加载记录分组电影/剧集季集，刷新保留展开状态，海报缺少持久数据时明确缺省；失败清旧文件，401清私有应用，退出取消请求。媒体根健康、入库及服务器确认记录独立呈现，不推断刷新完成或实体文件可用性。
首页事实由独立只读端口提供：`/api/v1/library/overview` 在 RR 快照复核当前活动账户、角色与显式 read scope，按共享媒体根模型聚合当前有效 imported 事实。作品、TV单元和文件各自去重，多集文件不重复计文件，多根不重复计全局作品；confirmed 不单独构成入库，根离线不把已登记数量清零。近期入库先取同一文件/根的历史首次 imported 时间，再关联当前有效映射；纠正映射不制造新的到达时间。计数与字节用十进制字符串，不返回绝对路径、凭据或执行许可。

管理员首页的全局速率读取 `/api/v1/download-clients/global-rates`，只展示已持久化的原生下载器上下行观测，不累加已跟踪任务推算全局流量，也不虚构带宽容量百分比。每个方向独立合计；缺失、失败或过期观测保持未知，历史缺口不补零。采集仅在显式部署开关启用时由有界后台任务执行；GET、刷新和页面导航不调用下载器或调度采集。

首页待审批摘要 GET `/api/v1/media-requests/summary` 只允许当前有效管理员Cookie会话，事务内复核实际账户角色，从完整请求集合计算精确pending数量和数据库观察时间，数量以十进制字符串返回；零、未知与503分开。GET `/api/v1/media-requests?state=pending` 同样仅管理员可读，UUID keyset分页排除终态；游标指向的请求在页间变为终态时仍保留其原排序位置。摘要、列表及错误响应均private/no-store，不做元数据、文件、下载器I/O或业务写入。首页可见时每15秒读取摘要，隐藏停读并取消进行中的请求；45秒未更新即隐藏旧计数，账号/角色变化同步遮蔽并隔离迟到结果。徽标只导航至待审批筛选，批准、拒绝和撤回须显式执行现有版本/原键命令，不能由刷新或筛选自动触发。

容量是已有扫描物理槽内对固定目录 FD 的同步 Fstatfs 观测，使用 Bavail 并验证范围/乘法溢出，取消后等待实际调用返回。独立 `root_capacity_observation` 绑定原 scan_run，在原封存事务中检查原根配置、generation、物理身份及有效 owner/token；采样失败不写零，不修改根冻结行或授予健康/基线。只展示最新扫描的可信完整样本，不以更早成功覆盖最新失败/部分/运行中；无样本或根不可用显示未知，超过30分钟标明过期并保留采样时间，不跨根累加可能共享文件系统的容量。

可信建档事务将 TMDB 图片路径存入独立 `media_artwork`，绑定精确 movie/TV 命名空间、外部身份和当前媒体版本；GET 不查上游，不用推荐流代替入库记录。固定 HTTPS TMDB 图片源以外、身份歧义或版本不符均不提供海报，页面明确缺省；海报是显示缓存，不参与历史授权或品质判断。

`/api/v1/dashboard/activity` 在单个只读快照内复核账户，普通需求按原用户归属，自动TV需求按持久执行授权的 owner/operation/unit/root 关系确定可见范围，管理员可读取全部。只读状态投影保留排队、未知、复核和整理阶段；普通进度仅显示有效的已存下载器百分比，自动TV比例必须来自最新匹配的可信观测与已验证字节，少一字节不能显示100%。累计下载流量不推算比例，较新的失败/复核不能回退到旧成功；任务记录时间与实际TV观测时间区分。首页活动在可见时定期读取，GET 不发起下载器/文件I/O；账号或角色变化同步隐藏旧数据并废弃迟到响应。站点配置摘要仅管理员会话请求并展示，成员不发起该管理GET；刷新等待相关可见读取真正完成，部分故障保留同身份旧数据并标明失败，401/403清除拒绝访问的数据，错误不伪装为空成功。


SSE 驱动查询失效，不维护第二份业务状态；破坏性操作确认，可逆操作才乐观更新。支持深浅主题、响应式、键盘与 WCAG 2.2 AA，颜色不独立承载状态。命令面板/PWA 不阻塞首个闭环。

应用壳体折叠与移动抽屉只保存本地展示状态，不复制业务状态或授予权限。抽屉关闭时导航不可交互；打开时背景隔离、锁定背景滚动、限制键盘焦点，Escape/关闭后恢复明确触发按钮；路由、身份或响应式断点变化关闭抽屉。桌面折叠不隐藏链接的可访问名称，移动端保持完整标签。首屏深色背景在HTML中先于异步CSS生效；正式体验验收读取当前构建，覆盖桌面/平板/移动与减少动效，状态和进度仍以开发计划为准。

web/src 按 api/app/features/ui/styles 分层；单组件建议不超过 400 行，功能 i18n 与查询就近。构建产物 go:embed；开发 Vite 代理 API。SPA 回退只处理页面路径，未知 API 不返回 index.html。

<a id="engineering"></a>
## 14. 工程与测试

工具版本唯一来源 [.mise.toml](../.mise.toml)，精确固定；Go 库由 go.mod/go.sum，前端库由 package.json/pnpm-lock 管理。GOTOOLCHAIN=local，保留模块/完整性校验，不全局关闭 CGO；发布构建可单独关闭，race 测试保留 CGO。

- 构造函数显式依赖；context 为阻塞操作首参数，不存入 struct。
- 时间、UUID、抖动随机数通过 Clock/IDGen 等注入；domain 接收值，不导入平台时钟。
- 错误包装并用 errors.Is/As；稳定领域 code；不 panic 处理业务失败。
- 禁止业务裸 go func；由持久任务或有 owner/取消/等待的受控运行时创建后台工作。
- slog 结构化日志带 request/job/subscription/indexer ID；指标不以无限增长 job ID 作 label。
- depguard 强制[依赖 DAG](#dependencies)；golangci-lint v2、gosec、govulncheck；建议文件 <500 行、函数复杂度 ≤15。
- 生成代码提交，CI 重新生成检查差异。变化先更新规范/迁移/本文，不新增平行设计文件。

| 层 | 检查方式 |
|---|---|
| domain | 表驱动、属性测试、解析黄金样本；V1 至少 1000 条实际文件名 |
| store | 真实 PG18，每套独立临时数据库/迁移；不能用替代 DB 模拟锁 |
| adapter | httptest、录制 fixture、相同能力的契约套件 |
| usecase | fake 外部适配器 + PG18，故障注入与回滚验证 |
| scenario | 完整 app、独立 PG、临时根、fake 外部服务、重启与响应丢失 |
| web | Vitest/组件测试、Playwright 初始化到入库冒烟 |

CI：mise install → 冻结前端依赖安装及 Go 模块下载/校验 → 前端 typecheck/build（先生成 Go embed 所需 web/dist）→ 生产依赖拒绝探针/lint → gen-check/生成器兼容 → 必过 govulncheck → 私有 PG unit/store/adapter/scenario/race → 原生认证 web → build/version。漏洞扫描不能因数据库不可达返回成功或静默跳过，非零阻止验收；报告区分代码可达、导入包及未调用模块级条目。PG 测试不得回退共享集群或因缺少 DSN 静默跳过；各入口只在 /data/temp 自有目录运行并验证资源清理，失败阻止验收。非 root 产物先支持 Linux amd64/arm64；固定 UID/GID 或容器 --user，不承诺 distroless 自动 PUID/PGID 切换。桌面平台与云盘另验证，不作为 V1 发布门槛。
最终容器验收另由CI原生amd64/arm64任务执行，依赖源码构建/漏洞门禁成功；从干净提交构建镜像并校验架构及revision，显式启用实际启动/认证、离线安装与受保护升级、PG18卷替换测试，失败或取消均执行本任务资源清理。任务不推送镜像，不将源码回归中的容器门控跳过作为容器验收通过。

<a id="operations"></a>
## 15. 运行、迁移与备份

### 15.1 开发数据库

```bash
mise trust && mise install
# 手动创建不提交的 .env，设置强 PULSE_POSTGRES_PASSWORD。
# PULSE_POSTGRES_PORT 可选；版本不重复写入 .env。
mise exec -- docker-compose --env-file .env -f compose.dev.yaml up -d postgres
mise exec -- docker-compose --env-file .env -f compose.dev.yaml exec -T postgres \
  psql -U pulsemedia -d pulsemedia -c 'SHOW server_version;'
```

[compose.dev.yaml](../compose.dev.yaml) 只启动开发数据库，绑定 127.0.0.1。PG18 卷目标为 `/var/lib/postgresql`，默认 PGDATA `/var/lib/postgresql/18/docker`；不用旧主版本卷布局。Docker Engine 由宿主提供，mise 管 CLI 和 PG 工具，也可由非 root 用户直接 initdb/pg_ctl。

Go 应用单独读取 config.yaml 的 database.url 或 PULSE_DATABASE_URL；Compose 的 .env 不自动注入应用。仅回环开发允许 sslmode=disable，生产 verify-full + CA；DSN 密码 URL 编码。部署配置重启生效，系统设置/版本化策略/集成配置由数据库管理。

### 15.2 连接与迁移

初始 MinConns=2、MaxConns=8；连接超时 5s，statement_timeout=30s、lock_timeout=2s、idle_in_transaction_session_timeout=30s，迁移/备份独立配置。READ COMMITTED + 明确锁/唯一约束/短事务，禁止事务内外部 I/O。开启 fsync/full_page_writes/synchronous_commit 与 autovacuum。

生产 DML 角色仅 schema USAGE、表与序列所需权限；迁移使用独立 DDL 角色。goose 使用显式本地方言 provider，禁用全局迁移注册；整次操作仅一条物理连接，在连接建立时取得 session advisory lock，版本表预检、DDL、版本写入和结果读取均在同一持锁会话内完成。竞争者拒绝后显式关闭已打开的连接；会话失效不自动重连或重放，返回失败后需独立核验数据库再发起恢复。物理连接关闭才释放锁，不用普通业务池或事务池代理承载。生产显式 migrate 后启动，应用只校验 schema；开发可开自动迁移。CREATE INDEX CONCURRENTLY 等单独声明、测试。
离线生产安装/升级入口为 `cmd/pulsemigrate`（`task build-migrate`）；镜像自带该命令、`pulsebackup`、迁移文件和 PG18 客户端。显式0600连接文件提供独立DDL身份，持原应用数据目录锁至结束，再调用上述迁移器；空数据库安装不生成主密钥，已有schema含重复调用需先认证配对数据库/key加密档案、当前schema与原主密钥、dump摘要，并通过PG18 pg_restore离线完整解码。恢复点来源及时间由操作人通过备份/恢复记录确认；升级保持数据库恢复隔离和不可变历史。多文件迁移失败时先核验已提交版本，再选择新库恢复，不自动重放不明DDL。具体命令见[部署指南](deployment.md#已有数据库离线升级)。


### 15.3 生命周期与可观测性


**配置来源连接诊断**：管理员Cookie及CSRF保护下显式POST固定`tmdb_api`、`tmdb_image`、`fanart_api`、`tvdb_api`目标，不接受任意URL。复用生产加密配置快照、传输、预算和超时；未配置/停用不请求。Fanart固定读取电影550的JSON，TVDB仅执行一次v4登录并丢弃令牌；不写媒体、文件或获取任务。禁止重定向与Cookie传播，返回安全类别和耗时，主机响应与图片完整取回校验分开。预算在物理响应体关闭后释放，准入后及关闭后重验配置版本/密文摘要和当前权限，变化则丢弃结果；浏览器账号切换取消并阻断迟到展示。

启动：配置 → 本地数据目录进程互斥/密钥自检 → PG/schema → 缺失类型的默认根安全准备/登记 → 恢复规划 → Outbox/Runner → Scheduler → HTTP。固定本地应用数据目录防同目录双启动；不同主机/不同数据目录接同库不受支持。未知物理步骤接管须确认旧进程退出，PG 租约不是跨主机文件锁。
缺少某媒体类型的配置根时，默认根由窄启动用例准备并原子登记；文件操作在事务外，用目录 FD 限定固定子目录和标记，拒绝链接与特殊文件，不覆盖原文件。新根保持未完成扫描基线，完整扫描成功后才允许依赖该基线的获取。已有根的状态与历史保留，离线根不触发自动创建替代根。首次必需初始化失败时不开放 HTTP 或工作循环，故障可重试且不伪造完成；运行期的根失联仍按局部健康故障处理。

关停：停 HTTP 接收与 Scheduler → Runner 停认领、等待在途 → Outbox → DB。HTTP 等待预算 15s、任务 30s；超预算保持未决步骤，等待预算不是外部完成/退出证明，不移交仍占用的物理资源。

运行时仅可启动一次，关闭后不得再次启动；同数据根的重新启动须重新构造并获得本地锁。外部 Close 与信号关停共用取消、等待及一次性释放：先标记未就绪、停止新请求与工作认领，再等待监听循环、所有已接受 HTTP 处理器及工作循环结束，最后关闭 DB 和释放本地数据根锁。初始化失败也走相同资源释放路径。并发 Close 等待同一个完成结果，逻辑 HTTP 等待超时不能让处理器或不可取消 I/O 脱离所有权。

healthz 检查存活，readyz 检查 schema/DB/运行时就绪；单集成或根异常集中显示，不把局部失败变为全局服务死亡。指标观察池等待、锁等待、队列年龄、端点预算、下载对账、扫描/整理耗时与 dead/stalled。

当前 HTTP 请求诊断通过结构化日志提供规范路由模板、有限方法集合、最终状态码、响应字节数、耗时与 request_id。路由从注册 mux 在鉴权前解析，参数值和查询原文不进入路径字段；未匹配路由固定为 unmatched，自定义方法归为 OTHER。日志包裹异常恢复，空响应记为 200，重复响应头不覆盖首次最终状态，SSE Flush 保持可用。日志由部署日志设施聚合。同一记录器按注册路由模板、有限方法与最终状态累计 `pulse_http_requests_total`、响应字节、耗时直方图及在途请求；指标实例归 Router 所有，无全局注册、后台轮询或动态用户标签，进程重启归零。`GET /api/v1/system/metrics` 提供 Prometheus text 0.0.4，仅当前管理员会话或具 `read` scope 的管理员 Token 可读，scope 不授予管理员身份；所有结果 private/no-store。App 注入窄 `RuntimeMetricsReader`，用例先检查权限，store 在两秒截止内以 sqlc 只读语句聚合 job/outbox 的活动/失败状态及最老可执行等待、过期逻辑租约，并读池连接/获取/等待计数；不访问外部集成，不写任务、业务、审计成功账本或回执。成功历史不计积压，未来 not_before 不增加 ready age，采集失败返回503而不输出虚假零值；租约过期仅为观察，不授权接管或重试。数据仅为采样时刻观察；HTTP流式耗时按整条流寿命记录，在途数包含当前采集请求，直方图告警应排除SSE路由。锁等待另从pg_locks的未授予锁和waitstart聚合，限定当前数据库及session_user登录角色，返回等待连接数和最老等待秒数，不返回SQL、PID、对象或账号标签；不要求pg_read_all_stats，不把查询年龄当锁等待。各观察顺序读取，共用两秒截止，不保证跨查询原子快照；一个子采集失败则整次503。短于采样周期的等待可能不可见。索引器预算同样只读聚合已有next_query_at/rate_limit_until时间事实，按open/interval/cooldown互斥计数（cooldown优先）并给出最大剩余延迟；健康状态不参与时间分类，open不授权执行，也不证明物理槽可用。App另持有零值可用、并发安全的固定九阶段记录器，覆盖普通下载提交/对账/观察（含轮询及Kind=ordinary的目录监听回调）、扫描、下载整理和本地复用worker调用；用单调耗时记录返回次数/累计秒数/固定桶直方图与在途数，调用尚未物理返回时取消不结束计时。返回含unknown/review/error等结果，不是成功率；计时包括调用内部等待，不包括排队、后续worker记账或循环睡眠。App重建清零，无后台采集、动态对象标签或新执行许可；HTTP拿到DB快照成功后才附加当前阶段快照。专用TV/升级等worker、缺失确认、直调用例以及下载器/元数据的内存预算不在本批覆盖范围。此端点仍不是完整业务可观测性。


人物关注、豆瓣想看与CookieCloud生产周期扫描由App分别注入实例级ScheduleLimits：整批最多30秒，单项原子事务保留既有15秒上限；失败按配置ID/账号及版本冷却5/10/20/40/60秒，最多1分钟。成功清除冷却，新版本立即重验；最多保留4096项，达到上限时淘汰最早可重试项，故障范围极大时可能提前重新尝试，不能据此推导执行权限。取消不记录为配置失败，不跳过未完成游标；扫描器重建清空冷却，数据库围栏继续保证去重和授权。每个扫描器独占控制对象，不共享可变状态。现有阶段指标另覆盖person_follow_schedule、douban_wish_schedule、cookiecloud_schedule；pulse_work_stage_failures_total记录非调用取消的失败周期数而非失败配置数。每个worker每分钟最多输出一次固定类别的调度错误，不输出错误原文、对象身份或凭据；指标保留所有失败周期。管理员只读观测不触发重试、解除冷却或恢复授权。

建议活动下载每 30s/空闲 5min 对账，事实到期 10min、全库 6h + 文件监控、投影每日/按需重算；监控只是加速，完整核对不可省。

### 15.4 备份恢复

离线工具入口为 `cmd/pulsebackup`，通过 `mise exec -- task build-backup` 构建。`export/restore/status/reconcile` 全部持本地数据目录锁；显式连接由 0600 文件提供，同主版本 PG18 客户端通过自有 service/PGPASSFILE 连接，保留 TLS、CA、客户端证书与主机身份校验，不将 DSN 密码放进 argv。数据库 custom dump 与主密钥分别用标准 tar/manifest/age X25519 流式加密，以恢复 UUID、schema、dump/key 摘要绑定；0600 临时 spool 有界使用内存，恢复先读取认证 EOF 再创建目标库。导出核验原主密钥能解密数据库密文，恢复后再次核验；只创建显式不存在的新库，schema 验证/迁移及数据库隔离成功后才无覆盖发布主密钥，失败清理本次创建的库，清理失败明确报告隔离目标。命令与操作步骤见[部署指南](deployment.md#3-数据库备份与恢复)。

备份加密至少保留 7 份；主密钥单独安全保管。dump 不含媒体文件、密钥、全局角色/服务器配置，不能复制运行中的 PG 数据目录冒充备份。迁移前确认可用恢复点，无法备份则拒绝继续。

切换：停应用/确认物理执行已结束 → 新空库 restore → schema/约束/数据/解密验证 → 持原数据目录锁激活数据库隔离 → 下载器与文件及外部孤儿逐项对账 → 明确解除隔离 → 改 DSN 启动。schema83 的恢复隔离持久保存在数据库，App 在媒体根准备、HTTP 和全部 worker/scheduler 启动前拒绝隔离库，Readyz 同样不放行；换数据目录或配置不能解除。离线解除须持数据目录锁，匹配恢复 ID、记录操作人和三类对账说明；存在排队/执行 job、未完成 delivery、open/ready 通知批次、活动下载或仍可调度的升级观察/跟踪工作时拒绝，不能通过等待冷却期暂未满足而判作已对账。不改写旧租约、物理许可、unknown 占位或任务历史；激活/解除与不可变审计同事务，已有恢复历史禁止降级移除。正常未恢复数据库保持原启动合同。严格 RPO/RTO 另由运维配置 WAL/PITR；主版本升级独立 pg_upgrade 或 dump/restore 演练。

<a id="implementation"></a>
## 16. 实施边界与验收

阶段拆分、任务依赖、排期、阻塞与完成记录统一见[开发计划与进度](development-plan.md)。架构文档只约束实施安全边界，不维护第二份任务台账。

集成测试数据库必须显式通过 `PULSE_TEST_DATABASE_URL` 提供并验证 PostgreSQL 18；缺失、配置错误或连接失败是验收失败，不能自动连接共享服务、创建共享 `/tmp` 集群或静默跳过。`task test`/`task test-race` 由私有集群入口在 `/data/temp` 创建本任务的数据库、Unix socket 和临时构建目录，支持当前非 root 用户运行；结束先等待或停止自有子进程，再验证自有测试库清理并停止本集群，只删除本任务目录。CI 先安装各自锁文件约束的依赖，校验生成漂移、生产依赖边界、前后端、真实 PG/race 和内嵌浏览器行为，任何必要检查失败都阻止验收；生产依赖规则的外部集成测试组装例外须逐个明确，不得泛化关闭规则。

必须按安全纵向切片推进：先完成媒体库基线和电影手动闭环，再启用订阅自动化；第一条真实获取就具备原子占位、持久任务、最小 Outbox 与 R3 观察恢复，不能推迟到发布前补做。每个切片同步 API、必要 UI、迁移和故障测试，不先搭空平台或全部未来功能表。

### 必须覆盖的故障场景

1. 双连接 job/delivery 认领，旧 owner/token/过期拒写，业务/job/event/幂等结果一起回滚。
2. 相同 Actor/key 重试与不同请求冲突，暂停/权限撤销/配置改变的提交边界。
3. RSS/手动、两个候选、整季包/单集竞争；未知 Add 结果不释放、不换资源盲发。
4. 同名查询新执行、旧轮次失效、分页续传；一个端点 429/慢响应不拖住其它。每次聚合最多四个 endpoint worker，由调用方传递取消并等待所有物理调用返回后 join；忽略取消的 I/O 未返回前不得释放该端点物理许可。查询 ID 按原端点/分页顺序汇总，默认每端点预算、跨实例物理互斥、四页上限、分页等待及最终同一快照版本重验仍生效。
5. 多策略 rank 重算、目标根过滤、多集文件、旧确认失效、文件同路径替换。
6. 初始基线、部分扫描、NAS 失联/换挂载、扫描重放/并发入库与大批缺失保护。
7. R3 每个 started/外部动作/校验/DB 提交之间崩溃，完整内容与路径安全，卡住 I/O 不产生第二写入者。
8. Outbox 迟提交、同聚合顺序、dead 阻塞与渠道独立重放；发送成功落库前崩溃符合至少一次语义。
9. 备份恢复到空库、解密与角色验证、外部孤儿/账实对齐；不覆盖原库，不立即新抓或重放。

发布目标：引导至首部电影入库 ≤10min（不含下载）；每个完成可追溯到文件；1000 订阅/10 万文件/10 个端点基准，列表 P95 目标 <200ms、Go 空闲内存目标 <100MB；环境与 PG 资源预算单独记录，不把目标当测得结果。

<a id="verification"></a>
## 17. 验证边界

当前已验证、未验证及交付证据统一记在[开发进度](development-plan.md#progress)。临时合同原型、旧草稿 DDL 或最小索引夹具通过不等于完整迁移/业务服务完成；fake 根身份/Stat 输入不证明真实 NAS 行为，构建成功不证明恢复安全。

发布验证必须覆盖生产 UoW/角色/升级、真实下载器响应丢失、NAS 阻塞/挂载切换、完整路径与权限安全、性能和密钥恢复。未具备环境时明确登记限制，不能把未运行场景当通过。

架构变化在本文原位置更新，不新增复审/建议/ADR 平行副本；实施状态只更新计划文档。规范或迁移与本文冲突时先明确修订，不能靠历史讨论推翻现行合同。
