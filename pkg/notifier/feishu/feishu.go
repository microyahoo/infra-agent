package feishu

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/microyahoo/infra-agent/pkg/common"
)

// FeishuNotifier 飞书通知器
type FeishuNotifier struct {
	WebhookURL string
	Secret     string // 签名密钥
}

// NewFeishuNotifier 创建飞书通知器
func NewFeishuNotifier(webhookURL, secret string) *FeishuNotifier {
	return &FeishuNotifier{
		WebhookURL: webhookURL,
		Secret:     secret,
	}
}

// GetName 返回通知器名称
func (f *FeishuNotifier) GetName() string {
	return "Feishu"
}

// SendMessage 发送消息
func (f *FeishuNotifier) SendMessage(title string, content string) error {
	msg := f.buildRichTextMessage(title, content)
	return f.send(msg)
}

// SendReport 发送巡检报告
func (f *FeishuNotifier) SendReport(report *common.PatrolReport) error {
	card := f.buildReportCard(report)
	return f.send(card)
}

// SendDiagnosisResult 发送诊断结果
func (f *FeishuNotifier) SendDiagnosisResult(result *common.Failure, diagnosisDetails string) error {
	card := f.buildDiagnosisCard(result, diagnosisDetails)
	return f.send(card)
}

// FeishuMessage 飞书消息结构
type FeishuMessage struct {
	Timestamp string                 `json:"timestamp,omitempty"`
	Sign      string                 `json:"sign,omitempty"`
	MsgType   string                 `json:"msg_type"`
	Content   map[string]interface{} `json:"content"`
}

// buildRichTextMessage 构建富文本消息
func (f *FeishuNotifier) buildRichTextMessage(title, content string) FeishuMessage {
	msg := FeishuMessage{
		MsgType: "text",
		Content: map[string]interface{}{
			"text": fmt.Sprintf("【%s】\n%s", title, content),
		},
	}

	f.addSignature(&msg)
	return msg
}

// buildReportCard 构建巡检报告卡片
func (f *FeishuNotifier) buildReportCard(report *common.PatrolReport) FeishuMessage {
	// 确定状态颜色
	var color string
	switch report.Status {
	case "OK":
		color = "green"
	case "Warning":
		color = "orange"
	default:
		color = "red"
	}

	// 构建卡片元素
	elements := []map[string]interface{}{
		{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**📊 巡检摘要**\n总问题数: %d | 严重: %d | 高: %d | 中: %d | 低: %d",
					report.Summary.TotalIssues,
					report.Summary.CriticalIssues,
					report.Summary.HighIssues,
					report.Summary.MediumIssues,
					report.Summary.LowIssues),
			},
		},
		{
			"tag": "hr",
		},
		{
			"tag": "div",
			"fields": []map[string]interface{}{
				{
					"is_short": true,
					"text": map[string]interface{}{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**开始时间**\n%s", report.StartTime.Format("2006-01-02 15:04:05")),
					},
				},
				{
					"is_short": true,
					"text": map[string]interface{}{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**耗时**\n%s", report.Duration),
					},
				},
			},
		},
	}

	// 添加问题详情
	if len(report.Findings) > 0 {
		elements = append(elements, map[string]interface{}{
			"tag": "hr",
		})

		findingsText := "**🔍 发现的问题**\n\n"
		maxFindings := 5
		if len(report.Findings) < maxFindings {
			maxFindings = len(report.Findings)
		}

		for i := 0; i < maxFindings; i++ {
			finding := report.Findings[i]
			severityEmoji := getSeverityEmoji(finding.Severity)
			findingsText += fmt.Sprintf("%s **%s/%s** (%s)\n",
				severityEmoji, finding.Namespace, finding.Name, finding.Kind)

			for _, err := range finding.Error {
				findingsText += fmt.Sprintf("  • %s\n", err.Text)
			}

			if finding.Solution != "" {
				findingsText += fmt.Sprintf("  💡 解决方案: %s\n", truncate(finding.Solution, 100))
			}
			findingsText += "\n"
		}

		if len(report.Findings) > maxFindings {
			findingsText += fmt.Sprintf("... 还有 %d 个问题\n", len(report.Findings)-maxFindings)
		}

		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": findingsText,
			},
		})
	}

	msg := FeishuMessage{
		MsgType: "interactive",
		Content: map[string]interface{}{
			"config": map[string]interface{}{
				"wide_screen_mode": true,
			},
			"header": map[string]interface{}{
				"template": color,
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": fmt.Sprintf("🔍 基础设施巡检报告 - %s", report.Status),
				},
			},
			"elements": elements,
		},
	}

	f.addSignature(&msg)
	return msg
}

// buildDiagnosisCard 构建诊断结果卡片
func (f *FeishuNotifier) buildDiagnosisCard(result *common.Failure, diagnosisDetails string) FeishuMessage {
	// 确定颜色
	var color string
	switch result.Severity {
	case "critical":
		color = "red"
	case "high":
		color = "orange"
	case "medium":
		color = "yellow"
	default:
		color = "blue"
	}

	severityEmoji := getSeverityEmoji(result.Severity)

	elements := []map[string]interface{}{
		{
			"tag": "div",
			"text": map[string]interface{}{
				"tag": "lark_md",
				"content": fmt.Sprintf("**资源:** %s/%s (%s)\n**严重程度:** %s %s",
					result.Namespace, result.Name, result.Kind,
					severityEmoji, result.Severity),
			},
		},
		{
			"tag": "hr",
		},
		{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**🔍 问题描述**\n%s", formatErrors(result.Error)),
			},
		},
	}

	// 添加 AI 分析
	if result.Explain != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "hr",
		}, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**🤖 AI 分析**\n%s", result.Explain),
			},
		})
	}

	// 添加诊断详情
	if diagnosisDetails != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "hr",
		}, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**📋 诊断详情**\n%s", truncate(diagnosisDetails, 800)),
			},
		})
	}

	// 添加解决方案
	if result.Solution != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "hr",
		}, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**💡 解决方案**\n%s", result.Solution),
			},
		})
	}

	msg := FeishuMessage{
		MsgType: "interactive",
		Content: map[string]interface{}{
			"config": map[string]interface{}{
				"wide_screen_mode": true,
			},
			"header": map[string]interface{}{
				"template": color,
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": "🚨 自动诊断报告",
				},
			},
			"elements": elements,
		},
	}

	f.addSignature(&msg)
	return msg
}

// addSignature 添加签名
func (f *FeishuNotifier) addSignature(msg *FeishuMessage) {
	if f.Secret == "" {
		return
	}

	timestamp := time.Now().Unix()
	msg.Timestamp = strconv.FormatInt(timestamp, 10)

	// 计算签名
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, f.Secret)
	h := hmac.New(sha256.New, []byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))
	msg.Sign = signature
}

// send 发送消息到飞书
func (f *FeishuNotifier) send(msg FeishuMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	resp, err := http.Post(f.WebhookURL, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu returned status %d", resp.StatusCode)
	}

	// 检查响应
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Code != 0 {
		return fmt.Errorf("feishu error: %s (code: %d)", result.Msg, result.Code)
	}

	return nil
}

// 辅助函数

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

func formatErrors(errors []common.Error) string {
	var result string
	for _, err := range errors {
		result += fmt.Sprintf("• %s\n", err.Text)
	}
	return result
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
