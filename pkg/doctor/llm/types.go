// Package llm 提供与 OpenAI 兼容（含官方与各类代理/中转）的 /chat/completions 客户端，
// 支持工具调用（function calling），供诊断 loop 使用。
package llm

import (
	"context"
	"encoding/json"
)

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是一条对话消息。
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // 仅 assistant 消息使用：模型发起的工具调用
	ToolCallID string     // 仅 tool 消息使用：对应的工具调用 ID
}

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID   string
	Name string
	Args string // 原始 JSON 字符串，由工具自行解析
}

// ToolSpec 是发给模型的工具描述。
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Usage 是 token 用量。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Reply 是模型的一次回复。
type Reply struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// ToolResultMessage 构造一条工具结果消息，callID 必须与模型给出的 ToolCall.ID 一致。
func ToolResultMessage(callID, content string) Message {
	return Message{
		Role:       RoleTool,
		Content:    content,
		ToolCallID: callID,
	}
}

// ChatModel 是诊断 loop 依赖的模型接口，测试可替换为假模型。
type ChatModel interface {
	Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Reply, error)
}
