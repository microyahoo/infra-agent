# AI 自主诊断和自动修复系统

## 🎯 核心需求

当存储集群（Ceph）出现故障时，AI Agent 能够：
1. ✅ **自动发现问题** - 监控告警触发
2. ✅ **自主收集信息** - SSH 到节点，查看日志
3. ✅ **智能分析** - AI 分析根因
4. ✅ **自动执行修复** - 执行修复命令
5. ✅ **验证结果** - 确认问题已解决
6. ✅ **记录和学习** - 记录处理过程，优化策略

---

## 🏗️ 系统架构

```
┌─────────────────────────────────────────────────────────────┐
│                  监控系统 (Prometheus/AlertManager)          │
│                         ↓ 告警触发                          │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│              AI Agent 自主诊断引擎                           │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐           │
│  │ 问题识别   │→ │ 信息收集   │→ │ AI 分析    │           │
│  └────────────┘  └────────────┘  └────────────┘           │
│         ↓              ↓              ↓                     │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐           │
│  │ 生成方案   │→ │ 执行修复   │→ │ 验证结果   │           │
│  └────────────┘  └────────────┘  └────────────┘           │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│              远程执行层 (SSH + K8s API)                      │
│  - SSH 到存储节点                                           │
│  - 执行诊断命令                                             │
│  - 收集日志                                                 │
│  - 执行修复操作                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## 🔄 自主诊断流程

### 阶段 1: 问题发现和初步判断

```go
// pkg/autonomous/detector.go

type AutonomousAgent struct {
    sshExecutor    *executor.SSHExecutor
    aiProvider     common.AIProvider
    actionExecutor *ActionExecutor
    knowledgeBase  *KnowledgeBase
}

// 接收告警
func (a *AutonomousAgent) HandleAlert(alert *Alert) error {
    log.Printf("🚨 收到告警: %s - %s", alert.AlertName, alert.Summary)
    
    // 1. 识别问题类型
    problemType := a.identifyProblemType(alert)
    
    // 2. 检查是否有已知解决方案
    if solution := a.knowledgeBase.FindSolution(problemType); solution != nil {
        log.Printf("📚 找到已知解决方案，直接应用")
        return a.applySolution(solution)
    }
    
    // 3. 启动自主诊断流程
    log.Printf("🤖 启动 AI 自主诊断流程...")
    return a.autonomousDiagnosis(alert)
}

// 识别问题类型
func (a *AutonomousAgent) identifyProblemType(alert *Alert) string {
    // 基于告警标签和内容识别
    labels := alert.Labels
    
    switch {
    case strings.Contains(labels["alertname"], "OSDDown"):
        return "ceph_osd_down"
    case strings.Contains(labels["alertname"], "HighDiskUsage"):
        return "disk_full"
    case strings.Contains(labels["alertname"], "SlowOps"):
        return "ceph_slow_ops"
    case strings.Contains(labels["alertname"], "PGDegraded"):
        return "ceph_pg_degraded"
    default:
        return "unknown"
    }
}
```

### 阶段 2: AI 驱动的信息收集

```go
// pkg/autonomous/collector.go

type InformationCollector struct {
    sshExecutor *executor.SSHExecutor
    aiProvider  common.AIProvider
}

// AI 决策需要收集什么信息
func (c *InformationCollector) CollectByAI(alert *Alert) (*DiagnosisContext, error) {
    // 1. 构建提示词，让 AI 决定收集什么
    prompt := fmt.Sprintf(`
你是一个 Ceph 存储专家。收到以下告警：

告警名称: %s
告警描述: %s
受影响节点: %s
严重程度: %s

请分析并告诉我：
1. 需要收集哪些日志文件？（给出具体路径）
2. 需要执行哪些诊断命令？
3. 收集的优先级（先收集什么）

以 JSON 格式返回：
{
  "logs": [
    {"path": "/var/log/ceph/ceph-osd.1.log", "lines": 200, "priority": 1},
    {"path": "/var/log/messages", "lines": 100, "priority": 2}
  ],
  "commands": [
    {"cmd": "ceph osd tree", "description": "查看 OSD 状态", "priority": 1},
    {"cmd": "ceph health detail", "description": "查看详细健康状态", "priority": 1}
  ],
  "reasoning": "为什么需要这些信息"
}
`, alert.AlertName, alert.Summary, alert.Instance, alert.Severity)

    // 2. AI 决策
    response, _, err := c.aiProvider.Analyze(prompt)
    if err != nil {
        return nil, err
    }
    
    // 3. 解析 AI 返回的收集计划
    plan := parseCollectionPlan(response)
    
    // 4. 按优先级收集信息
    ctx := &DiagnosisContext{
        Alert:          alert,
        CollectedLogs:  make(map[string]string),
        CommandResults: make(map[string]string),
    }
    
    log.Printf("🔍 AI 决策: 需要收集 %d 个日志，执行 %d 个命令", 
        len(plan.Logs), len(plan.Commands))
    log.Printf("💡 原因: %s", plan.Reasoning)
    
    // 5. 执行收集
    for _, logInfo := range plan.Logs {
        log.Printf("  → 收集日志: %s", logInfo.Path)
        content, err := c.sshExecutor.TailFile(context.Background(), logInfo.Path, logInfo.Lines)
        if err != nil {
            log.Printf("  ⚠ 收集失败: %v", err)
            continue
        }
        ctx.CollectedLogs[logInfo.Path] = content
        log.Printf("  ✓ 已收集 %d 行", len(strings.Split(content, "\n")))
    }
    
    for _, cmdInfo := range plan.Commands {
        log.Printf("  → 执行命令: %s", cmdInfo.Cmd)
        stdout, _, err := c.sshExecutor.Execute(context.Background(), cmdInfo.Cmd)
        if err != nil {
            log.Printf("  ⚠ 执行失败: %v", err)
            continue
        }
        ctx.CommandResults[cmdInfo.Cmd] = stdout
        log.Printf("  ✓ 已完成")
    }
    
    return ctx, nil
}
```

### 阶段 3: AI 根因分析

```go
// pkg/autonomous/analyzer.go

type RootCauseAnalyzer struct {
    aiProvider common.AIProvider
}

// AI 分析根因
func (a *RootCauseAnalyzer) AnalyzeRootCause(ctx *DiagnosisContext) (*RootCauseAnalysis, error) {
    // 构建完整的分析提示词
    prompt := a.buildAnalysisPrompt(ctx)
    
    log.Printf("🤖 AI 正在分析根本原因...")
    
    // AI 分析
    analysis, solution, err := a.aiProvider.Analyze(prompt)
    if err != nil {
        return nil, err
    }
    
    return &RootCauseAnalysis{
        RootCause:   analysis,
        Explanation: analysis,
        Solutions:   parseSolutions(solution),
    }, nil
}

func (a *RootCauseAnalyzer) buildAnalysisPrompt(ctx *DiagnosisContext) string {
    var sb strings.Builder
    
    sb.WriteString("作为 Ceph 存储专家，请分析以下完整信息并给出根本原因和解决方案。\n\n")
    
    // 告警信息
    sb.WriteString(fmt.Sprintf("## 告警信息\n"))
    sb.WriteString(fmt.Sprintf("名称: %s\n", ctx.Alert.AlertName))
    sb.WriteString(fmt.Sprintf("描述: %s\n", ctx.Alert.Summary))
    sb.WriteString(fmt.Sprintf("节点: %s\n\n", ctx.Alert.Instance))
    
    // 收集的日志
    sb.WriteString("## 收集的日志\n\n")
    for path, content := range ctx.CollectedLogs {
        sb.WriteString(fmt.Sprintf("### %s\n", path))
        sb.WriteString("```\n")
        sb.WriteString(truncate(content, 1500))
        sb.WriteString("\n```\n\n")
    }
    
    // 命令结果
    sb.WriteString("## 诊断命令结果\n\n")
    for cmd, output := range ctx.CommandResults {
        sb.WriteString(fmt.Sprintf("### %s\n", cmd))
        sb.WriteString("```\n")
        sb.WriteString(truncate(output, 1000))
        sb.WriteString("\n```\n\n")
    }
    
    // 要求
    sb.WriteString(`## 请提供

1. **根本原因分析**
   - 为什么会出现这个问题？
   - 是什么导致的？
   - 有哪些相关因素？

2. **修复方案**（按优先级排序）
   方案 1:
   - 描述: [方案描述]
   - 命令: [具体的修复命令]
   - 风险: [执行风险评估]
   - 预期效果: [执行后的预期]
   
   方案 2:
   ...

3. **验证方法**
   - 如何验证问题已解决？
   - 需要检查哪些指标？

以结构化格式返回，便于程序解析。
`)
    
    return sb.String()
}
```

### 阶段 4: 生成和评估修复方案

```go
// pkg/autonomous/solution.go

type SolutionGenerator struct {
    aiProvider    common.AIProvider
    safetyChecker *SafetyChecker
}

type Solution struct {
    ID          string
    Description string
    Commands    []Command
    Risk        RiskLevel  // LOW, MEDIUM, HIGH, CRITICAL
    AutoApply   bool       // 是否可以自动执行
    RequireConfirmation bool
}

type Command struct {
    Cmd          string
    Description  string
    Rollback     string  // 回滚命令
    Verification string  // 验证命令
}

// 生成修复方案
func (g *SolutionGenerator) GenerateSolutions(analysis *RootCauseAnalysis) ([]*Solution, error) {
    solutions := parseSolutionsFromAnalysis(analysis)
    
    // 评估每个方案的风险
    for _, solution := range solutions {
        risk := g.safetyChecker.EvaluateRisk(solution)
        solution.Risk = risk
        
        // 只有低风险的操作才允许自动执行
        solution.AutoApply = (risk == RiskLow)
        solution.RequireConfirmation = (risk >= RiskMedium)
    }
    
    // 按风险排序（低风险优先）
    sort.Slice(solutions, func(i, j int) bool {
        return solutions[i].Risk < solutions[j].Risk
    })
    
    return solutions, nil
}
```

### 阶段 5: 安全检查和自动执行

```go
// pkg/autonomous/executor.go

type ActionExecutor struct {
    sshExecutor   *executor.SSHExecutor
    safetyChecker *SafetyChecker
    auditLogger   *AuditLogger
}

// 执行修复方案
func (e *ActionExecutor) ExecuteSolution(solution *Solution, mode ExecutionMode) error {
    log.Printf("🔧 准备执行修复方案: %s", solution.Description)
    log.Printf("   风险级别: %s", solution.Risk)
    
    // 1. 安全检查
    if err := e.safetyChecker.PreExecutionCheck(solution); err != nil {
        return fmt.Errorf("安全检查失败: %v", err)
    }
    
    // 2. 如果是高风险操作且需要确认
    if solution.RequireConfirmation && mode == ModeAuto {
        log.Printf("⚠️  高风险操作，需要人工确认，发送通知...")
        e.sendConfirmationRequest(solution)
        return nil
    }
    
    // 3. 记录审计日志
    e.auditLogger.LogAction("SOLUTION_EXECUTION_START", solution)
    
    // 4. 执行命令
    for i, cmd := range solution.Commands {
        log.Printf("   [%d/%d] 执行: %s", i+1, len(solution.Commands), cmd.Description)
        
        // 执行命令
        stdout, stderr, err := e.sshExecutor.Execute(context.Background(), cmd.Cmd)
        if err != nil {
            log.Printf("   ❌ 执行失败: %v", err)
            
            // 如果有回滚命令，执行回滚
            if cmd.Rollback != "" {
                log.Printf("   🔄 执行回滚: %s", cmd.Rollback)
                e.sshExecutor.Execute(context.Background(), cmd.Rollback)
            }
            
            e.auditLogger.LogAction("SOLUTION_EXECUTION_FAILED", map[string]string{
                "command": cmd.Cmd,
                "error":   err.Error(),
                "stdout":  stdout,
                "stderr":  stderr,
            })
            
            return err
        }
        
        log.Printf("   ✓ 执行成功")
        
        // 验证结果
        if cmd.Verification != "" {
            log.Printf("   🔍 验证: %s", cmd.Verification)
            verifyOut, _, _ := e.sshExecutor.Execute(context.Background(), cmd.Verification)
            log.Printf("   验证结果: %s", truncate(verifyOut, 200))
        }
        
        // 等待命令生效
        time.Sleep(2 * time.Second)
    }
    
    e.auditLogger.LogAction("SOLUTION_EXECUTION_SUCCESS", solution)
    log.Printf("✅ 修复方案执行完成")
    
    return nil
}
```

### 阶段 6: 验证和学习

```go
// pkg/autonomous/validator.go

type ResultValidator struct {
    sshExecutor *executor.SSHExecutor
    aiProvider  common.AIProvider
}

// 验证问题是否解决
func (v *ResultValidator) ValidateFix(alert *Alert, solution *Solution, 
    waitTime time.Duration) (*ValidationResult, error) {
    
    log.Printf("⏳ 等待 %v 后验证结果...", waitTime)
    time.Sleep(waitTime)
    
    log.Printf("🔍 验证问题是否已解决...")
    
    // 1. 重新检查告警状态
    stillFiring := v.checkAlertStatus(alert)
    
    // 2. 执行验证命令
    var verificationResults []string
    for _, cmd := range solution.Commands {
        if cmd.Verification != "" {
            stdout, _, _ := v.sshExecutor.Execute(context.Background(), cmd.Verification)
            verificationResults = append(verificationResults, stdout)
        }
    }
    
    // 3. 让 AI 判断是否解决
    prompt := fmt.Sprintf(`
根据以下信息判断问题是否已解决：

原始告警: %s
执行的修复: %s
告警状态: %v (true=仍在告警, false=已恢复)

验证结果:
%s

请判断:
1. 问题是否已完全解决？
2. 如果没有，还需要什么操作？
3. 给出信心评分（0-100）
`, alert.AlertName, solution.Description, stillFiring, 
    strings.Join(verificationResults, "\n---\n"))
    
    analysis, _, _ := v.aiProvider.Analyze(prompt)
    
    result := &ValidationResult{
        Fixed:      !stillFiring,
        Confidence: extractConfidence(analysis),
        Analysis:   analysis,
    }
    
    if result.Fixed {
        log.Printf("✅ 验证通过: 问题已解决 (信心: %d%%)", result.Confidence)
    } else {
        log.Printf("❌ 验证失败: 问题仍然存在")
    }
    
    return result, nil
}

// 学习和记录
func (v *ResultValidator) Learn(alert *Alert, solution *Solution, result *ValidationResult) {
    // 记录到知识库
    entry := &KnowledgeEntry{
        ProblemType:   identifyProblemType(alert),
        Symptoms:      alert.Summary,
        Solution:      solution,
        Success:       result.Fixed,
        Confidence:    result.Confidence,
        Timestamp:     time.Now(),
    }
    
    knowledgeBase.Add(entry)
    
    // 如果成功，下次遇到同样问题可以直接应用
    if result.Fixed && result.Confidence > 80 {
        log.Printf("📚 已学习: 下次遇到类似问题将直接应用此方案")
    }
}
```

---

## 🛡️ 安全机制

### 1. 风险评估

```go
// pkg/autonomous/safety.go

type SafetyChecker struct {
    dangerousCommands []string
    whitelist         []string
}

func (s *SafetyChecker) EvaluateRisk(solution *Solution) RiskLevel {
    highRiskKeywords := []string{
        "rm -rf", "dd", "mkfs", "ceph osd destroy",
        "ceph mon remove", "shutdown", "reboot",
    }
    
    mediumRiskKeywords := []string{
        "ceph osd out", "ceph osd stop", "systemctl stop",
        "iptables", "ip link set", "ifconfig down",
    }
    
    for _, cmd := range solution.Commands {
        cmdStr := cmd.Cmd
        
        // 检查高风险命令
        for _, keyword := range highRiskKeywords {
            if strings.Contains(cmdStr, keyword) {
                return RiskCritical
            }
        }
        
        // 检查中风险命令
        for _, keyword := range mediumRiskKeywords {
            if strings.Contains(cmdStr, keyword) {
                return RiskMedium
            }
        }
    }
    
    return RiskLow
}
```

### 2. 命令白名单

```yaml
# config/safety.yaml
autonomous:
  # 自动执行模式
  mode: "semi-auto"  # auto, semi-auto, manual
  
  # 允许自动执行的命令（白名单）
  whitelist:
    - "ceph osd in"           # 恢复 OSD
    - "ceph pg repair"        # 修复 PG
    - "systemctl start"       # 启动服务
    - "systemctl restart"     # 重启服务
    - "truncate -s 0"         # 清空日志
  
  # 禁止自动执行的命令（黑名单）
  blacklist:
    - "rm -rf"
    - "ceph osd destroy"
    - "ceph mon remove"
    - "shutdown"
    - "reboot"
  
  # 需要人工确认的风险级别
  require_confirmation: ["MEDIUM", "HIGH", "CRITICAL"]
  
  # 最大等待时间
  max_wait_time: "30m"
```

### 3. 审计日志

```go
// pkg/autonomous/audit.go

type AuditLogger struct {
    db *sql.DB
}

func (a *AuditLogger) LogAction(action string, details interface{}) {
    entry := AuditEntry{
        Timestamp: time.Now(),
        Action:    action,
        Details:   fmt.Sprintf("%+v", details),
        User:      "ai-agent",
    }
    
    // 记录到数据库
    a.db.Exec(`
        INSERT INTO audit_logs (timestamp, action, details, user)
        VALUES ($1, $2, $3, $4)
    `, entry.Timestamp, entry.Action, entry.Details, entry.User)
    
    // 同时记录到文件
    log.Printf("[AUDIT] %s: %s", action, entry.Details)
}
```

---

## 📊 实际案例演示

### 案例 1: OSD Down 自动修复

```
15:23:45 🚨 收到告警: CephOSDDown - OSD.5 is down
15:23:46 🤖 启动 AI 自主诊断流程...

15:23:47 🔍 AI 决策: 需要收集 3 个日志，执行 4 个命令
         💡 原因: OSD down 可能由进程崩溃、磁盘故障或网络问题导致

15:23:48   → 收集日志: /var/log/ceph/ceph-osd.5.log
15:23:49   ✓ 已收集 200 行
15:23:50   → 收集日志: /var/log/messages
15:23:51   ✓ 已收集 100 行
15:23:52   → 执行命令: ceph osd tree
15:23:53   ✓ 已完成
15:23:54   → 执行命令: systemctl status ceph-osd@5
15:23:55   ✓ 已完成

15:23:56 🤖 AI 正在分析根本原因...

15:24:10 📊 根因分析:
         OSD.5 进程因内存不足被 OOM Killer 杀死。
         日志显示: "Out of memory: Killed process 12345 (ceph-osd)"
         系统内存压力大，OSD 内存配置可能过高。

15:24:11 💡 生成了 2 个修复方案:
         
         方案 1 (风险: LOW, 可自动执行):
         1. 重启 OSD 服务
            命令: systemctl start ceph-osd@5
         2. 验证 OSD 已 up
            验证: ceph osd tree | grep "osd.5"
         
         方案 2 (风险: MEDIUM, 需要确认):
         1. 调整 OSD 内存限制
         2. 重启 OSD

15:24:12 ✅ 选择方案 1 (低风险，自动执行)

15:24:13 🔧 准备执行修复方案: 重启 OSD.5
            风险级别: LOW
            
15:24:14    [1/1] 执行: 重启 OSD 服务
15:24:16    ✓ 执行成功
15:24:17    🔍 验证: ceph osd tree | grep "osd.5"
15:24:18    验证结果: 5  hdd  1.82330  osd.5     up  1.00000  1.00000

15:24:19 ✅ 修复方案执行完成

15:24:20 ⏳ 等待 2m0s 后验证结果...

15:26:21 🔍 验证问题是否已解决...
15:26:22 ✅ 验证通过: 问题已解决 (信心: 95%)
15:26:23 📚 已学习: 下次遇到类似问题将直接应用此方案

15:26:24 📤 发送飞书通知: OSD.5 故障已自动修复
```

### 案例 2: 磁盘满需要人工确认

```
16:45:12 🚨 收到告警: HighDiskUsage - /var/lib/ceph 95% full

16:45:20 🤖 AI 分析: 
         磁盘快满了，建议清理旧的 PG 日志
         
16:45:21 💡 生成修复方案:
         方案 1 (风险: MEDIUM):
         1. 清理 Ceph 日志: find /var/log/ceph -mtime +7 -delete
         2. 清理临时文件: rm -f /var/lib/ceph/tmp/*
         
16:45:22 ⚠️  中风险操作，需要人工确认，发送通知...

16:45:23 📤 飞书通知: [需要确认] AI 建议清理磁盘
         [同意] [拒绝] [稍后]
         
--- 用户点击 [同意] ---

16:47:30 ✅ 收到人工确认，开始执行...
16:47:35 ✅ 修复完成，磁盘使用率降至 75%
```

---

## 🎯 使用方式

### 1. 启动自主 Agent

```bash
# 全自动模式（仅低风险操作）
infra-agent autonomous start \
  --mode auto \
  --cluster prod-bj-01 \
  --enable-auto-fix

# 半自动模式（中高风险需要确认）
infra-agent autonomous start \
  --mode semi-auto \
  --cluster prod-bj-01

# 手动模式（仅分析，不执行）
infra-agent autonomous start \
  --mode manual \
  --cluster prod-bj-01
```

### 2. 配置告警接收

```yaml
# config/autonomous.yaml
autonomous:
  enabled: true
  mode: "semi-auto"
  
  # 告警来源
  alert_sources:
    - type: "alertmanager"
      url: "http://alertmanager:9093"
      
    - type: "prometheus"
      url: "http://prometheus:9090"
  
  # 自动修复规则
  auto_fix_rules:
    - alert_name: "CephOSDDown"
      auto_apply: true
      max_retries: 3
      
    - alert_name: "CephSlowOps"
      auto_apply: true
      
    - alert_name: "HighDiskUsage"
      auto_apply: false  # 需要人工确认
```

### 3. 知识库管理

```bash
# 查看已学习的解决方案
infra-agent knowledge list

# 导出知识库
infra-agent knowledge export --output solutions.json

# 导入知识库
infra-agent knowledge import --input solutions.json
```

---

## ✅ 总结

**可行性**: ✅ **完全可行！**

**核心优势**:
1. ✅ AI 自主决策收集什么信息
2. ✅ 自动 SSH 到节点查看日志
3. ✅ AI 分析根因并生成修复方案
4. ✅ 低风险操作自动执行
5. ✅ 高风险操作需要确认
6. ✅ 完整的审计和回滚机制
7. ✅ 学习和优化能力

**安全保障**:
- 命令白名单/黑名单
- 风险级别评估
- 人工确认机制
- 完整审计日志
- 回滚能力

**下一步**:
需要我实现这个自主诊断系统吗？
