package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/skills"
)

// readSkillToolName 是内置 agent 专用的工具：按需展开 SKILL.md 的细节文档。
// 它不在 tools.Registry 里，因此不会通过 MCP 暴露给外部 agent
// ——外部 agent 直接读磁盘上的 references/*.md。
const readSkillToolName = "read_skill"

func readSkillSpec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        readSkillToolName,
		Description: "读取 Ceph 诊断的细节文档。当需要更具体的排查步骤时调用。可选：" + strings.Join(skills.ReferenceNames(), "、"),
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"文档名，例如 osd"}},"required":["name"]}`),
	}
}

func skillContent(args map[string]any) string {
	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return `{"error":"缺少 name 参数"}`
	}
	content, err := skills.ReadReference(name)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return content
}
