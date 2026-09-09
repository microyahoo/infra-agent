package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/microyahoo/infra-agent/pkg/common"
)

// SlackNotifier Slack 通知器
type SlackNotifier struct {
	WebhookURL string
	Channel    string
	Username   string
	IconEmoji  string
}

// SlackMessage Slack 消息格式
type SlackMessage struct {
	Channel   string            `json:"channel,omitempty"`
	Username  string            `json:"username,omitempty"`
	IconEmoji string            `json:"icon_emoji,omitempty"`
	Text      string            `json:"text,omitempty"`
	Blocks    []SlackBlock      `json:"blocks,omitempty"`
}

// SlackBlock Slack 消息块
type SlackBlock struct {
	Type string                 `json:"type"`
	Text *SlackText             `json:"text,omitempty"`
	Fields []SlackText          `json:"fields,omitempty"`
}

// SlackText Slack 文本
type SlackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewSlackNotifier 创建 Slack 通知器
func NewSlackNotifier(webhookURL, channel, username, iconEmoji string) *SlackNotifier {
	return &SlackNotifier{
		WebhookURL: webhookURL,
		Channel:    channel,
		Username:   username,
		IconEmoji:  iconEmoji,
	}
}

// GetName 返回通知器名称
func (s *SlackNotifier) GetName() string {
	return "Slack"
}

// SendMessage 发送消息
func (s *SlackNotifier) SendMessage(title string, content string) error {
	msg := SlackMessage{
		Channel:   s.Channel,
		Username:  s.Username,
		IconEmoji: s.IconEmoji,
		Blocks: []SlackBlock{
			{
				Type: "header",
				Text: &SlackText{
					Type: "plain_text",
					Text: title,
				},
			},
			{
				Type: "section",
				Text: &SlackText{
					Type: "mrkdwn",
					Text: content,
				},
			},
		},
	}

	return s.send(msg)
}

// SendReport 发送巡检报告
func (s *SlackNotifier) SendReport(report *common.PatrolReport) error {
	// 构建报告消息
	msg := SlackMessage{
		Channel:   s.Channel,
		Username:  s.Username,
		IconEmoji: s.IconEmoji,
		Blocks: []SlackBlock{
			{
				Type: "header",
				Text: &SlackText{
					Type: "plain_text",
					Text: fmt.Sprintf("🔍 巡检报告 - %s", report.Status),
				},
			},
			{
				Type: "section",
				Fields: []SlackText{
					{Type: "mrkdwn", Text: fmt.Sprintf("*开始时间:*\n%s", report.StartTime.Format(time.RFC3339))},
					{Type: "mrkdwn", Text: fmt.Sprintf("*耗时:*\n%s", report.Duration)},
					{Type: "mrkdwn", Text: fmt.Sprintf("*总问题数:*\n%d", report.Summary.TotalIssues)},
					{Type: "mrkdwn", Text: fmt.Sprintf("*严重问题:*\n%d", report.Summary.CriticalIssues)},
				},
			},
		},
	}

	// 添加问题详情
	if len(report.Findings) > 0 {
		var issues string
		for i, finding := range report.Findings {
			if i >= 5 { // 只显示前 5 个问题
				issues += fmt.Sprintf("\n... 还有 %d 个问题", len(report.Findings)-5)
				break
			}
			severityEmoji := getSeverityEmoji(finding.Severity)
			issues += fmt.Sprintf("\n%s *%s/%s* (%s)\n", severityEmoji, finding.Namespace, finding.Name, finding.Kind)
			for _, err := range finding.Error {
				issues += fmt.Sprintf("  • %s\n", err.Text)
			}
		}

		msg.Blocks = append(msg.Blocks, SlackBlock{
			Type: "section",
			Text: &SlackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*发现的问题:*\n%s", issues),
			},
		})
	}

	return s.send(msg)
}

// send 发送消息到 Slack
func (s *SlackNotifier) send(msg SlackMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	resp, err := http.Post(s.WebhookURL, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned status %d", resp.StatusCode)
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
