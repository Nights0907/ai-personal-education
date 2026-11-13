package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestIndexToolOnlyProjectsBoundL2(t *testing.T) {
	raw := rawFixture("t1")
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extraction(raw), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ALG:一元二次方程求根"}
	if !reflect.DeepEqual(m.Index.Content.Keywords, want) {
		t.Fatalf("index keywords = %v, want %v", m.Index.Content.Keywords, want)
	}
	if m.Index.Content.TurnCount != 1 || m.Index.Content.Span.First != "2026-10-28" {
		t.Fatal("index metadata not projected")
	}
	tool, err := NewIndexTool(m.Summary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.InvokableRun(context.Background(), `{"l2_path":"/user/memory/s1/2026-10/meta.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	var index Document[IndexContent]
	if err = json.Unmarshal([]byte(result), &index); err != nil {
		t.Fatal(err)
	}
	if index.Revision != m.Summary.Revision || !index.UpdatedAt.Equal(m.Summary.UpdatedAt) || !reflect.DeepEqual(index.Content.Keywords, want) || index.NextPaths[0] != "/user/memory/s1/2026-10/meta.md" {
		t.Fatal("projection metadata or content diverged")
	}
	for _, bad := range []string{`{"l2_path":"/user/memory/s2/2026-10/meta.md"}`, `{"l2_path":"/user/memory/s1/2026-11/meta.md"}`, `{"l2_path":"/user/memory/s1/2026-10/meta.md","keywords":["injected"]}`, `{"l2_path":"/user/memory/s1/2026-10/meta.md"} {}`} {
		if _, err = tool.InvokableRun(context.Background(), bad); err == nil {
			t.Fatalf("unauthorized tool arguments accepted: %s", bad)
		}
	}
	// Raw text and free-form summary are deliberately not index sources.
	changed := m.Summary
	changed.Content.Summary = "unrelated prose"
	a, _ := IndexFromSummary(m.Summary)
	b, _ := IndexFromSummary(changed)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("free-form summary affected deterministic index")
	}
}

func TestLegacyReindexKeepsRawAndRenumbersSeq(t *testing.T) {
	raw := rawFixture("t1")
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extraction(raw), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	m.Index.Content.Keywords = []string{"only-in-old-L1"}
	docs := &fakeDocuments{month: m}
	s := Service{Documents: docs}
	next, err := s.RebuildIndex(context.Background(), "s1", "2026-10", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Index.Content.Keywords, []string{"ALG:一元二次方程求根"}) {
		t.Fatal("legacy L1 did not come exclusively from L2 fields")
	}
	if next.Raw.Content.Turns[0].Seq != 1 {
		t.Fatal("reindex did not renumber seq")
	}
	if next.Raw.Content.Turns[0].User != raw.User {
		t.Fatal("reindex rewrote history")
	}
}
