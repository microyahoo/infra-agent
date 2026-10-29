package evidence

import (
	"testing"
	"time"
)

func TestChainAssignsSequentialIDs(t *testing.T) {
	c := NewChain()
	e1 := c.Add(Evidence{Tool: "ceph_status"})
	e2 := c.Add(Evidence{Tool: "osd_tree"})
	if e1.ID != "ev-001" || e2.ID != "ev-002" {
		t.Errorf("ids = %q/%q", e1.ID, e2.ID)
	}
}

// 带显式 ID 的证据（会话开始时预置的快照证据）必须保留原编号，
// 否则提示词里给出的编号与链里对不上。
func TestChainPreservesExplicitID(t *testing.T) {
	c := NewChain()
	seeded := c.Add(Evidence{ID: "ev-001", Tool: "snapshot:ceph_status"})
	if seeded.ID != "ev-001" {
		t.Errorf("id = %q", seeded.ID)
	}
	real := c.Add(Evidence{Tool: "ceph_status"})
	if real.ID == "ev-001" {
		t.Error("new evidence must not reuse an existing id")
	}
	if c.Len() != 2 {
		t.Errorf("len = %d", c.Len())
	}
}

func TestChainAllPreservesOrder(t *testing.T) {
	c := NewChain()
	for _, name := range []string{"a", "b", "c"} {
		c.Add(Evidence{Tool: name})
	}
	all := c.All()
	if len(all) != 3 || all[0].Tool != "a" || all[2].Tool != "c" {
		t.Errorf("order broken: %+v", all)
	}
}

func TestChainDedupsIdenticalCall(t *testing.T) {
	c := NewChain()
	c.MarkCall("osd_detail", map[string]any{"osd_id": 7})
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
