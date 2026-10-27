package feishu

import (
	"strings"
	"testing"
	"time"
)

func TestExtractText(t *testing.T) {
	cases := map[string]string{
		`{"text":"分析一下集群为啥warn"}`: "分析一下集群为啥warn",
		`not json`:    "not json",
		`{"text":""}`: "",
	}
	for in, want := range cases {
		if got := extractText(in); got != want {
			t.Errorf("extractText(%q) = %q, want %q", in, got, want)
		}
	}
}

// 飞书 @ 机器人后，文本里会留下 @_user_1 这样的占位符，必须去掉。
func TestMentionsAreStripped(t *testing.T) {
	text := extractText(`{"text":"@_user_1 分析一下集群为啥warn"}`)
	text = strings.ReplaceAll(text, "@_user_1", "")
	if got := strings.TrimSpace(text); got != "分析一下集群为啥warn" {
		t.Errorf("got %q", got)
	}
}

// 飞书会重投事件，同一条消息只能处理一次。
func TestMarkSeenDeduplicates(t *testing.T) {
	b := &Bot{seen: map[string]time.Time{}}
	if !b.markSeen("m1") {
		t.Fatal("first delivery should be processed")
	}
	if b.markSeen("m1") {
		t.Fatal("redelivery must be ignored")
	}
	if !b.markSeen("m2") {
		t.Fatal("different message should be processed")
	}
}

// 群绑定优先：同一个群固定查同一套集群。
func TestClusterForChat(t *testing.T) {
	b := &Bot{cfg: Config{
		Clusters: map[string]ClusterConfig{
			"prod": {Name: "prod", Host: "10.0.0.1"},
			"test": {Name: "test", Host: "10.0.0.2"},
		},
		ChatClusters: map[string]string{"chat-1": "prod"},
	}}
	if got := b.clusterForChat("chat-1", "随便问"); got != "prod" {
		t.Errorf("bound chat = %q, want prod", got)
	}
	if got := b.clusterForChat("chat-2", "test:为啥慢"); got != "test" {
		t.Errorf("prefix form = %q, want test", got)
	}
	if got := b.clusterForChat("chat-2", "为啥慢"); got != "" {
		t.Errorf("default = %q, want empty", got)
	}
}

func TestStripClusterPrefix(t *testing.T) {
	cases := []struct{ in, cluster, want string }{
		{"prod:为啥慢", "prod", "为啥慢"},
		{"为啥慢", "prod", "为啥慢"},
		{"http://x", "prod", "http://x"},
		{"  prod: 为啥慢 ", "prod", "为啥慢"},
	}
	for _, c := range cases {
		if got := stripClusterPrefix(c.in, c.cluster); got != c.want {
			t.Errorf("stripClusterPrefix(%q,%q) = %q, want %q", c.in, c.cluster, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("字", 200000)
	out := clip(long, 100)
	if len([]rune(out)) > 130 {
		t.Errorf("clip did not shorten: %d runes", len([]rune(out)))
	}
	if !strings.Contains(out, "已截断") {
		t.Error("should state truncation")
	}
	if got := clip("短", 100); got != "短" {
		t.Errorf("got %q", got)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error without app credentials")
	}
	if _, err := New(Config{AppID: "a", AppSecret: "b"}); err == nil {
		t.Fatal("expected error without cluster")
	}
}

func TestNewDefaultsIdleTimeout(t *testing.T) {
	b, err := New(Config{
		AppID: "a", AppSecret: "b",
		Cluster: ClusterConfig{Name: "c", Host: "10.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.cfg.IdleTimeout != 30*time.Minute {
		t.Errorf("idle = %v", b.cfg.IdleTimeout)
	}
}
