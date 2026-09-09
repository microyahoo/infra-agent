# Infra Agent - 智能基础设施运维 Agent

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)](https://go.dev/)
[![Built with AI](https://img.shields.io/badge/Built%20with-AI%20Assistance-purple)]()

一个综合性的 Kubernetes 基础设施智能运维 Agent，专为计算和存储服务器设计。整合了 **k8sgpt**、**sre-agent** 和 **storage-bot** 的优秀设计，并结合 AI 能力，实现自动问题发现、智能诊断和解决方案推荐。

## ✨ 核心特性

### 🤖 智能诊断
- **AI 双阶段分析**：初步分析 → 智能决策 → 执行诊断 → 综合分析
- **自动远程执行**：在 K8s Pod 和物理服务器上执行诊断命令
- **根因分析**：深度分析问题根本原因
- **解决方案推荐**：提供详细的修复步骤和预防建议

### 🔍 多维度分析
- **K8s 资源分析**：Pod、Deployment、StatefulSet、Service、Node
- **Rook Ceph 监控** ⭐：Monitor、Manager、OSD、CSI 等组件
- **远程存储服务器** ⭐⭐：SSH 连接物理服务器，执行 Ceph 命令
- **磁盘监控**：使用率、I/O 性能、SMART 状态
- **日志分析** ⭐⭐：远程日志收集、搜索、智能分析

### 🗄️ 存储专项能力（整合 storage-bot）
- **SSH 远程执行**：直接连接到 Ceph 存储服务器
- **Ceph 诊断命令**：status、health、osd tree、df 等
- **远程日志收集** ⭐：解决 storage-bot 无法查看日志的痛点
- **日志搜索** ⭐：在远程服务器上搜索错误模式
- **综合监控**：K8s Rook Ceph + 物理存储服务器

### 🔔 自动巡检和告警
- **定期巡检**：包含 K8s 和远程存储服务器
- **飞书集成**：精美的卡片通知，实时告警
- **严重程度分级**：Critical、High、Medium、Low
- **详细报告**：包含远程诊断信息

### 🚀 易用性
- **命令行工具**：简单直观的 CLI 界面
- **多输出格式**：Text、JSON、YAML
- **灵活过滤**：按资源类型、命名空间过滤
- **完整文档**：7 份详细文档

## 📊 与其他项目对比

| 特性 | k8sgpt | sre-agent | storage-bot | **infra-agent** |
|------|--------|-----------|-------------|-----------------|
| 语言 | Go | Python | Go | **Go** |
| K8s 资源分析 | ✅ | ❌ | ❌ | ✅ **8 种** |
| Rook Ceph 监控 | ❌ | ❌ | ❌ | ✅ **专项** |
| SSH 远程执行 | ❌ | ❌ | ✅ | ✅ **增强** |
| 日志查看 | 部分 | ✅ | ❌ **缺失** | ✅ **完整** |
| 日志搜索 | ❌ | ❌ | ❌ **缺失** | ✅ **新增** |
| AI 诊断 | ✅ 单次 | ✅ 单次 | ❌ | ✅ **双阶段** |
| 自动巡检 | ❌ | ✅ | 部分 | ✅ **完整** |
| 飞书通知 | ❌ | ❌ | ✅ | ✅ **增强** |
| 物理服务器 | ❌ | ❌ | ✅ 部分 | ✅ **完整** |

### 💡 对 storage-bot 的增强

storage-bot 的局限：
- ❌ 无法查看和分析日志
- ❌ 没有 AI 智能诊断
- ❌ 缺少自动巡检
- ❌ 无法关联 K8s 资源

infra-agent 的增强：
- ✅ **远程日志收集和搜索**
- ✅ **AI 智能诊断**
- ✅ **自动定期巡检**
- ✅ **K8s 和物理服务器综合监控**
- ✅ **飞书实时告警**

## 🚀 快速开始

### 安装

```bash
# 从源码构建
git clone https://github.com/microyahoo/infra-agent
cd infra-agent
make build

# 测试
./bin/infra-agent version
```

### 配置

#### 1. 配置 AI Provider

```bash
# OpenAI
export OPENAI_API_KEY="sk-..."
infra-agent config set-ai --provider openai --token "$OPENAI_API_KEY"

# 或 Anthropic
export ANTHROPIC_API_KEY="sk-ant-..."
infra-agent config set-ai --provider anthropic --token "$ANTHROPIC_API_KEY"
```

#### 2. 配置飞书通知

```bash
export FEISHU_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/..."
export FEISHU_SECRET="your_secret"  # 可选
```

#### 3. 配置远程存储服务器（可选）

```yaml
# ~/.infra-agent.yaml
remoteStorage:
  enabled: true
  servers:
    - host: 192.168.1.10
      port: 22
      user: root
      password: "${STORAGE_PASSWORD}"
      role: mon
    - host: 192.168.1.11
      port: 22
      user: root
      password: "${STORAGE_PASSWORD}"
      role: osd
```

### 使用示例

#### 基本使用

```bash
# 基本分析
infra-agent analyze

# AI 智能诊断
infra-agent analyze --explain --auto-diagnose

# 完整流程：分析 + 诊断 + 通知
infra-agent analyze --explain --auto-diagnose --notify
```

#### 存储专项监控

```bash
# Rook Ceph 集群检查
infra-agent analyze --filter RookCeph --explain

# 远程存储服务器检查（SSH）
infra-agent analyze --filter RemoteStorage --explain --auto-diagnose

# 综合检查（K8s + Rook Ceph + 远程服务器）
infra-agent analyze \
  --filter Pod,RookCeph,Disk,RemoteStorage \
  --explain --auto-diagnose --notify
```

#### 定期巡检

```bash
# 启动巡检（包含远程服务器）
infra-agent patrol start --interval 30m

# 巡检会自动：
# 1. 检查 K8s 资源
# 2. 检查 Rook Ceph 集群
# 3. SSH 到远程存储服务器
# 4. 收集远程日志并分析
# 5. AI 智能诊断
# 6. 发送飞书通知
```

## 📖 文档

- [快速开始指南](docs/GETTING_STARTED.md) - 安装和基本使用
- [架构设计](docs/ARCHITECTURE.md) - 系统架构和设计理念
- [自动诊断](docs/AUTO_DIAGNOSIS.md) - 自动诊断功能详解
- [飞书集成](docs/FEISHU_INTEGRATION.md) - 飞书通知配置指南
- [Rook Ceph 监控](docs/ROOK_CEPH_MONITORING.md) - Rook Ceph 专项监控
- [远程存储监控](docs/REMOTE_STORAGE_MONITORING.md) ⭐ - SSH 远程服务器监控
- [项目总结](PROJECT_SUMMARY.md) - 完整的项目总结

## 🎯 使用场景

### 场景 1: Rook Ceph 集群监控

```bash
# 完整的 Ceph 集群健康检查
infra-agent analyze --filter RookCeph --explain --auto-diagnose

# 输出：
# 🔴 rook-ceph/rook-ceph-osd-0-xxx (RookCeph)
# ❌ Ceph OSD container has high restart count: 5
# 🤖 AI 分析: OSD 频繁重启，可能是磁盘故障
# 💡 解决方案: 检查磁盘 SMART 状态，考虑更换磁盘
```

### 场景 2: 远程存储服务器诊断 ⭐

```bash
# SSH 到物理存储服务器进行深度诊断
infra-agent analyze --filter RemoteStorage --explain --auto-diagnose

# 自动：
# 1. SSH 连接到配置的存储服务器
# 2. 执行 ceph status 检查集群状态
# 3. 收集 /var/log/ceph/*.log 日志
# 4. 搜索日志中的错误模式
# 5. 检查磁盘使用率和 SMART 状态
# 6. AI 分析并给出解决方案
# 7. 发送飞书通知

# 输出：
# 🔴 192.168.1.11 (RemoteStorage)
# ❌ Disk /dev/sdb usage is critical: 92.5%
# 📋 诊断详情:
#    - 在日志中发现: "slow request 30.156 seconds old"
#    - SMART 状态: 磁盘有 5 个坏扇区
# 🤖 AI 分析: 磁盘故障导致 I/O 慢，需要更换
# 💡 解决方案:
#    1. 标记 OSD 为 out: ceph osd out osd.11
#    2. 更换 /dev/sdb 磁盘
#    3. 重新部署 OSD
```

### 场景 3: 综合监控

```bash
# K8s + Rook Ceph + 远程服务器 综合检查
infra-agent analyze \
  --filter Pod,RookCeph,Disk,RemoteStorage \
  --explain --auto-diagnose --notify

# 会检查：
# - K8s Pod 状态
# - Rook Ceph 组件状态
# - 节点磁盘使用率
# - 远程存储服务器（SSH）
# - 远程 Ceph 日志分析
# - AI 综合诊断
# - 飞书实时通知
```

### 场景 4: 持续监控

```bash
# 启动包含远程服务器的自动巡检
infra-agent patrol start --interval 15m

# 每 15 分钟自动：
# 1. 扫描 K8s 集群
# 2. 检查 Rook Ceph
# 3. SSH 到远程存储服务器
# 4. 收集远程日志
# 5. AI 智能诊断
# 6. 发送飞书通知
```

## 🏗️ 架构设计

```
┌──────────────────────────────────────────────────────────┐
│                    Infra Agent                           │
├──────────────────────────────────────────────────────────┤
│  CLI / API / 巡检调度 / 指标收集                         │
├──────────────────────────────────────────────────────────┤
│         核心分析引擎 (9 个分析器)                        │
│  K8s (6) + Rook Ceph (1) + Disk (1) + Remote (1)         │
├──────────────────────────────────────────────────────────┤
│    远程执行层 (K8s Pod + SSH)                            │
│  - 在 Pod 中执行命令                                     │
│  - SSH 连接物理服务器 ⭐                                 │
│  - 远程日志收集和搜索 ⭐                                 │
├──────────────────────────────────────────────────────────┤
│    AI 诊断层 (OpenAI / Anthropic)                        │
│  - 双阶段分析                                            │
│  - 智能命令决策                                          │
├──────────────────────────────────────────────────────────┤
│  通知层 (飞书) / 存储层 (缓存/历史)                      │
└──────────────────────────────────────────────────────────┘
                   ▼                    ▼
         Kubernetes Cluster    物理存储服务器 (SSH)
         - Rook Ceph Pods      - Ceph Monitor/OSD
         - PVC/StorageClass    - 日志文件 ⭐
         - Nodes               - 系统日志 ⭐
```

## 🔧 核心功能

### 1. 分析器系统（9 个）

**K8s 资源分析器 (6 个)**
- PodAnalyzer - Pod 状态、重启、资源限制
- DeploymentAnalyzer - 副本状态、滚动更新
- StatefulSetAnalyzer - 有状态应用检查
- ServiceAnalyzer - 服务端点、选择器
- NodeAnalyzer - 节点健康、资源压力
- PVCAnalyzer - 存储卷状态、容量

**存储专项分析器 (3 个)** ⭐
- RookCephAnalyzer - Rook Ceph 集群监控
- DiskAnalyzer - 磁盘状态和使用率
- RemoteStorageAnalyzer ⭐⭐ - SSH 远程存储服务器

### 2. 远程执行能力 ⭐

**K8s Pod 执行**
- 在 Pod 中执行诊断命令
- 收集容器日志

**SSH 远程执行** （整合 storage-bot）
- SSH 连接物理存储服务器
- 执行 Ceph 诊断命令
- **收集远程日志** ⭐
- **搜索日志内容** ⭐
- 检查磁盘 SMART 状态

### 3. 自动诊断流程

```
发现问题 
  ↓
收集信息（K8s + 远程服务器）
  ↓
AI 第一轮：决策执行哪些命令
  ↓
执行诊断命令（Pod + SSH）
  ↓
收集远程日志 ⭐
  ↓
AI 第二轮：综合分析
  ↓
生成详细方案
  ↓
飞书通知
```

### 4. 预定义诊断命令

**通用命令**
- `check-process` - 进程状态
- `check-memory` - 内存使用
- `check-disk` - 磁盘空间
- `check-network` - 网络连接
- `check-dns` - DNS 解析

**Ceph 专项命令** ⭐
- `check-ceph-status` - Ceph 集群状态
- `check-ceph-health` - 健康详情
- `check-ceph-osd` - OSD 拓扑

**磁盘专项命令** ⭐
- `check-disk-io` - I/O 统计
- `check-disk-smart` - SMART 状态

## 🔒 安全性

- ✅ RBAC 权限控制
- ✅ ServiceAccount 支持
- ✅ 数据脱敏（配置中）
- ✅ 命令执行隔离
- ✅ 签名验证（飞书）

## 📈 统计信息

- **代码行数**: ~3,700+ 行 Go 代码
- **分析器**: 6 个内置分析器
- **AI 提供商**: 2 个（OpenAI、Anthropic）
- **通知渠道**: 1 个（飞书）
- **诊断命令**: 6+ 个预定义命令
- **文档**: 5 份详细文档

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📄 许可证

Apache License 2.0

## 💡 致谢

本项目整合了以下优秀项目的设计理念：
- [k8sgpt](https://github.com/k8sgpt-ai/k8sgpt) - 分析器架构、多 AI 后端集成
- [sre-agent](https://github.com/fuzzylabs/sre-agent) - 日志分析、自动诊断流程
- [storage-bot](https://github.com/microyahoo/storage-bot) - SSH 远程执行能力

### 对 storage-bot 的增强

storage-bot 是一个优秀的 Ceph 存储管理工具，但存在一些局限：
- ❌ 无法查看和分析日志文件
- ❌ 缺少 AI 智能诊断能力
- ❌ 没有自动化巡检功能

infra-agent 在保留 storage-bot 的 SSH 远程执行能力的同时，增加了：
- ✅ **远程日志收集和搜索** - 解决了关键痛点
- ✅ **AI 智能诊断** - 自动分析根因
- ✅ **K8s 集成** - 关联 Rook Ceph 和物理服务器
- ✅ **自动巡检** - 定期检查并通知

---

**🎉 现在就开始使用 Infra Agent，让 AI 成为你的运维助手！**

```bash
# K8s 和物理存储服务器综合监控
./bin/infra-agent analyze \
  --filter RookCeph,RemoteStorage \
  --explain --auto-diagnose --notify
```
