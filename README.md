# Infra Agent - 智能基础设施运维 Agent

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)](https://go.dev/)
[![Built with AI](https://img.shields.io/badge/Built%20with-AI%20Assistance-purple)]()

一个综合性的 Kubernetes 基础设施智能运维 Agent，专为计算和存储服务器设计。结合 AI 能力，实现自动问题发现、智能诊断和解决方案推荐。

## ✨ 核心特性

### 🤖 智能诊断
- **AI 双阶段分析**：初步分析 → 智能决策 → 执行诊断 → 综合分析
- **自动远程执行**：在 Pod 中自动执行诊断命令收集信息
- **根因分析**：深度分析问题根本原因
- **解决方案推荐**：提供详细的修复步骤和预防建议

### 🔍 多维度分析
- **K8s 资源分析**：Pod、Deployment、StatefulSet、Service、Node
- **存储监控**：PVC、磁盘空间、I/O 性能
- **网络检查**：连接性、DNS 解析
- **日志分析**：自动收集当前和重启前的日志

### 🔔 自动巡检和告警
- **定期巡检**：按配置间隔自动检查集群健康
- **飞书集成**：精美的卡片通知，实时告警
- **严重程度分级**：Critical、High、Medium、Low
- **问题汇总**：生成详细的巡检报告

### 🚀 易用性
- **命令行工具**：简单直观的 CLI 界面
- **多输出格式**：Text、JSON、YAML
- **灵活过滤**：按资源类型、命名空间过滤
- **完整文档**：详细的使用指南和最佳实践

## 📊 与其他项目对比

| 特性 | k8sgpt | sre-agent | **infra-agent** |
|------|--------|-----------|-----------------|
| 语言 | Go | Python | **Go** |
| K8s 资源分析 | ✅ | ❌ | ✅ **6 种** |
| 远程命令执行 | ❌ | ❌ | ✅ **核心** |
| AI 智能诊断 | ✅ 单次 | ✅ 单次 | ✅ **双阶段** |
| 自动巡检 | ❌ | ✅ | ✅ **完整** |
| 飞书通知 | ❌ | ❌ | ✅ **完整** |
| 自动诊断 | ❌ | ❌ | ✅ **智能** |

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

```bash
# 配置 AI Provider (OpenAI 或 Anthropic)
export OPENAI_API_KEY="sk-..."
infra-agent config set-ai --provider openai --token "$OPENAI_API_KEY"

# 配置飞书通知
export FEISHU_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/..."
```

### 使用

```bash
# 基本分析
infra-agent analyze

# AI 智能诊断
infra-agent analyze --explain --auto-diagnose

# 完整流程：分析 + 诊断 + 通知
infra-agent analyze --explain --auto-diagnose --notify

# 定期巡检（每 30 分钟）
infra-agent patrol start --interval 30m
```

## 📖 文档

- [快速开始指南](docs/GETTING_STARTED.md) - 安装和基本使用
- [架构设计](docs/ARCHITECTURE.md) - 系统架构和设计理念
- [自动诊断](docs/AUTO_DIAGNOSIS.md) - 自动诊断功能详解
- [飞书集成](docs/FEISHU_INTEGRATION.md) - 飞书通知配置指南
- [项目总结](PROJECT_SUMMARY.md) - 完整的项目总结

## 🎯 使用场景

### 场景 1: 日常问题排查

```bash
# 发现 Pod CrashLoopBackOff
infra-agent analyze --filter Pod --explain --auto-diagnose -n production

# 输出：
# 🔴 production/my-app (Pod)
# ❌ Container is in CrashLoopBackOff
# 🤖 AI 分析: 内存不足导致 OOM
# 💡 解决方案: 调整内存限制到 512Mi
```

### 场景 2: 持续监控

```bash
# 启动自动巡检
infra-agent patrol start --interval 15m

# 自动：
# 1. 每 15 分钟扫描集群
# 2. 发现问题自动诊断
# 3. 发送飞书通知
```

### 场景 3: 存储问题检查

```bash
# 检查 PVC 状态
infra-agent analyze --filter PersistentVolumeClaim --explain

# 输出：
# 🟠 default/data-pvc (PVC)
# ❌ PVC is in Pending state
# 💡 解决方案: 检查 StorageClass 配置
```

## 🏗️ 架构设计

```
┌─────────────────────────────────────────────┐
│              Infra Agent                     │
├─────────────────────────────────────────────┤
│  CLI / API / 巡检调度 / 指标收集            │
├─────────────────────────────────────────────┤
│         核心分析引擎 (6 个分析器)            │
├─────────────────────────────────────────────┤
│    AI 诊断层 (OpenAI / Anthropic)           │
├─────────────────────────────────────────────┤
│  通知层 (飞书) / 存储层 (缓存/历史)         │
└─────────────────────────────────────────────┘
                   ▼
         Kubernetes Cluster
```

## 🔧 核心功能

### 1. 分析器系统

- **PodAnalyzer** - Pod 状态、重启、资源限制
- **DeploymentAnalyzer** - 副本状态、滚动更新
- **StatefulSetAnalyzer** - 有状态应用检查
- **ServiceAnalyzer** - 服务端点、选择器
- **NodeAnalyzer** - 节点健康、资源压力
- **PVCAnalyzer** - 存储卷状态、容量

### 2. 自动诊断流程

```
发现问题 → 收集日志 → AI 决策执行命令 → 
执行诊断 → 综合分析 → 生成方案 → 飞书通知
```

### 3. 预定义诊断命令

- `check-process` - 检查进程状态
- `check-memory` - 检查内存使用
- `check-disk` - 检查磁盘空间
- `check-network` - 检查网络连接
- `check-dns` - 检查 DNS 解析
- 支持自定义命令

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

本项目参考了以下优秀项目：
- [k8sgpt](https://github.com/k8sgpt-ai/k8sgpt) - Kubernetes 智能诊断工具
- [sre-agent](https://github.com/fuzzylabs/sre-agent) - SRE Agent 架构

---

**🎉 现在就开始使用 Infra Agent，让 AI 成为你的运维助手！**

```bash
./bin/infra-agent analyze --explain --auto-diagnose --notify
```
