package agent

import (
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
)

func TestSystemPromptEmbedsSkillAndClusterFacts(t *testing.T) {
	p := SystemPrompt(snapshot.Snapshot{
		ClusterID: "fsid-x",
		Health:    "HEALTH_OK",
		Summary:   "mon 3，OSD 1",
	})
	if !strings.Contains(p, "name: ceph-doctor") {
		t.Error("SKILL.md frontmatter missing from prompt")
	}
	if !strings.Contains(p, "fsid-x") {
		t.Error("dynamic cluster facts missing from prompt")
	}
}
