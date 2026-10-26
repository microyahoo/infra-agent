# 远程存储服务器监控功能

## 概述

Infra Agent 整合了 storage-bot 的 SSH 远程执行能力，可以直接连接到物理存储服务器进行深度监控和日志分析。这解决了 storage-bot 无法查看日志的问题，并结合了 infra-agent 的智能诊断能力。

## 核心功能

### 1. SSH 远程连接

直接通过 SSH 连接到存储服务器，支持：
- ✅ 密码认证
- ✅ 私钥认证
- ✅ 连接超时控制
- ✅ 自动重连机制

### 2. 远程命令执行

在存储服务器上执行各种诊断命令：
- `ceph status` - Ceph 集群状态
- `ceph health detail` - 健康详情
- `ceph osd tree` - OSD 拓扑
- `df -h` - 磁盘使用
- `iostat -x` - I/O 统计
- `smartctl` - SMART 状态
- `ps aux` - 进程状态
- `netstat -tulpn` - 网络连接

### 3. 日志收集和分析 ⭐

**这是 storage-bot 缺失的关键功能！**

#### Ceph 日志
```bash
# 获取 Monitor 日志
/var/log/ceph/ceph-mon.*.log

# 获取 OSD 日志
/var/log/ceph/ceph-osd.*.log

# 获取 Manager 日志
/var/log/ceph/ceph-mgr.*.log
```

#### 系统日志
```bash
# kubelet 日志
journalctl -u kubelet -n 100

# docker/containerd 日志
journalctl -u docker -n 100

# 内核日志
dmesg -T
```

#### 日志搜索
```bash
# 搜索错误
grep -i 'error\|fail\|panic' /var/log/ceph/*.log

# 搜索 OSD 问题
grep -i 'osd.*down\|osd.*out' /var/log/ceph/*.log

# 搜索慢操作
grep -i 'slow.*request' /var/log/ceph/*.log
```

### 4. 智能诊断 ⭐

结合 AI 能力进行智能分析：
- 自动分析日志错误模式
- 识别常见问题（OOM、磁盘满、网络问题）
- 关联多个数据源（状态 + 日志 + 系统信息）
- 给出详细的解决方案

### 5. 综合监控

对比 storage-bot 和 infra-agent 的能力整合：

| 功能 | storage-bot | infra-agent | **综合后** |
|------|-------------|-------------|-----------|
| SSH 远程执行 | ✅ | ❌ | ✅ **增强** |
| K8s 资源分析 | ❌ | ✅ | ✅ |
| 日志查看 | ❌ | 部分 | ✅ **完整** |
| 日志搜索 | ❌ | ❌ | ✅ **新增** |
| AI 诊断 | ❌ | ✅ | ✅ **增强** |
| 自动巡检 | ❌ | ✅ | ✅ |
| 飞书告警 | ❌ | ✅ | ✅ |

## 配置方式

### 方法 1: ConfigMap 配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: storage-servers
  namespace: kube-system
data:
  servers: |
    # 格式: host:port:user:password:role
    192.168.1.10:22:root:password123:mon
    192.168.1.11:22:root:password123:osd
    192.168.1.12:22:root:password123:osd
    192.168.1.13:22:root:password123:mgr
```

### 方法 2: 配置文件

```yaml
# ~/.infra-agent.yaml

# 远程存储服务器配置
remoteStorage:
  enabled: true
  
  servers:
    - host: 192.168.1.10
      port: 22
      user: root
      password: "${STORAGE_PASSWORD}"  # 或使用环境变量
      # privateKey: "/path/to/private/key"  # 或使用私钥
      role: mon  # mon, osd, mgr, mds, rgw
      
    - host: 192.168.1.11
      port: 22
      user: root
      password: "${STORAGE_PASSWORD}"
      role: osd
      
    - host: 192.168.1.12
      port: 22
      user: root
      password: "${STORAGE_PASSWORD}"
      role: osd

  # SSH 连接配置
  ssh:
    timeout: 30s
    keepalive: 60s
    maxRetries: 3

  # 监控配置
  monitoring:
    checkCephStatus: true
    checkDiskUsage: true
    checkOSDStatus: true
    checkProcessStatus: true
    collectLogs: true
    logLines: 200
```

### 方法 3: 环境变量

```bash
export STORAGE_SERVER_1="192.168.1.10:22:root:password:mon"
export STORAGE_SERVER_2="192.168.1.11:22:root:password:osd"
export STORAGE_SERVER_3="192.168.1.12:22:root:password:osd"
```

## 使用示例

### 场景 1: 远程存储服务器健康检查

```bash
# 检查所有配置的远程存储服务器
infra-agent analyze --filter RemoteStorage --explain

# 输出示例：
# 🔴 192.168.1.11 (RemoteStorage)
#     Type: DiskUsage, Role: osd
#     ❌ Disk /dev/sdb usage is critical: 92.5% (mounted at /var/lib/ceph/osd-1)
#     
#     💡 解决方案:
#     1. 清理旧的 PG 数据
#     2. 检查是否有大文件: du -sh /var/lib/ceph/osd-1/*
#     3. 考虑添加新的 OSD

# 🟠 192.168.1.10 (RemoteStorage)
#     Type: CephStatus, Role: mon
#     ❌ Ceph cluster health is WARN
#     
#     💡 建议: 运行 ceph health detail 查看详情
```

### 场景 2: 远程日志分析 ⭐

```bash
# 启用自动诊断，会自动收集远程日志
infra-agent analyze --filter RemoteStorage --explain --auto-diagnose

# AI 会自动：
# 1. SSH 连接到存储服务器
# 2. 收集 Ceph 日志
# 3. 收集系统日志
# 4. 搜索错误模式
# 5. 分析根本原因
# 6. 给出详细解决方案

# 输出示例：
# 🔴 192.168.1.11 (RemoteStorage)
#     Type: OSDStatus, Role: osd
#     ❌ Process ceph-osd is not running
#     
#     📋 诊断详情:
#     - 日志分析: 在 /var/log/ceph/ceph-osd.11.log 中发现
#       "failed to read metadata: No such file or directory"
#     - 系统日志: journalctl 显示 OSD 因磁盘错误退出
#     - SMART 状态: /dev/sdb 有 5 个坏扇区
#     
#     🤖 AI 分析:
#     OSD 无法启动的根本原因是底层磁盘故障。
#     SMART 状态显示磁盘有坏扇区，导致 OSD 元数据读取失败。
#     
#     💡 解决方案:
#     1. 立即更换 /dev/sdb 磁盘
#     2. 标记 OSD 为 out: ceph osd out osd.11
#     3. 停止 OSD: systemctl stop ceph-osd@11
#     4. 更换磁盘后重新部署 OSD
#     5. 数据会自动重新平衡
```

### 场景 3: 日志搜索

```bash
# 使用自定义命令搜索日志
infra-agent diagnose --server 192.168.1.11 --search-logs "slow request"

# 输出：
# 在 192.168.1.11 上搜索 "slow request"
# 找到 23 条匹配记录:
# 
# 2024-01-15 10:23:45 slow request 30.156 seconds old: osd.11
# 2024-01-15 10:24:12 slow request 45.231 seconds old: osd.11
# ...
#
# 💡 建议: 慢请求可能由以下原因导致：
# 1. 磁盘 I/O 性能问题
# 2. 网络延迟
# 3. OSD 负载过高
```

### 场景 4: 综合巡检

```bash
# 启动包含远程存储的定期巡检
infra-agent patrol start --interval 15m

# 巡检会自动：
# 1. 检查 K8s 中的 Ceph Pods
# 2. SSH 连接到物理存储服务器
# 3. 检查 Ceph 集群状态
# 4. 检查磁盘使用率
# 5. 检查 OSD 状态
# 6. 收集和分析日志
# 7. 检查进程状态
# 8. 检查网络连接
# 9. 发现问题自动诊断
# 10. 发送飞书通知
```

## 诊断命令

### Ceph 相关

| 命令 | 用途 | 示例 |
|------|------|------|
| `CheckCephStatus` | 集群状态 | `ceph status` |
| `CheckCephHealth` | 健康详情 | `ceph health detail` |
| `CheckOSDStatus` | OSD 状态 | `ceph osd tree` |
| `GetCephLogs` | 获取日志 | `tail -n 200 /var/log/ceph/ceph-osd.*.log` |
| `SearchCephLogs` | 搜索日志 | `grep 'error' /var/log/ceph/*.log` |

### 系统相关

| 命令 | 用途 | 示例 |
|------|------|------|
| `CheckDiskUsage` | 磁盘使用 | `df -h` |
| `CheckDiskIO` | I/O 统计 | `iostat -x 1 3` |
| `CheckDiskSMART` | SMART 状态 | `smartctl -a /dev/sdb` |
| `GetSystemLogs` | 系统日志 | `journalctl -u kubelet -n 100` |
| `CheckProcessStatus` | 进程状态 | `ps aux | grep ceph` |
| `CheckNetworkConnections` | 网络连接 | `netstat -tulpn | grep :6800` |

### 日志相关

| 功能 | 说明 |
|------|------|
| `ReadFile` | 读取远程文件 |
| `TailFile` | 读取文件尾部 |
| `GrepFile` | 搜索文件内容 |

## 工作流程

```
┌─────────────────────────────────────────────────────┐
│  1. K8s 内部检查                                     │
│     - Rook Ceph Pods                                │
│     - PVC 状态                                      │
│     - StorageClass                                  │
└──────────────────┬──────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│  2. SSH 连接到物理存储服务器 (新增)                  │
│     - 密码或私钥认证                                 │
│     - 自动重连                                       │
└──────────────────┬──────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│  3. 执行远程诊断命令                                 │
│     - Ceph 状态                                     │
│     - 磁盘使用                                       │
│     - OSD 状态                                      │
│     - 进程状态                                       │
└──────────────────┬──────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│  4. 收集远程日志 ⭐ (新增)                           │
│     - Ceph 日志                                     │
│     - 系统日志                                       │
│     - 搜索错误模式                                   │
└──────────────────┬──────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│  5. AI 智能分析                                      │
│     - 关联多个数据源                                 │
│     - 识别根本原因                                   │
│     - 生成解决方案                                   │
└──────────────────┬──────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│  6. 飞书通知                                         │
│     - 详细的诊断报告                                 │
│     - 包含日志摘要                                   │
│     - 解决方案步骤                                   │
└─────────────────────────────────────────────────────┘
```

## 安全性

### SSH 认证

```yaml
# 推荐使用私钥认证
remoteStorage:
  servers:
    - host: 192.168.1.10
      port: 22
      user: ceph-admin
      privateKey: |
        -----BEGIN RSA PRIVATE KEY-----
        ...
        -----END RSA PRIVATE KEY-----
```

### 权限控制

- 使用专用用户而非 root
- 限制 sudo 权限到特定命令
- 使用只读命令（避免危险操作）

### 密钥管理

```bash
# 使用 K8s Secret 存储密钥
kubectl create secret generic storage-ssh-keys \
  --from-file=server1=/path/to/key1 \
  --from-file=server2=/path/to/key2 \
  -n kube-system
```

## 最佳实践

1. **连接管理**
   - 使用连接池复用 SSH 连接
   - 设置合理的超时时间
   - 实现自动重连机制

2. **日志收集**
   - 定期收集日志到集中存储
   - 使用日志轮转避免日志过大
   - 建立日志索引加快搜索

3. **性能优化**
   - 并发执行多个服务器的检查
   - 缓存不常变化的信息
   - 限制日志收集的行数

4. **安全加固**
   - 使用私钥而非密码
   - 限制 SSH 用户权限
   - 定期轮换密钥

## 对比 storage-bot

### storage-bot 的限制
- ❌ 无法查看日志
- ❌ 无法搜索日志内容
- ❌ 没有 AI 诊断
- ❌ 没有自动巡检
- ❌ 没有告警通知
- ❌ 无法关联 K8s 资源

### infra-agent 的增强
- ✅ 完整的日志查看和搜索
- ✅ AI 智能诊断
- ✅ 自动定期巡检
- ✅ 飞书实时告警
- ✅ K8s 和物理服务器综合监控
- ✅ 一键诊断和解决方案

---

**💡 综合 storage-bot 和 infra-agent 的能力，实现了完整的存储监控解决方案！**
