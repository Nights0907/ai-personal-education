package memory

import (
	"context"
	"testing"
	"time"
)

func TestNavigationRejectsInventedReferences(t *testing.T) {
	raw := rawFixture("navigation")
	d, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extraction(raw), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	topic := d.Summary.Content.KnowledgeAreas[0].Topics[0]
	paths, _ := MonthlyPaths("s1", "2026-10")
	n := TeachingNavigation{LearningFocus: []string{topic.Topic}, SourceRevision: d.Summary.Revision, EvidenceRefs: []NavigationRef{{TopicID: topic.TopicID, L2Path: paths.Summary, Revision: d.Summary.Revision}}}
	if ValidateNavigation(n, d.Summary) != nil {
		t.Fatal("valid navigation rejected")
	}
	n.EvidenceRefs[0].TopicID = "invented"
	if ValidateNavigation(n, d.Summary) == nil {
		t.Fatal("fabricated reference accepted")
	}
}
