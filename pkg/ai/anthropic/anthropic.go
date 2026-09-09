package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AnthropicProvider Anthropic (Claude) AI 提供商
type AnthropicProvider struct {
	APIKey    string
	Model     string
	BaseURL   string
	MaxTokens int
	Client    *http.Client
}

// NewAnthropicProvider 创建 Anthropic 提供商
func NewAnthropicProvider(apiKey, model, baseURL string) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if model == "" {
		model = "claude-3-sonnet-20240229"
	}

	return &AnthropicProvider{
		APIKey:    apiKey,
		Model:     model,
		BaseURL:   baseURL,
		MaxTokens: 2000,
		Client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// GetName 返回提供商名称
func (a *AnthropicProvider) GetName() string {
	return "Anthropic"
}

// Analyze 分析问题并返回解释和解决方案
func (a *AnthropicProvider) Analyze(problem string) (explanation string, solution string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	systemPrompt := `你是一个 Kubernetes 和基础设施专家。你的任务是分析问题、找出根本原因，并提供详细的解决方案。

请按以下格式回复：

## 问题分析
[详细的根因分析]

## 解决方案
1. [步骤1]
2. [步骤2]
3. [步骤3]

## 预防措施
[如何避免类似问题]
`

	response, err := a.chat(ctx, systemPrompt, problem)
	if err != nil {
		return "", "", err
	}

	// 使用相同的解析逻辑
	explanation, solution = parseResponse(response)

	return explanation, solution, nil
}

// chat 调用 Anthropic Messages API
func (a *AnthropicProvider) chat(ctx context.Context, system, userMessage string) (string, error) {
	requestBody := map[string]interface{}{
		"model":      a.Model,
		"max_tokens": a.MaxTokens,
		"system":     system,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": userMessage,
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL+"/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("Anthropic error: %s", result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("no response from Anthropic")
	}

	return result.Content[0].Text, nil
}

// parseResponse 复用 OpenAI 的解析逻辑
func parseResponse(response string) (explanation, solution string) {
	lines := splitLines(response)
	var currentSection string
	var explanationLines, solutionLines []string

	for _, line := range lines {
		if contains(line, "## 问题分析") || contains(line, "## 根因分析") || contains(line, "Analysis") {
			currentSection = "explanation"
			continue
		} else if contains(line, "## 解决方案") || contains(line, "Solution") {
			currentSection = "solution"
			continue
		} else if contains(line, "## 预防措施") || contains(line, "Prevention") {
			currentSection = "solution"
			continue
		}

		if currentSection == "explanation" && line != "" {
			explanationLines = append(explanationLines, line)
		} else if currentSection == "solution" && line != "" {
			solutionLines = append(solutionLines, line)
		}
	}

	if len(explanationLines) == 0 && len(solutionLines) == 0 {
		return response, response
	}

	explanation = joinLines(explanationLines)
	solution = joinLines(solutionLines)

	if explanation == "" {
		explanation = response
	}
	if solution == "" {
		solution = response
	}

	return explanation, solution
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

func joinLines(lines []string) string {
	var result string
	for i, line := range lines {
		result += line
		if i < len(lines)-1 {
			result += "\n"
		}
	}
	return result
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
