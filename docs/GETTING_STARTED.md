# Infra Agent 快速开始指南

## 🚀 快速安装

### 从源码构建

```bash
cd /root/go/src/github.com/microyahoo/infra-agent
make build

# 二进制文件在 bin/infra-agent
./bin/infra-agent version
```

### 安装到系统

```bash
make install
infra-agent version
```

## ⚙️ 配置

### 1. 配置 AI Provider

#### 使用 OpenAI

```bash
export OPENAI_API_KEY="sk-..."
infra-agent config set-ai --provider openai --token "$OPENAI_API_KEY" --model gpt-4
```

#### 使用 Anthropic (Claude)

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
infra-agent config set-ai --provider anthropic --token "$ANTHROPIC_API_KEY" --model claude-3-sonnet-20240229
```

### 2. 配置飞书通知

```bash
export FEISHU_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_KEY"
export FEISHU_SECRET="your_secret"  # 可选
```

或者编辑配置文件 `~/.infra-agent.yaml`:

```yaml
ai:
  provider: openai
  enabled: true
  openai:
    apiKey: "sk-..."
    model: "gpt-4"

notifiers:
  default: feishu
  feishu:
    enabled: true
    webhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/..."
    secret: "your_secret"
```

## 📝 使用示例

### 1. 基本分析

```bash
# 分析集群所有资源
infra-agent analyze

# 指定命名空间
infra-agent analyze -n default

# 按资源类型过滤
infra-agent analyze --filter Pod,Service
```

### 2. AI 智能诊断

```bash
# 使用 AI 解释问题
infra-agent analyze --explain

# 启用自动诊断（在 Pod 中执行命令）
infra-agent analyze --explain --auto-diagnose

# 完整流程：分析 + 诊断 + 飞书通知
infra-agent analyze --explain --auto-diagnose --notify
```

### 3. 定期巡检

```bash
# 启动巡检服务，每 30 分钟执行一次
infra-agent patrol start --interval 30m

# 执行一次巡检
infra-agent patrol run

# 查看配置
infra-agent config list
```

### 4. 输出格式

```bash
# JSON 输出
infra-agent analyze --output json

# YAML 输出
infra-agent analyze --output yaml

# 默认文本输出
infra-agent analyze
```

## 🎯 实际场景示例

### 场景 1: 排查 Pod 启动失败

```bash
# 发现并诊断 Pod 问题
infra-agent analyze --filter Pod --explain --auto-diagnose -n production

# 输出示例：
# [1] 🔴 production/my-app-7d8f9c-abc (Pod)
#     严重程度: critical
#     ❌ Container app is in CrashLoopBackOff
#
#     🤖 AI 分析:
#     根据日志和诊断信息分析：
#     1. 应用因内存不足（OOM）被 kill
#     2. 当前内存限制为 256Mi，实际使用峰值达到 280Mi
#     3. 进程检查显示 Java 应用堆内存配置为 512MB，超过容器限制
#
#     💡 解决方案:
#     1. 立即调整内存限制到至少 512Mi
#        kubectl set resources deployment my-app --limits=memory=512Mi
#     2. 或优化 JVM 配置
#        JAVA_OPTS="-Xmx200m -Xms128m"
```

### 场景 2: 检查存储问题

```bash
# 检查 PVC 状态
infra-agent analyze --filter PersistentVolumeClaim --explain

# 输出示例：
# [1] 🟠 default/data-pvc (PersistentVolumeClaim)
#     严重程度: high
#     ❌ PVC is in Pending state, waiting for volume to be provisioned
#
#     💡 解决方案:
#     1. 检查 StorageClass 是否存在
#     2. 确认有足够的存储资源
#     3. 查看 PV provisioner 日志
```

### 场景 3: 持续监控

```bash
# 后台运行巡检服务
nohup infra-agent patrol start --interval 15m > patrol.log 2>&1 &

# 检查日志
tail -f patrol.log

# 输出示例：
# 🔍 开始巡检: 2024-01-15 10:30:00
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#   → 运行 PodAnalyzer...
#   ✓ 未发现问题
#   → 运行 DeploymentAnalyzer...
#   ✗ 发现 2 个问题
# 
# 🤖 正在进行 AI 诊断...
#   [1/2] 诊断 default/web-deployment...
#   ✓ 诊断完成
#
# 📤 发送巡检报告到飞书...
#   ✓ 通知已发送
```

## 🔧 RBAC 权限配置

在 Kubernetes 集群中运行时，需要配置相应的权限：

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: infra-agent
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: infra-agent
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log", "pods/exec", "nodes", "services", "persistentvolumeclaims", "events"]
  verbs: ["get", "list", "create"]
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets", "replicasets"]
  verbs: ["get", "list"]
- apiGroups: ["metrics.k8s.io"]
  resources: ["nodes", "pods"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: infra-agent
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: infra-agent
subjects:
- kind: ServiceAccount
  name: infra-agent
  namespace: kube-system
```

## 📊 监控和告警

### 飞书通知格式

自动诊断完成后，会收到包含以下信息的飞书卡片：

- 🎯 **问题描述** - 资源信息、严重程度、错误详情
- 🤖 **AI 分析** - 根因分析
- 📋 **诊断详情** - 执行的命令和结果
- 💡 **解决方案** - 详细的修复步骤

### 严重程度

- 🔴 **Critical** - 服务不可用，需要立即处理
- 🟠 **High** - 重要问题，影响服务质量
- 🟡 **Medium** - 需要关注，暂不影响服务
- 🟢 **Low** - 优化建议

## 🐛 故障排查

### 问题 1: API Key 未配置

```
Error: OpenAI API key not configured
```

**解决方法：**
```bash
export OPENAI_API_KEY="sk-..."
# 或
infra-agent config set-ai --provider openai --token "sk-..."
```

### 问题 2: 权限不足

```
Error: pods "my-pod" is forbidden
```

**解决方法：**
- 检查 kubeconfig 权限
- 应用正确的 RBAC 配置
- 使用有权限的 ServiceAccount

### 问题 3: 命令执行失败

```
执行失败: container not found
```

**解决方法：**
- 确认 Pod 正在运行
- 检查容器是否包含必要的工具（ps, netstat 等）
- 某些精简镜像可能缺少诊断工具

## 📖 进一步阅读

- [完整架构文档](./ARCHITECTURE.md)
- [自动诊断详细说明](./AUTO_DIAGNOSIS.md)
- [飞书集成配置](./FEISHU_INTEGRATION.md)
- [开发指南](./DEVELOPMENT.md)

## 💡 最佳实践

1. **从简单开始** - 先用 `analyze` 熟悉基本功能
2. **逐步启用功能** - 先 `--explain`，再 `--auto-diagnose`
3. **配置定期巡检** - 用 `patrol` 持续监控
4. **关注飞书通知** - 及时收到告警
5. **结合人工判断** - AI 建议仅供参考
6. **关注关键资源** - 使用 `--filter` 聚焦重要资源
7. **定期检查配置** - `infra-agent config list`

## 🤝 支持

如有问题或建议，欢迎提交 Issue！
