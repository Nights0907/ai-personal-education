package idgen

import (
	"sync"
	"testing"
)

func TestConcurrentIssuanceAndStableMessageFamily(t *testing.T) {
	seen := sync.Map{}
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 500; k++ {
				id := New("t")
				if !Is("t", id) {
					t.Error("invalid UUIDv7", id)
				}
				if _, old := seen.LoadOrStore(id, true); old {
					t.Error("duplicate ID", id)
				}
				u, _ := Message(id, "user")
				a, _ := Message(id, "assistant")
				again, _ := Message(id, "user")
				if u == a || u != again || !Is("mu", u) || !Is("ma", a) {
					t.Error("unstable message family")
				}
			}
		}()
	}
	wg.Wait()
	previous := ""
	for n := 0; n < 1000; n++ {
		next := New("c")
		if previous != "" && next <= previous {
			t.Fatal("same-process IDs are not increasing")
		}
		previous = next
	}
	if _, ok := Message("legacy-turn", "user"); ok {
		t.Fatal("legacy ID was reinterpreted")
	}
}
