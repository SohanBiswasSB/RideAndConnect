package graph

import (
	"strconv"
	"testing"
)

func TestPersistedCache_LRUEviction(t *testing.T) {
	c := newPersistedCache(3)
	c.set("a", "qA")
	c.set("b", "qB")
	c.set("c", "qC")
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should be present")
	}
	// "a" is now most-recent. Insert "d" — should evict "b" (LRU).
	c.set("d", "qD")
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.get(k); !ok {
			t.Fatalf("%s should still be present", k)
		}
	}
}

func TestPersistedCache_Bounded(t *testing.T) {
	c := newPersistedCache(64)
	for i := 0; i < 10_000; i++ {
		c.set("h"+strconv.Itoa(i), "q")
	}
	if c.order.Len() != 64 {
		t.Fatalf("len=%d want 64", c.order.Len())
	}
	if len(c.items) != 64 {
		t.Fatalf("items=%d want 64", len(c.items))
	}
}

func TestPersistedCache_UpdateExisting(t *testing.T) {
	c := newPersistedCache(2)
	c.set("a", "qA")
	c.set("b", "qB")
	c.set("a", "qA2") // update existing — must not evict
	if _, ok := c.get("b"); !ok {
		t.Fatal("b should still be present")
	}
	if v, _ := c.get("a"); v != "qA2" {
		t.Fatalf("a=%q want qA2", v)
	}
}
