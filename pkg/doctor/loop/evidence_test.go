package loop

import (
	"testing"
	"time"
)

func TestChainAssignsSequentialIDs(t *testing.T) {
	c := NewChain()
	e1 := c.Add(Evidence{Tool: "ceph_status", Summary: "ok"})
	e2 := c.Add(Evidence{Tool: "osd_tree", Summary: "ok"})
	if e1.ID != "ev-001" {
		t.Errorf("first id = %q", e1.ID)
	}
	if e2.ID != "ev-002" {
		t.Errorf("second id = %q", e2.ID)
	}
}

func TestChainGet(t *testing.T) {
	c := NewChain()
	added := c.Add(Evidence{Tool: "x"})
	got, ok := c.Get(added.ID)
	if !ok {
		t.Fatal("expected to find evidence")
	}
	if got.Tool != "x" {
		t.Errorf("tool = %q", got.Tool)
	}
	if _, ok := c.Get("ev-999"); ok {
		t.Error("should not find missing id")
	}
}

func TestChainAllPreservesOrder(t *testing.T) {
	c := NewChain()
	for _, name := range []string{"a", "b", "c"} {
		c.Add(Evidence{Tool: name})
	}
	all := c.All()
	if len(all) != 3 {
		t.Fatalf("got %d", len(all))
	}
	if all[0].Tool != "a" || all[2].Tool != "c" {
		t.Errorf("order broken: %+v", all)
	}
}

func TestChainValidateRefs(t *testing.T) {
	c := NewChain()
	e1 := c.Add(Evidence{Tool: "a"})
	c.Add(Evidence{Tool: "b"})

	if err := c.ValidateRefs([]string{e1.ID}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := c.ValidateRefs([]string{e1.ID, "ev-999"}); err == nil {
		t.Fatal("expected error for missing ref")
	}
}

func TestChainDetectsNoProgress(t *testing.T) {
	c := NewChain()
	if c.NoProgress() {
		t.Error("empty chain is not progress")
	}
	c.Add(Evidence{Tool: "a"})
	c.MarkRound(1)
	if c.NoProgress() {
		t.Error("just added evidence, should have progress")
	}
	c.MarkRound(2)
	if !c.NoProgress() {
		t.Error("expected no progress after 2 rounds without new evidence")
	}
	c.Add(Evidence{Tool: "b"})
	c.MarkRound(3)
	if c.NoProgress() {
		t.Error("new evidence should reset no-progress")
	}
}

func TestChainDedupsIdenticalCall(t *testing.T) {
	c := NewChain()
	c.Add(Evidence{Tool: "osd_detail", Args: map[string]any{"osd_id": 7}})
	if !c.SeenCall("osd_detail", map[string]any{"osd_id": 7}) {
		t.Error("expected identical call detected")
	}
	if c.SeenCall("osd_detail", map[string]any{"osd_id": 8}) {
		t.Error("different args should not match")
	}
}

func TestChainRecordsDuration(t *testing.T) {
	c := NewChain()
	e := c.Add(Evidence{Tool: "a", Duration: 3 * time.Second})
	if e.Duration != 3*time.Second {
		t.Error("duration not preserved")
	}
}

// 证据链必须能安全地并发读：报告渲染和循环状态检查可能位于不同协程。
func TestChainConcurrentAddIsSafe(t *testing.T) {
	c := NewChain()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			c.Add(Evidence{Tool: "x"})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if c.Len() != 20 {
		t.Errorf("len = %d, want 20", c.Len())
	}
	ids := map[string]bool{}
	for _, e := range c.All() {
		if ids[e.ID] {
			t.Errorf("duplicate id %q", e.ID)
		}
		ids[e.ID] = true
	}
}
