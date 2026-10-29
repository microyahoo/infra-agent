// Package skills 内嵌 Ceph 诊断知识。
//
// 同一份文档有两个消费者：
//   - 内置 agent 通过 SystemPrompt 读取 SKILL.md
//   - 外部 agent（Cursor / Dify / Claude）直接读磁盘上的 skills/ceph-doctor/
//
// 因此这里只做读取，不写任何"只对内置 agent 成立"的指令。
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed ceph-doctor
var FS embed.FS

const skillName = "ceph-doctor"

// MustRead 读取技能目录下的文件，供内置 agent 使用。
func MustRead(name string) string {
	b, err := FS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("skills: read %s: %v", name, err))
	}
	return string(b)
}

// ReadReference 按名字读取 references/<name>.md。
func ReadReference(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".md")
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("无效的文档名 %q", name)
	}
	b, err := FS.ReadFile(path.Join(skillName, "references", name+".md"))
	if err != nil {
		return "", fmt.Errorf("没有名为 %q 的文档", name)
	}
	return string(b), nil
}

// ReferenceNames 返回全部细节文档名（不含 .md）。
func ReferenceNames() []string {
	entries, err := fs.ReadDir(FS, path.Join(skillName, "references"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}
