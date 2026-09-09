package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/executor"
)

// AutoDiagnoser 自动诊断器
type AutoDiagnoser struct {
	executor   *executor.RemoteExecutor
	aiProvider common.AIProvider
}

// NewAutoDiagnoser 创建自动诊断器
func NewAutoDiagnoser(exec *executor.RemoteExecutor, aiProvider common.AIProvider) *AutoDiagnoser {
	return &AutoDiagnoser{
		executor:   exec,
		aiProvider: aiProvider,
	}
}

// DiagnosisContext 诊断上下文
type DiagnosisContext struct {
	Result          common.Result                // 初步分析结果
	PodLogs         string                       // Pod 日志
	PreviousLogs    string                       // 之前的日志（如果 Pod 重启）
	CommandResults  []executor.ExecutionResult   // 命令执行结果
	AIAnalysis      string                       // AI 分析结果
	Recommendations []string                     // 推荐的操作
}

// Diagnose 自动诊断问题
func (d *AutoDiagnoser) Diagnose(ctx context.Context, result common.Result) (*DiagnosisContext, error) {
	diagCtx := &DiagnosisContext{
		Result:         result,
		CommandResults: make([]executor.ExecutionResult, 0),
	}

	// 步骤 1: 收集基础信息
	if err := d.collectBasicInfo(ctx, diagCtx); err != nil {
		return diagCtx, fmt.Errorf("failed to collect basic info: %w", err)
	}

	// 步骤 2: 第一轮 AI 分析 - 确定需要执行的诊断命令
	commands, err := d.determineCommands(diagCtx)
	if err != nil {
		return diagCtx, fmt.Errorf("failed to determine commands: %w", err)
	}

	// 步骤 3: 执行诊断命令
	if err := d.executeCommands(ctx, diagCtx, commands); err != nil {
		return diagCtx, fmt.Errorf("failed to execute commands: %w", err)
	}

	// 步骤 4: 综合分析并生成诊断报告
	if err := d.generateDiagnosis(diagCtx); err != nil {
		return diagCtx, fmt.Errorf("failed to generate diagnosis: %w", err)
	}

	return diagCtx, nil
}

// collectBasicInfo 收集基础信息
func (d *AutoDiagnoser) collectBasicInfo(ctx context.Context, diagCtx *DiagnosisContext) error {
	// 只有 Pod 类型的问题才收集日志
	if diagCtx.Result.Kind != "Pod" {
		return nil
	}

	// 获取 Pod 当前日志
	logs, err := d.executor.GetPodLogs(ctx, diagCtx.Result.Namespace, diagCtx.Result.Name, "", 200, false)
	if err == nil {
		diagCtx.PodLogs = logs
	}

	// 尝试获取之前的日志（如果 Pod 重启过）
	prevLogs, err := d.executor.GetPodLogs(ctx, diagCtx.Result.Namespace, diagCtx.Result.Name, "", 200, true)
	if err == nil {
		diagCtx.PreviousLogs = prevLogs
	}

	return nil
}

// determineCommands 通过 AI 决定需要执行的诊断命令
func (d *AutoDiagnoser) determineCommands(diagCtx *DiagnosisContext) ([]executor.DiagnosticCommand, error) {
	// 构建上下文提示
	prompt := d.buildDiagnosticPrompt(diagCtx)

	// 询问 AI 需要执行哪些命令
	analysis, _, err := d.aiProvider.Analyze(prompt)
	if err != nil {
		return nil, err
	}

	// 解析 AI 返回的命令列表
	commands := d.parseCommandsFromAI(analysis, diagCtx.Result.Kind)

	return commands, nil
}

// buildDiagnosticPrompt 构建诊断提示词
func (d *AutoDiagnoser) buildDiagnosticPrompt(diagCtx *DiagnosisContext) string {
	var sb strings.Builder

	sb.WriteString("我是一个 Kubernetes 基础设施诊断 Agent。我发现了以下问题：\n\n")
	sb.WriteString(fmt.Sprintf("资源类型: %s\n", diagCtx.Result.Kind))
	sb.WriteString(fmt.Sprintf("资源名称: %s/%s\n", diagCtx.Result.Namespace, diagCtx.Result.Name))
	sb.WriteString("发现的错误:\n")

	for _, e := range diagCtx.Result.Error {
		sb.WriteString(fmt.Sprintf("- %s\n", e.Text))
	}

	if diagCtx.PodLogs != "" {
		sb.WriteString("\n最近的日志:\n")
		sb.WriteString("```\n")
		sb.WriteString(truncateString(diagCtx.PodLogs, 2000))
		sb.WriteString("\n```\n")
	}

	if diagCtx.PreviousLogs != "" {
		sb.WriteString("\n之前的日志（Pod 重启前）:\n")
		sb.WriteString("```\n")
		sb.WriteString(truncateString(diagCtx.PreviousLogs, 2000))
		sb.WriteString("\n```\n")
	}

	sb.WriteString("\n为了进一步诊断，我可以在 Pod 中执行以下类型的命令：\n")
	sb.WriteString("- check-process: 检查进程状态 (ps aux)\n")
	sb.WriteString("- check-disk: 检查磁盘使用 (df -h)\n")
	sb.WriteString("- check-memory: 检查内存使用 (free -h)\n")
	sb.WriteString("- check-network: 检查网络连接 (netstat -tulpn)\n")
	sb.WriteString("- check-dns: 检查 DNS 解析\n")
	sb.WriteString("- check-env: 检查环境变量\n")
	sb.WriteString("- custom: 自定义命令\n")

	sb.WriteString("\n请以 JSON 格式返回你建议执行的命令列表，用于进一步诊断：\n")
	sb.WriteString(`{"commands": ["check-process", "check-disk"], "custom_commands": [{"name": "check-app-config", "command": "cat /app/config.yaml"}]}`)

	return sb.String()
}

// parseCommandsFromAI 从 AI 响应中解析命令
func (d *AutoDiagnoser) parseCommandsFromAI(aiResponse string, kind string) []executor.DiagnosticCommand {
	// 尝试从 AI 响应中提取 JSON
	var commandsResponse struct {
		Commands       []string `json:"commands"`
		CustomCommands []struct {
			Name    string `json:"name"`
			Command string `json:"command"`
		} `json:"custom_commands"`
	}

	// 查找 JSON 内容
	jsonStart := strings.Index(aiResponse, "{")
	jsonEnd := strings.LastIndex(aiResponse, "}")

	if jsonStart != -1 && jsonEnd != -1 {
		jsonStr := aiResponse[jsonStart : jsonEnd+1]
		if err := json.Unmarshal([]byte(jsonStr), &commandsResponse); err == nil {
			// 解析成功，构建命令列表
			var commands []executor.DiagnosticCommand
			allCommands := executor.GetCommonDiagnosticCommands()

			// 添加预定义命令
			for _, cmdName := range commandsResponse.Commands {
				for _, cmd := range allCommands.PodCommands {
					if cmd.Name == cmdName {
						commands = append(commands, cmd)
						break
					}
				}
			}

			// 添加自定义命令
			for _, custom := range commandsResponse.CustomCommands {
				commands = append(commands, executor.DiagnosticCommand{
					Name:        custom.Name,
					Description: "Custom diagnostic command",
					Command:     []string{"sh", "-c", custom.Command},
					Category:    "custom",
				})
			}

			return commands
		}
	}

	// 如果解析失败，返回默认命令集
	return d.getDefaultCommands(kind)
}

// getDefaultCommands 获取默认诊断命令
func (d *AutoDiagnoser) getDefaultCommands(kind string) []executor.DiagnosticCommand {
	allCommands := executor.GetCommonDiagnosticCommands()

	switch kind {
	case "Pod":
		// Pod 问题默认执行进程、内存、磁盘检查
		return []executor.DiagnosticCommand{
			allCommands.PodCommands[0], // check-process
			allCommands.PodCommands[1], // check-disk
			allCommands.PodCommands[2], // check-memory
		}
	default:
		return []executor.DiagnosticCommand{}
	}
}

// executeCommands 执行诊断命令
func (d *AutoDiagnoser) executeCommands(ctx context.Context, diagCtx *DiagnosisContext, commands []executor.DiagnosticCommand) error {
	// 只对 Pod 执行命令
	if diagCtx.Result.Kind != "Pod" {
		return nil
	}

	for _, cmd := range commands {
		stdout, stderr, err := d.executor.ExecuteCommand(
			ctx,
			diagCtx.Result.Namespace,
			diagCtx.Result.Name,
			"", // 空字符串表示使用第一个容器
			cmd.Command,
		)

		diagCtx.CommandResults = append(diagCtx.CommandResults, executor.ExecutionResult{
			Command:     strings.Join(cmd.Command, " "),
			Description: cmd.Description,
			Stdout:      stdout,
			Stderr:      stderr,
			Error:       err,
		})
	}

	return nil
}

// generateDiagnosis 生成综合诊断
func (d *AutoDiagnoser) generateDiagnosis(diagCtx *DiagnosisContext) error {
	// 构建最终诊断提示
	prompt := d.buildFinalDiagnosticPrompt(diagCtx)

	// AI 综合分析
	analysis, solution, err := d.aiProvider.Analyze(prompt)
	if err != nil {
		return err
	}

	diagCtx.AIAnalysis = analysis
	diagCtx.Recommendations = parseSolutions(solution)

	return nil
}

// buildFinalDiagnosticPrompt 构建最终诊断提示
func (d *AutoDiagnoser) buildFinalDiagnosticPrompt(diagCtx *DiagnosisContext) string {
	var sb strings.Builder

	sb.WriteString("基于以下所有信息，请提供详细的根因分析和解决方案：\n\n")
	sb.WriteString(fmt.Sprintf("问题: %s/%s (%s)\n", diagCtx.Result.Namespace, diagCtx.Result.Name, diagCtx.Result.Kind))

	for _, e := range diagCtx.Result.Error {
		sb.WriteString(fmt.Sprintf("- %s\n", e.Text))
	}

	if diagCtx.PodLogs != "" {
		sb.WriteString("\n应用日志:\n```\n")
		sb.WriteString(truncateString(diagCtx.PodLogs, 1500))
		sb.WriteString("\n```\n")
	}

	sb.WriteString("\n诊断命令执行结果:\n")
	for _, result := range diagCtx.CommandResults {
		sb.WriteString(fmt.Sprintf("\n## %s\n", result.Description))
		if result.Error != nil {
			sb.WriteString(fmt.Sprintf("执行失败: %v\n", result.Error))
		} else {
			sb.WriteString("```\n")
			sb.WriteString(truncateString(result.Stdout, 500))
			sb.WriteString("\n```\n")
		}
	}

	sb.WriteString("\n请提供:\n")
	sb.WriteString("1. 根本原因分析\n")
	sb.WriteString("2. 详细的解决步骤\n")
	sb.WriteString("3. 预防措施建议\n")

	return sb.String()
}

// parseSolutions 解析解决方案
func parseSolutions(solution string) []string {
	lines := strings.Split(solution, "\n")
	var recommendations []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && (strings.HasPrefix(line, "-") || strings.HasPrefix(line, "•") || strings.HasPrefix(line, "*")) {
			recommendations = append(recommendations, strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "•"), "*"))
		}
	}

	if len(recommendations) == 0 {
		recommendations = []string{solution}
	}

	return recommendations
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}
