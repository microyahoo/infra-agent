package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider OpenAI AI 提供商
type OpenAIProvider struct {
	APIKey      string
	Model       string
	BaseURL     string
	Temperature float64
	MaxTokens   int
	Client      *http.Client
}

// NewOpenAIProvider 创建 OpenAI 提供商
func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4"
	}

	return &OpenAIProvider{
		APIKey:      apiKey,
		Model:       model,
		BaseURL:     baseURL,
		Temperature: 0.7,
		MaxTokens:   2000,
		Client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// GetName 返回提供商名称
func (o *OpenAIProvider) GetName() string {
	return "OpenAI"
}

// Analyze 分析问题并返回解释和解决方案
func (o *OpenAIProvider) Analyze(problem string) (explanation string, solution string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 构建提示词
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

	messages := []map[string]string{
		{
			"role":    "system",
			"content": systemPrompt,
		},
		{
			"role":    "user",
			"content": problem,
		},
	}

	response, err := o.chat(ctx, messages)
	if err != nil {
		return "", "", err
	}

	// 解析响应，分离解释和解决方案
	explanation, solution = parseResponse(response)

	return explanation, solution, nil
}

// chat 调用 OpenAI Chat API
func (o *OpenAIProvider) chat(ctx context.Context, messages []map[string]string) (string, error) {
	requestBody := map[string]interface{}{
		"model":       o.Model,
		"messages":    messages,
		"temperature": o.Temperature,
		"max_tokens":  o.MaxTokens,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.APIKey)

	resp, err := o.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("OpenAI error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return result.Choices[0].Message.Content, nil
}

// parseResponse 解析 AI 响应，分离解释和解决方案
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

	// 如果没有明确分段，将整个响应作为解释
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
