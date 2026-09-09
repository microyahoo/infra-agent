package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"encoding/json"

	"github.com/microyahoo/infra-agent/pkg/ai/anthropic"
	"github.com/microyahoo/infra-agent/pkg/ai/openai"
	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/analyzer/k8s"
	"github.com/microyahoo/infra-agent/pkg/analyzer/storage"
	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/diagnosis"
	"github.com/microyahoo/infra-agent/pkg/executor"
	"github.com/microyahoo/infra-agent/pkg/kubernetes"
	"github.com/microyahoo/infra-agent/pkg/notifier/feishu"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	explain          bool
	outputFmt        string
	filters          []string
	anonymize        bool
	withDoc          bool
	timeout          int
	autoDiagnose     bool
	sendNotification bool
)

// analyzeCmd represents the analyze command
var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "分析 Kubernetes 集群并识别问题",
	Long: `分析 Kubernetes 集群中的资源，识别潜在问题。

支持自动诊断功能：当发现问题时，自动在目标服务器上执行诊断命令，
收集日志和系统信息，通过 AI 进行深度分析，最终给出详细的根因分析和解决方案。

示例:
  # 基本分析
  infra-agent analyze

  # 使用 AI 解释问题
  infra-agent analyze --explain

  # 启用自动诊断（会在 Pod 中执行命令）
  infra-agent analyze --explain --auto-diagnose

  # 分析并发送飞书通知
  infra-agent analyze --explain --auto-diagnose --notify

  # 按资源类型过滤
  infra-agent analyze --filter pod,pvc -n default

  # 输出为 JSON 格式
  infra-agent analyze --output json
`,
	RunE: runAnalyze,
}

func init() {
	rootCmd.AddCommand(analyzeCmd)

	analyzeCmd.Flags().BoolVar(&explain, "explain", false, "使用 AI 解释分析结果")
	analyzeCmd.Flags().StringVarP(&outputFmt, "output", "o", "text", "输出格式 (text, json, yaml)")
	analyzeCmd.Flags().StringSliceVarP(&filters, "filter", "f", []string{}, "按资源类型过滤 (例如: pod,pvc,service)")
	analyzeCmd.Flags().BoolVar(&anonymize, "anonymize", false, "在发送到 AI 之前对数据进行脱敏")
	analyzeCmd.Flags().BoolVar(&withDoc, "with-doc", false, "包含 Kubernetes 官方文档链接")
	analyzeCmd.Flags().IntVar(&timeout, "timeout", 60, "分析超时时间（秒）")
	analyzeCmd.Flags().BoolVar(&autoDiagnose, "auto-diagnose", false, "启用自动诊断（在 Pod 中执行命令收集信息）")
	analyzeCmd.Flags().BoolVar(&sendNotification, "notify", false, "发送通知到飞书")
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	startTime := time.Now()

	// 创建 Kubernetes 客户端
	client, restConfig, err := kubernetes.NewClientWithConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// 创建分析器注册表
	registry := analyzer.NewAnalyzerRegistry()

	// 注册分析器
	registry.Register(k8s.NewPodAnalyzer())
	registry.Register(k8s.NewDeploymentAnalyzer())
	registry.Register(k8s.NewServiceAnalyzer())
	registry.Register(k8s.NewNodeAnalyzer())
	registry.Register(k8s.NewStatefulSetAnalyzer())
	registry.Register(storage.NewPVCAnalyzer())
	registry.Register(storage.NewRookCephAnalyzer())
	registry.Register(storage.NewDiskAnalyzer())

	// 应用过滤器
	if len(filters) > 0 {
		filterMap := make(map[string]bool)
		for _, f := range filters {
			filterMap[f] = true
		}

		for _, a := range registry.List() {
			if !filterMap[a.GetKind()] && !filterMap[a.GetName()] {
				a.SetEnabled(false)
			}
		}
	}

	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// 执行分析
	var allResults []common.Result
	enabledAnalyzers := registry.GetEnabled()

	fmt.Fprintf(os.Stderr, "🔍 正在运行 %d 个分析器...\n", len(enabledAnalyzers))

	for _, a := range enabledAnalyzers {
		fmt.Fprintf(os.Stderr, "  → 运行 %s...\n", a.GetName())

		results, err := a.Analyze(ctx, client, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ 警告: %s 分析失败: %v\n", a.GetName(), err)
			continue
		}

		if len(results) > 0 {
			fmt.Fprintf(os.Stderr, "  ✗ 发现 %d 个问题\n", len(results))
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ 未发现问题\n")
		}

		allResults = append(allResults, results...)
	}

	// 构建分析结果
	analysisResult := common.AnalysisResult{
		Results:         allResults,
		Problems:        len(allResults),
		AnalyzedAt:      time.Now(),
		AnalyzeDuration: time.Since(startTime).String(),
	}

	if len(allResults) == 0 {
		analysisResult.Status = "OK"
		fmt.Fprintf(os.Stderr, "\n✅ 分析完成：未发现问题\n")
		return outputResult(analysisResult, outputFmt)
	}

	analysisResult.Status = "Warning"

	// 如果启用自动诊断
	if autoDiagnose && explain {
		fmt.Fprintf(os.Stderr, "\n🤖 启动自动诊断...\n")

		// 创建远程执行器
		remoteExec := executor.NewRemoteExecutor(client, restConfig)

		// 获取 AI Provider
		aiProvider, err := getAIProvider()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ AI Provider 初始化失败: %v\n", err)
			fmt.Fprintf(os.Stderr, "  ℹ 将跳过自动诊断\n")
			analysisResult.Status = "Error"
			return outputResult(analysisResult, outputFmt)
		}

		// 创建自动诊断器
		diagnoser := diagnosis.NewAutoDiagnoser(remoteExec, aiProvider)

		// 对每个问题进行深度诊断
		var failures []common.Failure
		for i, result := range allResults {
			fmt.Fprintf(os.Stderr, "\n  [%d/%d] 诊断 %s/%s...\n", i+1, len(allResults), result.Namespace, result.Name)

			// 执行自动诊断
			diagCtx, err := diagnoser.Diagnose(ctx, result)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ 诊断失败: %v\n", err)
				// 即使诊断失败，也创建一个基本的 Failure
				failure := common.Failure{
					Result:   result,
					Severity: determineSeverity(result),
					Category: "availability",
				}
				failures = append(failures, failure)
				continue
			}

			// 构建 Failure
			failure := common.Failure{
				Result:   result,
				Severity: determineSeverity(result),
				Category: "availability",
				Solution: joinStrings(diagCtx.Recommendations, "\n"),
				Explain:  diagCtx.AIAnalysis,
			}

			failures = append(failures, failure)
			fmt.Fprintf(os.Stderr, "  ✓ 诊断完成\n")
		}

		analysisResult.Failures = failures
	}

	// 发送通知
	if sendNotification && len(analysisResult.Failures) > 0 {
		fmt.Fprintf(os.Stderr, "\n📤 发送飞书通知...\n")

		webhookURL := viper.GetString("notifiers.feishu.webhookURL")
		secret := viper.GetString("notifiers.feishu.secret")

		if webhookURL == "" {
			fmt.Fprintf(os.Stderr, "  ⚠ 未配置飞书 Webhook URL，跳过通知\n")
		} else {
			notifier := feishu.NewFeishuNotifier(webhookURL, secret)

			for _, failure := range analysisResult.Failures {
				err := notifier.SendDiagnosisResult(&failure, "")
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ 发送通知失败: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "  ✓ 通知已发送\n")
				}
			}
		}
	}

	// 输出结果
	return outputResult(analysisResult, outputFmt)
}

func determineSeverity(result common.Result) string {
	// 简单的严重性判断逻辑
	for _, err := range result.Error {
		errText := err.Text
		if containsAny(errText, []string{"OOMKilled", "CrashLoopBackOff", "Failed"}) {
			return "critical"
		}
		if containsAny(errText, []string{"Pending", "high restart", "Lost"}) {
			return "high"
		}
	}
	return "medium"
}

func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if contains(s, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func outputResult(result common.AnalysisResult, format string) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	case "yaml":
		encoder := yaml.NewEncoder(os.Stdout)
		return encoder.Encode(result)
	default:
		return outputText(result)
	}
}

func outputText(result common.AnalysisResult) error {
	fmt.Printf("\n📊 分析结果:\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("状态: %s\n", result.Status)
	fmt.Printf("发现问题: %d\n", result.Problems)
	fmt.Printf("分析耗时: %s\n", result.AnalyzeDuration)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	if result.Problems == 0 {
		fmt.Println("✅ 未发现问题")
		return nil
	}

	// 如果有带诊断的失败
	if len(result.Failures) > 0 {
		for i, failure := range result.Failures {
			severityEmoji := getSeverityEmoji(failure.Severity)
			fmt.Printf("[%d] %s %s/%s (%s)\n", i+1, severityEmoji, failure.Namespace, failure.Name, failure.Kind)
			fmt.Printf("    严重程度: %s\n", failure.Severity)

			for _, e := range failure.Error {
				fmt.Printf("    ❌ %s\n", e.Text)
			}

			if failure.Explain != "" {
				fmt.Printf("\n    🤖 AI 分析:\n")
				fmt.Printf("    %s\n", indentText(failure.Explain, "    "))
			}

			if failure.Solution != "" {
				fmt.Printf("\n    💡 解决方案:\n")
				fmt.Printf("    %s\n", indentText(failure.Solution, "    "))
			}

			fmt.Println()
		}
	} else {
		// 只有基本结果
		for i, r := range result.Results {
			fmt.Printf("[%d] %s/%s (%s)\n", i+1, r.Namespace, r.Name, r.Kind)
			for _, e := range r.Error {
				fmt.Printf("    ❌ %s\n", e.Text)
			}
			fmt.Println()
		}
	}

	return nil
}

func getSeverityEmoji(severity string) string {
	switch severity {
	case "critical":
		return "🔴"
	case "high":
		return "🟠"
	case "medium":
		return "🟡"
	case "low":
		return "🟢"
	default:
		return "⚪"
	}
}

func indentText(text, indent string) string {
	lines := splitLines(text)
	var result string
	for _, line := range lines {
		if line != "" {
			result += indent + line + "\n"
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	var current string
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(s[i])
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
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

// getAIProvider 获取 AI Provider (从 patrol.go 移过来的共享函数)
func getAIProvider() (common.AIProvider, error) {
	provider := viper.GetString("ai.provider")
	if provider == "" {
		provider = "openai"
	}

	switch provider {
	case "openai":
		apiKey := viper.GetString("ai.openai.apiKey")
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI API key not configured")
		}
		model := viper.GetString("ai.openai.model")
		baseURL := viper.GetString("ai.openai.baseURL")
		return openai.NewOpenAIProvider(apiKey, model, baseURL), nil

	case "anthropic":
		apiKey := viper.GetString("ai.anthropic.apiKey")
		if apiKey == "" {
			return nil, fmt.Errorf("Anthropic API key not configured")
		}
		model := viper.GetString("ai.anthropic.model")
		baseURL := viper.GetString("ai.anthropic.baseURL")
		return anthropic.NewAnthropicProvider(apiKey, model, baseURL), nil

	default:
		return nil, fmt.Errorf("unsupported AI provider: %s", provider)
	}
}
