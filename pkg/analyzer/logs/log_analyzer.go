package logs

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
)

// LogAnalyzer 日志分析器
type LogAnalyzer struct {
	// 错误模式
	errorPatterns []*regexp.Regexp
	// 警告模式
	warningPatterns []*regexp.Regexp
}

// NewLogAnalyzer 创建日志分析器
func NewLogAnalyzer() *LogAnalyzer {
	return &LogAnalyzer{
		errorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)error`),
			regexp.MustCompile(`(?i)exception`),
			regexp.MustCompile(`(?i)fatal`),
			regexp.MustCompile(`(?i)panic`),
			regexp.MustCompile(`(?i)failed`),
			regexp.MustCompile(`(?i)cannot`),
			regexp.MustCompile(`(?i)unable to`),
			regexp.MustCompile(`(?i)out of memory`),
			regexp.MustCompile(`(?i)oom`),
			regexp.MustCompile(`(?i)connection refused`),
			regexp.MustCompile(`(?i)timeout`),
			regexp.MustCompile(`(?i)disk.*full`),
			regexp.MustCompile(`(?i)no space left`),
		},
		warningPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)warn`),
			regexp.MustCompile(`(?i)deprecated`),
			regexp.MustCompile(`(?i)retry`),
			regexp.MustCompile(`(?i)slow`),
		},
	}
}

// AnalyzeLogs 分析日志内容
func (a *LogAnalyzer) AnalyzeLogs(ctx context.Context, logs string) *LogAnalysisResult {
	result := &LogAnalysisResult{
		ErrorCount:   0,
		WarningCount: 0,
		ErrorLines:   make([]string, 0),
		WarningLines: make([]string, 0),
		Patterns:     make(map[string]int),
		StartTime:    time.Now(),
	}

	scanner := bufio.NewScanner(strings.NewReader(logs))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// 检查错误模式
		if a.containsError(line) {
			result.ErrorCount++
			if len(result.ErrorLines) < 20 { // 最多保存 20 行
				result.ErrorLines = append(result.ErrorLines, fmt.Sprintf("L%d: %s", lineNum, line))
			}

			// 统计错误模式
			pattern := a.extractPattern(line)
			result.Patterns[pattern]++
		}

		// 检查警告模式
		if a.containsWarning(line) {
			result.WarningCount++
			if len(result.WarningLines) < 10 { // 最多保存 10 行
				result.WarningLines = append(result.WarningLines, fmt.Sprintf("L%d: %s", lineNum, line))
			}
		}
	}

	result.EndTime = time.Now()
	result.TotalLines = lineNum

	return result
}

// AnalyzeSystemLogs 分析系统日志（journalctl）
func (a *LogAnalyzer) AnalyzeSystemLogs(ctx context.Context, logs string, service string) *SystemLogAnalysis {
	analysis := &SystemLogAnalysis{
		Service:      service,
		ErrorCount:   0,
		WarningCount: 0,
		CriticalLogs: make([]string, 0),
		Recommendations: make([]string, 0),
	}

	scanner := bufio.NewScanner(strings.NewReader(logs))

	for scanner.Scan() {
		line := scanner.Text()

		// 检测严重错误
		if a.isCritical(line) {
			if len(analysis.CriticalLogs) < 10 {
				analysis.CriticalLogs = append(analysis.CriticalLogs, line)
			}
		}

		// 计数
		if a.containsError(line) {
			analysis.ErrorCount++
		}
		if a.containsWarning(line) {
			analysis.WarningCount++
		}

		// 特定服务的分析
		analysis.Recommendations = append(analysis.Recommendations, a.analyzeServiceSpecific(service, line)...)
	}

	return analysis
}

// AnalyzeCephLogs 分析 Ceph 日志
func (a *LogAnalyzer) AnalyzeCephLogs(ctx context.Context, logs string) *CephLogAnalysis {
	analysis := &CephLogAnalysis{
		HealthIssues:   make([]string, 0),
		OSDIssues:      make([]string, 0),
		MonitorIssues:  make([]string, 0),
		PGIssues:       make([]string, 0),
		Recommendations: make([]string, 0),
	}

	scanner := bufio.NewScanner(strings.NewReader(logs))

	for scanner.Scan() {
		line := scanner.Text()

		// Ceph 健康状态
		if strings.Contains(line, "HEALTH_WARN") || strings.Contains(line, "HEALTH_ERR") {
			analysis.HealthIssues = append(analysis.HealthIssues, line)
		}

		// OSD 问题
		if strings.Contains(line, "osd") && (strings.Contains(line, "down") || strings.Contains(line, "out")) {
			analysis.OSDIssues = append(analysis.OSDIssues, line)
		}

		// Monitor 问题
		if strings.Contains(line, "mon") && strings.Contains(line, "not in quorum") {
			analysis.MonitorIssues = append(analysis.MonitorIssues, line)
		}

		// PG 问题
		if strings.Contains(line, "pg") && (strings.Contains(line, "degraded") || strings.Contains(line, "undersized")) {
			analysis.PGIssues = append(analysis.PGIssues, line)
		}
	}

	// 生成建议
	if len(analysis.OSDIssues) > 0 {
		analysis.Recommendations = append(analysis.Recommendations, "检查 OSD 状态: ceph osd tree")
	}
	if len(analysis.MonitorIssues) > 0 {
		analysis.Recommendations = append(analysis.Recommendations, "检查 Monitor 状态: ceph mon stat")
	}
	if len(analysis.PGIssues) > 0 {
		analysis.Recommendations = append(analysis.Recommendations, "检查 PG 状态: ceph pg dump")
	}

	return analysis
}

// containsError 检查是否包含错误
func (a *LogAnalyzer) containsError(line string) bool {
	for _, pattern := range a.errorPatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// containsWarning 检查是否包含警告
func (a *LogAnalyzer) containsWarning(line string) bool {
	for _, pattern := range a.warningPatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// isCritical 检查是否为严重错误
func (a *LogAnalyzer) isCritical(line string) bool {
	criticalKeywords := []string{"fatal", "panic", "out of memory", "kernel", "segfault"}
	lineLower := strings.ToLower(line)
	for _, keyword := range criticalKeywords {
		if strings.Contains(lineLower, keyword) {
			return true
		}
	}
	return false
}

// extractPattern 提取错误模式
func (a *LogAnalyzer) extractPattern(line string) string {
	// 简化的模式提取
	if strings.Contains(strings.ToLower(line), "out of memory") {
		return "OOM"
	}
	if strings.Contains(strings.ToLower(line), "connection refused") {
		return "Connection Refused"
	}
	if strings.Contains(strings.ToLower(line), "timeout") {
		return "Timeout"
	}
	if strings.Contains(strings.ToLower(line), "disk") {
		return "Disk Error"
	}
	return "General Error"
}

// analyzeServiceSpecific 分析特定服务的日志
func (a *LogAnalyzer) analyzeServiceSpecific(service, line string) []string {
	var recommendations []string

	switch service {
	case "kubelet":
		if strings.Contains(line, "failed to get node") {
			recommendations = append(recommendations, "检查节点网络连接")
		}
		if strings.Contains(line, "image pull") {
			recommendations = append(recommendations, "检查镜像仓库连接和凭证")
		}
	case "docker", "containerd":
		if strings.Contains(line, "unable to remove") {
			recommendations = append(recommendations, "可能需要清理容器存储")
		}
	}

	return recommendations
}

// LogAnalysisResult 日志分析结果
type LogAnalysisResult struct {
	TotalLines   int
	ErrorCount   int
	WarningCount int
	ErrorLines   []string
	WarningLines []string
	Patterns     map[string]int // 错误模式统计
	StartTime    time.Time
	EndTime      time.Time
}

// SystemLogAnalysis 系统日志分析
type SystemLogAnalysis struct {
	Service         string
	ErrorCount      int
	WarningCount    int
	CriticalLogs    []string
	Recommendations []string
}

// CephLogAnalysis Ceph 日志分析
type CephLogAnalysis struct {
	HealthIssues    []string
	OSDIssues       []string
	MonitorIssues   []string
	PGIssues        []string
	Recommendations []string
}

// GetSummary 获取分析摘要
func (r *LogAnalysisResult) GetSummary() string {
	duration := r.EndTime.Sub(r.StartTime)
	return fmt.Sprintf("分析了 %d 行日志（耗时 %v），发现 %d 个错误，%d 个警告",
		r.TotalLines, duration, r.ErrorCount, r.WarningCount)
}

// GetTopPatterns 获取最常见的错误模式
func (r *LogAnalysisResult) GetTopPatterns(n int) []string {
	// 简单实现：返回所有模式
	patterns := make([]string, 0, len(r.Patterns))
	for pattern, count := range r.Patterns {
		patterns = append(patterns, fmt.Sprintf("%s: %d 次", pattern, count))
	}
	return patterns
}
