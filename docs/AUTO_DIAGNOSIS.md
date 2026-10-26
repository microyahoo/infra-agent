# 自动诊断功能文档

## 概述

Infra Agent 的自动诊断功能是一个智能化的故障排查系统，它能够：

1. 🔍 **自动发现问题** - 通过分析器扫描 Kubernetes 资源
2. 🤖 **智能决策** - AI 根据初步信息决定需要收集什么额外数据
3. 🔧 **远程执行** - 在目标 Pod/Node 上自动执行诊断命令
4. 📊 **深度分析** - 综合所有信息进行根因分析
5. 💡 **给出方案** - 提供详细的解决步骤和预防建议
6. 📤 **自动通知** - 将诊断结果发送到飞书

## 工作流程

```
┌─────────────────┐
│  发现问题        │  例如: Pod CrashLoopBackOff
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  收集基础信息    │  Pod 状态、事件、当前日志、之前日志
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  AI 第一轮分析   │  "需要检查进程状态、内存使用、磁盘空间"
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  执行诊断命令    │  自动在 Pod 中执行: ps aux, free -h, df -h
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  AI 综合分析     │  根因分析 + 解决方案 + 预防建议
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  发送飞书通知    │  格式化的诊断报告卡片
└─────────────────┘
```

## 使用方法

### 基本用法

```bash
# 分析并使用 AI 解释
infra-agent analyze --explain

# 启用自动诊断（会在 Pod 中执行命令）
infra-agent analyze --explain --auto-diagnose

# 分析、诊断并发送飞书通知
infra-agent analyze --explain --auto-diagnose --notify
```

### 配置文件

```yaml
# ~/.infra-agent.yaml

# 自动诊断配置
diagnosis:
  # 是否启用自动诊断
  enabled: true
  
  # 是否自动在 Pod 中执行诊断命令
  autoExecuteCommands: true
  
  # 命令执行超时（秒）
  commandTimeout: 30
  
  # 日志收集行数
  logLines: 200
  
  # 是否收集之前的日志（Pod 重启前）
  collectPreviousLogs: true

# 飞书通知配置
notifiers:
  default: "feishu"
  feishu:
    enabled: true
    webhookURL: "${FEISHU_WEBHOOK_URL}"
    secret: "${FEISHU_SECRET}"
```

### 环境变量

```bash
# 飞书 Webhook
export FEISHU_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_KEY"
export FEISHU_SECRET="your_secret_if_configured"

# AI Provider (示例)
export OPENAI_API_KEY="sk-..."
```

## 诊断命令

### 预定义命令

自动诊断器包含以下预定义的诊断命令：

#### Pod 诊断命令

1. **check-process** - 检查进程状态
   ```bash
   ps aux
   ```

2. **check-disk** - 检查磁盘使用情况
   ```bash
   df -h
   ```

3. **check-memory** - 检查内存使用情况
   ```bash
   free -h
   ```

4. **check-network** - 检查网络连接
   ```bash
   netstat -tulpn
   ```

5. **check-dns** - 检查 DNS 解析
   ```bash
   nslookup kubernetes.default.svc.cluster.local
   ```

6. **check-env** - 检查环境变量
   ```bash
   env
   ```

### AI 决策的命令

AI 会根据初步信息决定执行哪些命令，例如：

- 如果日志显示 OOMKilled → 执行内存检查
- 如果日志显示网络错误 → 执行网络和 DNS 检查
- 如果日志显示磁盘错误 → 执行磁盘检查

AI 还可以建议执行自定义命令：

```json
{
  "commands": ["check-process", "check-memory"],
  "custom_commands": [
    {
      "name": "check-app-config",
      "command": "cat /app/config.yaml"
    },
    {
      "name": "check-app-logs",
      "command": "tail -n 50 /var/log/app/error.log"
    }
  ]
}
```

## 诊断结果

### 文本输出示例

```
📊 分析结果:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
状态: Warning
发现问题: 1
分析耗时: 15.2s
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

[1] 🔴 default/my-app-pod (Pod)
    严重程度: critical
    ❌ Container app is in CrashLoopBackOff

    🤖 AI 分析:
    根据日志和诊断信息分析：
    1. 应用因内存不足（OOM）被 kill
    2. 当前内存限制为 256Mi，实际使用峰值达到 280Mi
    3. 进程检查显示 Java 应用堆内存配置为 512MB，超过容器限制

    💡 解决方案:
    1. 立即调整内存限制到至少 512Mi
       kubectl set resources deployment my-app --limits=memory=512Mi
    
    2. 优化 Java 堆内存配置
       调整 JAVA_OPTS="-Xmx256m -Xms128m"
    
    3. 预防措施
       - 添加内存监控告警
       - 进行压力测试确定合理的内存配置
       - 考虑启用 JVM 自动堆大小调整
```

### 飞书通知示例

自动诊断完成后，会发送包含以下信息的飞书卡片：

- 🎯 **问题描述** - 资源信息、严重程度、错误详情
- 🤖 **AI 分析** - 根因分析
- 📋 **诊断详情** - 执行的命令和结果摘要
- 💡 **解决方案** - 详细的修复步骤

## 安全性考虑

### 命令执行权限

自动诊断需要在 Pod 中执行命令，确保：

1. **ServiceAccount 权限** - 需要 `pods/exec` 权限
2. **容器工具** - Pod 中需要有基本的 shell 和诊断工具
3. **超时控制** - 所有命令都有超时限制，防止卡死

### RBAC 配置

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: infra-agent
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log", "pods/exec"]
  verbs: ["get", "list", "create"]
- apiGroups: [""]
  resources: ["nodes", "events", "persistentvolumeclaims"]
  verbs: ["get", "list"]
```

### 数据脱敏

启用 `--anonymize` 标志后，发送到 AI 的数据会自动脱敏：

- Pod 名称
- Namespace 名称
- IP 地址
- 密钥和敏感环境变量

## 局限性

1. **容器工具依赖** - 某些精简容器可能缺少诊断工具（如 `ps`, `netstat`）
2. **权限限制** - 某些操作可能因安全策略被禁止
3. **网络隔离** - 在严格的网络策略下，可能无法执行某些网络诊断
4. **AI 准确性** - AI 分析依赖于提供的信息质量，可能存在误判

## 最佳实践

1. **逐步启用** - 先用 `--explain`，熟悉后再用 `--auto-diagnose`
2. **定期巡检** - 使用 `patrol` 命令进行定期巡检
3. **监控通知** - 配置飞书接收实时告警
4. **结合人工** - AI 建议仅供参考，关键操作需人工确认
5. **持续优化** - 根据历史问题优化诊断规则

## 故障排查

### 命令执行失败

```
执行失败: container not found
```

**解决方法：**
- 确认 Pod 正在运行
- 检查容器名称是否正确
- 尝试指定容器：`--container=app`

### 权限不足

```
Error: pods "my-pod" is forbidden
```

**解决方法：**
- 检查 ServiceAccount 权限
- 应用正确的 RBAC 配置

### 飞书通知失败

```
feishu error: invalid webhook url
```

**解决方法：**
- 验证 Webhook URL 是否正确
- 检查签名密钥配置
- 确认网络可达性

## 后续规划

- [ ] 支持节点级别的诊断
- [ ] 自动修复常见问题
- [ ] 诊断历史记录和趋势分析
- [ ] 自定义诊断脚本
- [ ] 多集群诊断
- [ ] 性能优化建议
