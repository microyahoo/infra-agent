# Rook Ceph 存储监控功能

## 概述

Infra Agent 提供了针对 Rook Ceph 存储系统的专项监控和诊断功能，包括：

- **Ceph 集群健康检查**
- **OSD 状态监控**
- **Monitor 和 Manager 监控**
- **CSI 插件状态检查**
- **存储卷（PVC）问题诊断**
- **磁盘健康和性能监控**
- **系统日志分析**

## 功能模块

### 1. Rook Ceph 分析器

自动检测和分析 Rook Ceph 集群的各个组件：

#### 监控的组件

- **Ceph Monitor (MON)** - 集群状态管理
- **Ceph Manager (MGR)** - 集群管理守护进程
- **Ceph OSD** - 对象存储守护进程
- **Ceph MDS** - 元数据服务器（CephFS）
- **Ceph RGW** - 对象网关（S3/Swift）
- **CSI Driver** - 容器存储接口驱动
- **Rook Operator** - Rook 运营者

#### 检测的问题

```bash
# 运行 Rook Ceph 专项检查
infra-agent analyze --filter RookCeph --explain
```

检测内容：
- ✅ Ceph 组件 Pod 状态
- ✅ 容器重启次数异常
- ✅ CrashLoopBackOff 状态
- ✅ Deployment 副本不足
- ✅ StorageClass 配置问题
- ✅ Ceph-backed PVC 问题

### 2. 磁盘分析器

监控节点磁盘状态和使用情况：

```bash
# 检查磁盘状态
infra-agent analyze --filter Disk --explain --auto-diagnose
```

监控项目：
- **磁盘压力** - NodeDiskPressure 条件
- **临时存储使用** - Ephemeral Storage 使用率
- **磁盘污点** - 磁盘相关的 Node Taints
- **磁盘 I/O** - 通过远程命令检查 iostat
- **SMART 状态** - 磁盘健康状况

### 3. 日志分析器

智能分析系统和应用日志：

#### 分析的日志类型

1. **应用日志** - Pod 容器日志
2. **系统日志** - kubelet, docker/containerd
3. **Ceph 日志** - Ceph 集群日志

#### 日志模式识别

自动识别常见错误模式：
- ❌ OOM (Out of Memory)
- ❌ Connection Refused
- ❌ Timeout
- ❌ Disk Full / No Space
- ❌ Ceph Health Warnings
- ⚠️ Deprecated APIs
- ⚠️ Slow Operations

### 4. 自动诊断命令

针对存储和磁盘问题的专项诊断命令：

#### Ceph 相关命令

```bash
check-ceph-status    # ceph status - 集群整体状态
check-ceph-health    # ceph health detail - 健康详情
check-ceph-osd       # ceph osd tree - OSD 拓扑
```

#### 磁盘相关命令

```bash
check-disk           # df -h - 磁盘空间
check-disk-io        # iostat -x - I/O 统计
check-disk-smart     # smartctl - SMART 状态
```

## 使用示例

### 场景 1: Ceph 集群健康检查

```bash
# 完整的 Ceph 集群检查
infra-agent analyze --filter RookCeph --explain --auto-diagnose --notify

# 输出示例：
# 🔴 rook-ceph/rook-ceph-osd-0-xxx (RookCeph)
#     严重程度: critical
#     组件: OSD
#     ❌ Ceph OSD container has high restart count: 5
#
#     🤖 AI 分析:
#     OSD 频繁重启可能由以下原因导致：
#     1. 底层磁盘故障
#     2. 网络连接不稳定
#     3. 资源不足（内存/CPU）
#
#     📋 诊断详情:
#     执行的命令:
#     - check-ceph-status: HEALTH_WARN, 1 OSD down
#     - check-ceph-osd: osd.0 down
#     - check-disk: /var/lib/rook 95% 使用
#
#     💡 解决方案:
#     1. 检查磁盘健康: smartctl -H /dev/sdX
#     2. 清理磁盘空间或扩容
#     3. 重启 OSD: kubectl delete pod rook-ceph-osd-0-xxx
```

### 场景 2: 磁盘空间监控

```bash
# 检查所有节点的磁盘状态
infra-agent analyze --filter Disk --explain

# 输出示例：
# 🟠 node-worker-1 (Disk)
#     ❌ Node ephemeral storage usage is high: 87.5%
#     ❌ Node has disk pressure: kubelet has insufficient disk space
#
#     💡 解决方案:
#     1. 清理未使用的容器镜像: docker system prune -a
#     2. 清理日志文件
#     3. 扩容磁盘
```

### 场景 3: PVC 问题排查

```bash
# 检查 Ceph-backed PVC
infra-agent analyze --filter PersistentVolumeClaim --explain --auto-diagnose

# 输出示例：
# 🔴 default/data-pvc (RookCeph)
#     Type: PVC
#     StorageClass: rook-ceph-block
#     ❌ Ceph-backed PVC is in Pending state
#
#     🤖 AI 分析:
#     PVC 无法绑定，可能原因：
#     1. Ceph 集群不健康
#     2. StorageClass 配置错误
#     3. 没有可用的 OSD
#
#     📋 诊断详情:
#     - check-ceph-status: HEALTH_ERR, no OSDs available
#
#     💡 解决方案:
#     1. 检查 Ceph 集群: ceph -s
#     2. 检查 OSD Pods: kubectl get pods -n rook-ceph | grep osd
#     3. 查看 Rook Operator 日志
```

### 场景 4: 定期巡检（包含 Rook Ceph）

```bash
# 启动包含 Ceph 监控的巡检
infra-agent patrol start --interval 15m

# 巡检会自动：
# 1. 检查所有 Ceph 组件状态
# 2. 检查磁盘使用情况
# 3. 分析 Ceph 日志
# 4. 发现问题自动诊断
# 5. 发送飞书通知
```

## 配置

### 启用 Rook Ceph 监控

```yaml
# config/default.yaml 或 ~/.infra-agent.yaml

analyzers:
  enabled:
    - pod
    - deployment
    - node
    - pvc
    - rook-ceph    # Rook Ceph 专项分析
    - disk         # 磁盘分析

  # Rook Ceph 特定配置
  rook-ceph:
    namespace: "rook-ceph"  # Rook Ceph 命名空间
    checkOSD: true
    checkMonitor: true
    checkCSI: true

  # 磁盘分析配置
  disk:
    usageThreshold: 80      # 使用率阈值（百分比）
    checkSMART: true        # 检查 SMART 状态
    checkIO: true           # 检查 I/O 性能
```

### 日志分析配置

```yaml
diagnosis:
  # 日志收集配置
  logLines: 500              # 收集日志行数
  collectPreviousLogs: true  # 收集重启前日志
  
  # 日志分析
  analyzeLogs: true
  logPatterns:
    - "out of memory"
    - "disk.*full"
    - "ceph.*health.*warn"
    - "osd.*down"
```

## 诊断命令说明

### Ceph 诊断命令

| 命令 | 说明 | 使用场景 |
|------|------|----------|
| `ceph status` | 集群整体状态 | 快速查看集群健康 |
| `ceph health detail` | 健康详情 | 详细的问题描述 |
| `ceph osd tree` | OSD 拓扑 | 查看 OSD 分布和状态 |
| `ceph osd df` | OSD 磁盘使用 | 检查空间分布 |
| `ceph pg dump` | PG 状态 | 排查数据分布问题 |

### 磁盘诊断命令

| 命令 | 说明 | 使用场景 |
|------|------|----------|
| `df -h` | 磁盘空间 | 检查空间使用 |
| `iostat -x` | I/O 统计 | 检查 I/O 性能 |
| `smartctl -H` | SMART 健康 | 检查磁盘健康 |
| `lsblk` | 块设备列表 | 查看磁盘布局 |

## 告警规则

### Ceph 告警级别

| 级别 | 条件 | 说明 |
|------|------|------|
| 🔴 Critical | OSD down, Monitor 丢失 quorum | 影响数据可用性 |
| 🟠 High | OSD 重启频繁, HEALTH_WARN | 影响性能 |
| 🟡 Medium | PG degraded, Slow ops | 需要关注 |

### 磁盘告警级别

| 级别 | 条件 | 说明 |
|------|------|------|
| 🔴 Critical | 使用率 > 90%, SMART 失败 | 紧急处理 |
| 🟠 High | 使用率 > 80%, Disk Pressure | 需要清理 |
| 🟡 Medium | I/O 延迟高 | 性能问题 |

## 最佳实践

### 1. 日常监控

```bash
# 每 15 分钟巡检一次
infra-agent patrol start --interval 15m
```

### 2. 问题排查

```bash
# 发现问题时，运行完整诊断
infra-agent analyze --filter RookCeph,Disk --explain --auto-diagnose --notify
```

### 3. 容量规划

```bash
# 定期检查磁盘使用趋势
infra-agent analyze --filter Disk --output json > disk-usage.json
```

### 4. 预防性维护

- 设置告警阈值：磁盘使用率 80%
- 定期检查 SMART 状态
- 监控 OSD 重启次数
- 关注 Ceph Health 状态

## 故障排查

### 问题 1: Ceph 集群 HEALTH_WARN

```bash
# 运行诊断
infra-agent analyze --filter RookCeph --explain --auto-diagnose

# 手动检查
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph -s
```

### 问题 2: OSD 频繁重启

```bash
# 诊断 OSD 问题
infra-agent analyze --filter RookCeph --explain --auto-diagnose

# 检查 OSD 日志
kubectl -n rook-ceph logs -l app=rook-ceph-osd --tail=100
```

### 问题 3: 磁盘空间不足

```bash
# 检查磁盘使用
infra-agent analyze --filter Disk --explain

# 清理建议会在诊断报告中给出
```

## 参考资源

- [Rook 官方文档](https://rook.io/docs/rook/latest/)
- [Ceph 文档](https://docs.ceph.com/)
- [Kubernetes 存储](https://kubernetes.io/docs/concepts/storage/)

---

**💡 提示**: Rook Ceph 监控功能已集成到主分析流程中，使用 `analyze` 命令时会自动检测和分析 Ceph 集群。
