package skills_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/skills"
)

func referenceDocs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range skills.ReferenceNames() {
		doc, err := skills.ReadReference(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, doc)
	}
	return out
}

func TestEveryToolIsDocumented(t *testing.T) {
	reg := tools.RegistryFrom(tools.All(nil, ""))
	doc := skills.MustRead("ceph-doctor/SKILL.md")
	for _, name := range reg.Names() {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("tool %q 没有出现在 SKILL.md 的工具路由里", name)
		}
	}
}

func TestSkillDoesNotMentionUnknownTools(t *testing.T) {
	reg := tools.RegistryFrom(tools.All(nil, ""))
	re := regexp.MustCompile("`([a-z][a-z0-9_]+)`")
	prefixes := []string{
		"ceph_", "osd_", "pg_", "pool_", "host_", "rook_", "read_",
		"run_", "daemon_", "perf_", "slow_ops", "explore",
	}
	docs := append([]string{skills.MustRead("ceph-doctor/SKILL.md")}, referenceDocs(t)...)
	for _, doc := range docs {
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			name := m[1]
			matched := false
			for _, p := range prefixes {
				if strings.HasPrefix(name, p) {
					matched = true
					break
				}
			}
			if matched && !reg.Has(name) {
				t.Errorf("文档提到了不存在的工具 %q", name)
			}
		}
	}
}

func TestReferenceLinksExist(t *testing.T) {
	doc := skills.MustRead("ceph-doctor/SKILL.md")
	re := regexp.MustCompile(`references/([a-z0-9_]+)\.md`)
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		if _, err := skills.ReadReference(m[1]); err != nil {
			t.Errorf("失效的文档链接 %q: %v", m[0], err)
		}
	}
}
