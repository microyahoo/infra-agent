# Infra Agent 架构设计文档

## 1. 总体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                          Infra Agent                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌──────────┐ │
│  │  CLI 接口   │  │  REST API  │  │   巡检调度  │  │  指标收集 │ │
│  └────────────┘  └────────────┘  └────────────┘  └──────────┘ │
│                                                                  │
├─────────────────────────────────────────────────────────────────┤
│                         核心分析引擎                             │
│  ┌────────────────────────────────────────────────────────┐   │
│  │              Analyzer Registry                          │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ │   │
│  │  │ K8s 分析器│ │存储分析器 │ │网络分析器 │ │计算分析器 │ │   │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ │   │
│  └────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────┤
│                         AI 诊断层                               │
│  ┌────────────────────────────────────────────────────────┐   │
│  │         AI Provider Registry                            │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ │   │
│  │  │  OpenAI  │ │Anthropic │ │   Qwen   │ │  Local   │ │   │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ │   │
│  └────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────┤
│                       通知与存储层                              │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────┐    │
│  │   Notifiers  │  │  Cache Layer │  │  History Store  │    │
│  │  - Slack     │  │  - Memory    │  │  - File         │    │
│  │  - DingTalk  │  │  - Redis     │  │  - Database     │    │
│  │  - WeChat    │  │              │  │                 │    │
│  └──────────────┘  └──────────────┘  └─────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐          │
│  │   Pods   │ │   PVCs   │ │  Nodes   │ │ Services │          │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘          │
└─────────────────────────────────────────────────────────────────┘
```

## 2. 核心模块

### 2.1 分析器模块 (Analyzer)

**职责：** 负责检测和识别各类资源的问题

**设计模式：** 注册表模式 + 策略模式

**核心接口：**
```go
type Analyzer interface {
    GetName() string
    GetKind() string
    Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]Result, error)
    IsEnabled() bool
    SetEnabled(enabled bool)
}
```

**内置分析器：**

1. **K8s 资源分析器**
   - PodAnalyzer: Pod 状态、重启次数、资源限制
   - PVCAnalyzer: PVC 状态、容量、StorageClass
   - ServiceAnalyzer: 服务端点、选择器匹配
   - DeploymentAnalyzer: 副本状态、滚动更新
   - NodeAnalyzer: 节点资源、污点、条件

2. **存储专项分析器**
   - DiskAnalyzer: 磁盘空间、I/O 性能
   - CSIAnalyzer: CSI 插件状态
   - StorageClassAnalyzer: 存储类配置

3. **计算资源分析器**
   - CPUAnalyzer: CPU 使用率和趋势
   - MemoryAnalyzer: 内存使用和泄漏检测
   - ResourceQuotaAnalyzer: 配额使用情况

4. **网络分析器**
   - IngressAnalyzer: Ingress 配置
   - NetworkPolicyAnalyzer: 网络策略
   - ConnectivityAnalyzer: 服务连通性

### 2.2 AI 诊断模块

**职责：** 使用大语言模型对问题进行智能诊断和提供解决方案

**支持的 AI 提供商：**
- OpenAI (GPT-4, GPT-3.5)
- Anthropic (Claude-3)
- 通义千问 (Qwen)
- 本地模型 (通过 Ollama)

**核心功能：**
- 问题描述生成
- 根因分析
- 解决方案推荐
- 数据脱敏
- 上下文学习（基于历史问题）

### 2.3 巡检调度模块

**职责：** 定期执行健康检查和问题发现

**核心特性：**
- 灵活的调度规则
- 可配置的检查间隔
- 优先级队列
- 并发控制
- 失败重试

**巡检规则：**
```yaml
rules:
  - name: "pod-health"
    enabled: true
    severity: "high"
    interval: "5m"
    analyzers: ["pod", "deployment"]
  
  - name: "storage-capacity"
    enabled: true
    severity: "critical"
    interval: "30m"
    analyzers: ["pvc", "disk"]
```

### 2.4 通知模块

**职责：** 多渠道告警和报告发送

**支持渠道：**
- Slack
- 钉钉 (DingTalk)
- 企业微信 (WeChat Work)
- 邮件
- Webhook (自定义)

**特性：**
- 告警聚合
- 静默规则
- 优先级路由
- 消息模板

## 3. 数据流

```
┌─────────┐
│  用户请求 │
└────┬────┘
     │
     ▼
┌─────────────┐
│  CLI / API  │
└────┬────────┘
     │
     ▼
┌──────────────────┐
│  Analyzer Engine │ ──> 并行执行多个分析器
└────┬─────────────┘
     │
     ▼
┌─────────────┐
│  Raw Results│ ──> 原始问题列表
└────┬────────┘
     │
     ▼
┌─────────────┐      ┌──────────┐
│ AI Provider │ <──> │ Anonymize│
└────┬────────┘      └──────────┘
     │
     ▼
┌─────────────┐
│  Failures   │ ──> 带 AI 解释的问题
└────┬────────┘
     │
     ├──> ┌──────────┐
     │    │ Notifier │ ──> 发送告警
     │    └──────────┘
     │
     └──> ┌──────────┐
          │  Storage │ ──> 保存历史
          └──────────┘
```

## 4. 扩展性设计

### 4.1 自定义分析器

用户可以通过实现 `Analyzer` 接口来添加自定义分析器：

```go
type CustomAnalyzer struct {
    analyzer.BaseAnalyzer
}

func (a *CustomAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
    // 自定义分析逻辑
}

// 注册
registry.Register(&CustomAnalyzer{...})
```

### 4.2 插件系统

支持通过插件扩展功能：

```
plugins/
├── analyzers/      # 分析器插件
├── notifiers/      # 通知器插件
└── ai-providers/   # AI 提供商插件
```

## 5. 部署模式

### 5.1 命令行模式

```bash
# 单次分析
infra-agent analyze --explain

# 定时巡检
infra-agent patrol start --interval 30m
```

### 5.2 服务器模式

```bash
# 启动 API 服务器
infra-agent serve --port 8080

# REST API
GET  /api/v1/analyze
POST /api/v1/analyze
GET  /api/v1/patrol/reports
GET  /api/v1/health
```

### 5.3 Kubernetes Operator

在集群内部署为 Operator，持续监控和自动修复：

```yaml
apiVersion: infra.microyahoo.com/v1
kind: InfraAgent
metadata:
  name: cluster-monitor
spec:
  interval: 5m
  analyzers:
    - pod
    - pvc
    - node
  ai:
    provider: openai
  notifiers:
    - slack
    - dingtalk
```

## 6. 性能优化

### 6.1 并发分析

- 多个分析器并行执行
- 使用 worker pool 控制并发数
- Context 超时控制

### 6.2 缓存策略

- 分析结果缓存（避免重复分析）
- Kubernetes API 响应缓存
- AI 响应缓存（相似问题）

### 6.3 资源限制

```yaml
performance:
  maxConcurrentAnalyzers: 10
  maxConcurrentAIRequests: 5
  analysisTimeout: 60s
  aiRequestTimeout: 30s
```

## 7. 安全性

### 7.1 数据脱敏

- 敏感字段自动识别
- 可配置脱敏规则
- 发送到 AI 前自动脱敏

### 7.2 认证授权

- RBAC 权限控制
- ServiceAccount 支持
- API Token 认证

### 7.3 审计日志

- 所有操作记录
- 敏感操作告警
- 日志加密存储

## 8. 监控指标

```
infra_agent_analysis_total
infra_agent_analysis_duration_seconds
infra_agent_problems_detected_total
infra_agent_ai_requests_total
infra_agent_ai_errors_total
infra_agent_notifications_sent_total
```

## 9. 未来规划

- [ ] 自动修复能力（AutoRemediation）
- [ ] 机器学习增强诊断
- [ ] 多集群管理
- [ ] Web UI 控制台
- [ ] 移动端 App
- [ ] 与 GitOps 集成
- [ ] 成本优化建议
