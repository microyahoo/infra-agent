package patrol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/diagnosis"
	"github.com/microyahoo/infra-agent/pkg/executor"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Scheduler 巡检调度器
type Scheduler struct {
	clientset    *kubernetes.Clientset
	restConfig   *rest.Config
	registry     *analyzer.AnalyzerRegistry
	aiProvider   common.AIProvider
	notifier     common.Notifier
	interval     time.Duration
	rules        []PatrolRule
	running      bool
	stopCh       chan struct{}
	mu           sync.Mutex
	lastReport   *common.PatrolReport
	diagnoser    *diagnosis.AutoDiagnoser
}

// PatrolRule 巡检规则
type PatrolRule struct {
	Name      string   `yaml:"name"`
	Enabled   bool     `yaml:"enabled"`
	Severity  string   `yaml:"severity"`
	Analyzers []string `yaml:"analyzers"`
}

// NewScheduler 创建巡检调度器
func NewScheduler(
	clientset *kubernetes.Clientset,
	restConfig *rest.Config,
	registry *analyzer.AnalyzerRegistry,
	aiProvider common.AIProvider,
	notifier common.Notifier,
	interval time.Duration,
) *Scheduler {
	remoteExec := executor.NewRemoteExecutor(clientset, restConfig)
	diagnoser := diagnosis.NewAutoDiagnoser(remoteExec, aiProvider)

	return &Scheduler{
		clientset:  clientset,
		restConfig: restConfig,
		registry:   registry,
		aiProvider: aiProvider,
		notifier:   notifier,
		interval:   interval,
		stopCh:     make(chan struct{}),
		diagnoser:  diagnoser,
	}
}

// Start 启动巡检调度
func (s *Scheduler) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler is already running")
	}
	s.running = true
	s.mu.Unlock()

	fmt.Printf("🚀 巡检调度器已启动，间隔: %s\n", s.interval)

	// 启动定时器
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 立即执行一次
	s.runPatrol()

	for {
		select {
		case <-ticker.C:
			s.runPatrol()
		case <-s.stopCh:
			fmt.Println("🛑 巡检调度器已停止")
			return nil
		}
	}
}

// Stop 停止巡检调度
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	close(s.stopCh)
}

// IsRunning 检查是否正在运行
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// GetLastReport 获取最后一次巡检报告
func (s *Scheduler) GetLastReport() *common.PatrolReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReport
}

// runPatrol 执行一次巡检
func (s *Scheduler) runPatrol() {
	ctx := context.Background()
	startTime := time.Now()

	fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("🔍 开始巡检: %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	// 生成报告 ID
	reportID := fmt.Sprintf("patrol-%d", startTime.Unix())

	// 执行所有启用的分析器
	var allResults []common.Result
	enabledAnalyzers := s.registry.GetEnabled()
	metrics := common.PatrolMetrics{}

	for _, a := range enabledAnalyzers {
		fmt.Printf("  → 运行 %s...\n", a.GetName())

		results, err := a.Analyze(ctx, s.clientset, "")
		if err != nil {
			fmt.Printf("  ⚠ 警告: %s 分析失败: %v\n", a.GetName(), err)
			continue
		}

		if len(results) > 0 {
			fmt.Printf("  ✗ 发现 %d 个问题\n", len(results))
			allResults = append(allResults, results...)
		} else {
			fmt.Printf("  ✓ 未发现问题\n")
		}

		// 更新指标
		s.updateMetrics(&metrics, a.GetKind(), len(results))
	}

	// 进行 AI 诊断
	var failures []common.Failure
	if len(allResults) > 0 && s.aiProvider != nil {
		fmt.Printf("\n🤖 正在进行 AI 诊断...\n")

		for i, result := range allResults {
			fmt.Printf("  [%d/%d] 诊断 %s/%s...\n", i+1, len(allResults), result.Namespace, result.Name)

			// 自动诊断
			diagCtx, err := s.diagnoser.Diagnose(ctx, result)
			if err != nil {
				fmt.Printf("  ⚠ 诊断失败: %v\n", err)
				// 即使诊断失败，也创建一个基本的 Failure
				failure := common.Failure{
					Result:   result,
					Severity: s.determineSeverity(result),
					Category: "unknown",
				}
				failures = append(failures, failure)
				continue
			}

			// 构建 Failure
			failure := common.Failure{
				Result:   result,
				Severity: s.determineSeverity(result),
				Category: "availability",
				Explain:  diagCtx.AIAnalysis,
				Solution: joinStrings(diagCtx.Recommendations, "\n"),
			}

			failures = append(failures, failure)
			fmt.Printf("  ✓ 诊断完成\n")
		}
	}

	// 构建报告
	endTime := time.Now()
	report := &common.PatrolReport{
		ID:        reportID,
		StartTime: startTime,
		EndTime:   endTime,
		Duration:  endTime.Sub(startTime).String(),
		Status:    s.determineStatus(failures),
		Summary:   s.calculateSummary(failures),
		Findings:  failures,
		Metrics:   metrics,
	}

	// 保存报告
	s.mu.Lock()
	s.lastReport = report
	s.mu.Unlock()

	// 打印摘要
	s.printSummary(report)

	// 发送通知
	if s.notifier != nil && len(failures) > 0 {
		fmt.Printf("\n📤 发送巡检报告到飞书...\n")
		if err := s.notifier.SendReport(report); err != nil {
			fmt.Printf("  ⚠ 发送通知失败: %v\n", err)
		} else {
			fmt.Printf("  ✓ 通知已发送\n")
		}
	}
}

// updateMetrics 更新指标
func (s *Scheduler) updateMetrics(metrics *common.PatrolMetrics, kind string, count int) {
	switch kind {
	case "Node":
		metrics.NodesChecked += count
	case "Pod":
		metrics.PodsChecked += count
	case "PersistentVolumeClaim":
		metrics.PVCsChecked += count
	case "Service":
		metrics.ServicesChecked += count
	}
}

// determineSeverity 确定严重程度
func (s *Scheduler) determineSeverity(result common.Result) string {
	for _, err := range result.Error {
		errText := err.Text
		if containsAny(errText, []string{"OOMKilled", "CrashLoopBackOff", "Failed", "not ready"}) {
			return "critical"
		}
		if containsAny(errText, []string{"Pending", "high restart", "Lost", "pressure"}) {
			return "high"
		}
	}
	return "medium"
}

// determineStatus 确定状态
func (s *Scheduler) determineStatus(failures []common.Failure) string {
	if len(failures) == 0 {
		return "OK"
	}

	for _, f := range failures {
		if f.Severity == "critical" {
			return "Error"
		}
	}

	return "Warning"
}

// calculateSummary 计算摘要
func (s *Scheduler) calculateSummary(failures []common.Failure) common.PatrolSummary {
	summary := common.PatrolSummary{
		TotalIssues: len(failures),
	}

	for _, f := range failures {
		switch f.Severity {
		case "critical":
			summary.CriticalIssues++
		case "high":
			summary.HighIssues++
		case "medium":
			summary.MediumIssues++
		case "low":
			summary.LowIssues++
		}
	}

	return summary
}

// printSummary 打印摘要
func (s *Scheduler) printSummary(report *common.PatrolReport) {
	fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("📊 巡检摘要\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("状态: %s\n", report.Status)
	fmt.Printf("耗时: %s\n", report.Duration)
	fmt.Printf("总问题数: %d\n", report.Summary.TotalIssues)
	fmt.Printf("  🔴 严重: %d\n", report.Summary.CriticalIssues)
	fmt.Printf("  🟠 高: %d\n", report.Summary.HighIssues)
	fmt.Printf("  🟡 中: %d\n", report.Summary.MediumIssues)
	fmt.Printf("  🟢 低: %d\n", report.Summary.LowIssues)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

// 辅助函数

func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if contains(s, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
