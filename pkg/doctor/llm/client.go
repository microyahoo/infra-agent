package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://api.openai.com/v1"
	defaultModel      = "gpt-4o"
	defaultTimeout    = 120 * time.Second
	defaultMaxRetries = 2
	defaultRetryDelay = 2 * time.Second

	// maxErrorBodyBytes 限制错误响应正文长度，避免日志与错误信息被撑爆
	maxErrorBodyBytes = 2048
)

// Config 是客户端配置。
type Config struct {
	BaseURL     string        // OpenAI 兼容端点根地址，例如 https://api.openai.com/v1 或中转地址
	APIKey      string        // 密钥，仅在 Authorization 头中使用，绝不记录
	Model       string        // 模型名
	Temperature float64       // 采样温度，0 表示使用服务端默认值
	MaxTokens   int           // 最大输出 token，0 表示使用服务端默认值
	Timeout     time.Duration // 单次请求超时
	MaxRetries  int           // 429/5xx 的重试次数，<0 表示使用默认值（2），0 表示不重试
}

// Client 是 OpenAI 兼容的 /chat/completions 客户端。
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	maxRetries  int

	// retryDelay 是重试的基础退避时间，测试中可置 0
	retryDelay time.Duration
	// httpClient 不导出，避免测试修改 retryDelay 时触发 go vet 的 copylocks 检查
	httpClient *http.Client
}

// 编译期确认 Client 实现 ChatModel。
var _ ChatModel = (*Client)(nil)

// NewClient 按配置创建客户端并填充默认值。
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	// MaxRetries 以负数表示“未配置”，零值表示显式关闭重试
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		temperature: cfg.Temperature,
		maxTokens:   cfg.MaxTokens,
		maxRetries:  cfg.MaxRetries,
		retryDelay:  defaultRetryDelay,
		httpClient:  &http.Client{Timeout: cfg.Timeout},
	}
}

// ---- 传输层结构体：严格匹配 OpenAI 线格式 ----

type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireToolFunc `json:"function"`
}

type wireToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string         `json:"role"`
			Content   *string        `json:"content"` // 工具调用时通常为 null
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// buildRequest 将内部消息与工具转换为线格式请求体。
func buildRequest(model string, temperature float64, maxTokens int, msgs []Message, tools []ToolSpec) wireRequest {
	req := wireRequest{
		Model:     model,
		MaxTokens: maxTokens,
	}
	if temperature > 0 {
		t := temperature
		req.Temperature = &t
	}
	for _, m := range msgs {
		wm := wireMessage{Role: string(m.Role), ToolCallID: m.ToolCallID}
		// 空 content 视为省略（指针为 nil 时字段不参与序列化）；
		// tool 消息即使内容为空也显式发送 ""，因为它是 OpenAI 的必填字段
		if m.Content != "" || m.Role == RoleTool {
			c := m.Content
			wm.Content = &c
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: tc.Args,
				},
			})
		}
		req.Messages = append(req.Messages, wm)
	}
	// 工具为空时整个字段省略，某些代理会拒绝 "tools":[]
	for _, t := range tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		req.Tools = append(req.Tools, wireTool{
			Type: "function",
			Function: wireToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}
	return req
}

// Chat 发送一次对话请求并返回模型回复。对 429 与 5xx 按 MaxRetries 重试。
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Reply, error) {
	req := buildRequest(c.model, c.temperature, c.maxTokens, msgs, tools)
	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, fmt.Errorf("llm: 编码请求失败: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避，同时尊重 ctx 取消
			if err := sleepCtx(ctx, c.retryDelay<<uint(attempt-1)); err != nil {
				return Reply{}, err
			}
		}

		reply, retryable, err := c.doOnce(ctx, url, body)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if !retryable {
			return Reply{}, err
		}
		// 上下文已结束则不再重试
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
	}
	return Reply{}, fmt.Errorf("llm: 重试 %d 次后仍失败: %w", c.maxRetries, lastErr)
}

// doOnce 执行单次 HTTP 请求，返回 retryable 表示是否值得重试。
func (c *Client) doOnce(ctx context.Context, url string, body []byte) (Reply, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Reply{}, false, fmt.Errorf("llm: 构造请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// 网络错误（含 ctx 取消）——上下文已结束时不重试
		if ctx.Err() != nil {
			return Reply{}, false, ctx.Err()
		}
		return Reply{}, true, fmt.Errorf("llm: 请求失败: %w", redactKey(err, c.apiKey))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		apiErr := fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, truncate(string(raw)))
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return Reply{}, retryable, apiErr
	}

	var wire wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return Reply{}, false, fmt.Errorf("llm: 解析响应失败: %w", err)
	}
	if wire.Error != nil && wire.Error.Message != "" {
		// 部分代理在 200 中返回错误体
		return Reply{}, false, fmt.Errorf("llm: 服务端返回错误: %s", truncate(wire.Error.Message))
	}
	if len(wire.Choices) == 0 {
		return Reply{}, false, errors.New("llm: 响应中没有 choices")
	}

	msg := wire.Choices[0].Message
	reply := Reply{Usage: Usage{
		PromptTokens:     wire.Usage.PromptTokens,
		CompletionTokens: wire.Usage.CompletionTokens,
	}, FinishReason: FinishReason(wire.Choices[0].FinishReason)}
	if msg.Content != nil {
		reply.Content = *msg.Content
	}
	for _, tc := range msg.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	return reply, false, nil
}

// sleepCtx 在等待 d 的同时尊重 ctx 取消。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// truncate 截断过长文本用于错误信息，按 rune 截断避免截坏 UTF-8。
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorBodyBytes {
		return s
	}
	r := []rune(s)
	n := 0
	for n < len(r) && n < maxErrorBodyBytes {
		n++
	}
	return string(r[:n]) + "...(截断)"
}

// redactKey 兜底移除错误文本中可能出现的密钥，保留原始错误链。
func redactKey(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return fmt.Errorf("%s [已脱敏]", strings.ReplaceAll(err.Error(), key, "***"))
}
