# 真实 NFS 验证入口

先确认隔离客户端能执行内核挂载，再接入 NFS 服务。实际环境证据和 M4-01 状态见 [开发计划](../../docs/development-plan.md)，存储与恢复合同见 [架构规范 §7](../../docs/architecture.md)。

```bash
mise exec -- task m4-nfs-preflight
```

镜像摘要统一维护在 `.mise.toml` 的 `PULSE_M4_NFS_PROBE_IMAGE`。预检直接使用本机 `podman`，要求镜像已存在；它在独立网络、PID 和 IPC 命名空间内检查私有 tmpfs 挂载能力与内核 NFS 客户端，保留默认安全策略。容器只增加 `SYS_ADMIN` 和 `no-new-privileges`，不绑定宿主路径，不改变宿主挂载、模块、网络或 nfsd。

退出码 77 表示环境前置条件不满足，98 表示资源清理失败。成功仅说明具备尝试 NFS 挂载的条件。预检按完整容器 ID 和 owner 标签核对归属，结束时清理自己的容器和临时目录，保留现有五项 M4 服务。

## 在允许挂载的隔离客户端中检查生产文件操作

环境负责人需提供允许私有内核 NFS 挂载的沙箱。在该环境部署隔离的 NFS 服务，将其导出挂载到客户端容器内，并准备项目与 mise 工具链。当前入口尚未包含 NFS 服务端和故障控制器。

```bash
PULSE_M4_NFS_ROOT=/data/temp/owned-client-root/nfs-mount \
  mise exec -- scripts/m4_nfs_check.sh
```

该命令要求真实 `NFS_SUPER_MAGIC`，在挂载中创建并清理唯一测试目录，调用生产 `LockRoots`、`Walk`、`Inspect` 和 `Hardlink`，核对文件、inode 与取消后的显式锁释放。普通 ext4/tmpfs 会失败；未提供挂载的日常测试会明确跳过。若目录 `flock` 不兼容，需评估生产物理独占合同，不应换成本地锁或使用 `local_lock` 掩盖问题。

## 继续完成故障矩阵

兼容性通过后，使用独立 PG18，沿 `App.Run → runLibraryWorkers → runLibraryConsumer → Scanner.ScanRoot` 注入实际故障，记录原文件与事实、根 generation/health revision、扫描 token/state，以及 job owner/token/lease：

1. 遍历时断开服务：扫描不得发布虚假完整基线，也不得制造文件或事实缺失。
2. 替换导出或挂载：旧扫描不得使用旧 generation/token 完成新根的扫描。
3. 阻塞请求跨过取消和租约到期：竞争消费者不得启动重叠的物理操作，恢复连通后需排空原调用并验证恢复。

故障控制器须先恢复连通，再等待内核 I/O、卸载和清理自己创建的客户端、服务端及网络。协议沙箱与具体 NAS 设备分别记录验收证据。
