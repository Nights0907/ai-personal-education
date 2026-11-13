package memory

import (
	"context"
	"testing"
	"time"
)

// recallDocs is a minimal Documents fake serving two months of L2 for RecallPlan.
type recallDocs struct {
	months  map[string]Month
	indexes []Document[IndexContent]
}

func (r *recallDocs) Load(_ context.Context, _, month string) (Month, error) {
	if m, ok := r.months[month]; ok {
		return m, nil
	}
	return Month{}, ErrNotFound
}
func (r *recallDocs) Publish(context.Context, string, string, Month, Month) error { return nil }
func (r *recallDocs) Indexes(context.Context, string, int) ([]Document[IndexContent], error) {
	return r.indexes, nil
}

func topicDoc(student, month string, revision int64, area, topic, mastery string, lastSeen time.Time, errorCount int) Month {
	stability := DefaultStability
	if mastery == MasteryMastered {
		stability = 0.8
	}
	difficulty := UpdateDifficulty(errorCount)
	m := Month{}
	m.Summary = Document[SummaryContent]{
		SchemaVersion: SchemaVersion, Layer: "L2", StudentID: student, Month: month, Revision: revision,
		Content: SummaryContent{
			Summary: "x",
			KnowledgeAreas: []KnowledgeArea{{Area: area, Topics: []Topic{{
				Topic: topic, Mastery: mastery, Ability: "AB-COMPUTE", ErrorCount: errorCount,
				LastSeen: lastSeen.Format(time.RFC3339), Stability: stability, Difficulty: difficulty,
			}}}},
		},
	}
	m.Index = Document[IndexContent]{SchemaVersion: SchemaVersion, Layer: "L1", StudentID: student, Month: month, Revision: revision}
	return m
}

func TestRecallPlanMergesRanksAndFlagsDue(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	// Old month: a topic last seen 20 days ago (definitely due).
	oldSeen := now.Add(-20 * 24 * time.Hour)
	// Recent month: two topics, one seen 1 day ago (not due), one 10 days ago (due).
	recentSeen := now.Add(-24 * time.Hour)
	mediumSeen := now.Add(-10 * 24 * time.Hour)

	old := topicDoc("s1", "2026-10", 1, "SEQ", "等差数列求和", MasteryMastered, oldSeen, 0)
	recent := topicDoc("s1", "2026-11", 2, "ALG", "一元二次方程求根", MasteryPartial, recentSeen, 1)
	recent.Summary.Content.KnowledgeAreas = append(recent.Summary.Content.KnowledgeAreas,
		KnowledgeArea{Area: "FUNC", Topics: []Topic{{Topic: "二次函数最值", Mastery: MasteryPartial, Ability: "AB-COMPUTE", ErrorCount: 2, LastSeen: mediumSeen.Format(time.RFC3339), Stability: 0.4, Difficulty: UpdateDifficulty(2)}}})

	docs := &recallDocs{months: map[string]Month{"2026-10": old, "2026-11": recent}, indexes: []Document[IndexContent]{old.Index, recent.Index}}
	s := &Service{Documents: docs}
	plan, err := s.RecallPlan(context.Background(), "s1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 {
		t.Fatalf("expected 3 merged items, got %d: %+v", len(plan.Items), plan.Items)
	}
	if plan.DueCount != 2 {
		t.Fatalf("expected 2 due items (20d and 10d), got %d", plan.DueCount)
	}
	// Ranking is by retrievability, not raw age: the 10-day-old topic has low
	// stability (0.4) and high difficulty (error_count=2), so it decays fastest
	// and must rank first despite being newer than the 20-day-old mastered topic.
	if plan.Items[0].Topic != "二次函数最值" {
		t.Fatalf("fastest-decaying topic not ranked first: %+v", plan.Items[0])
	}
	if !plan.Items[0].Due || !plan.Items[1].Due {
		t.Fatal("both older topics must be due")
	}
	// Last item must be the recent (not due) one.
	if plan.Items[2].Topic != "一元二次方程求根" || plan.Items[2].Due {
		t.Fatalf("recent topic should rank last and not be due: %+v", plan.Items[2])
	}
}

func TestRecallPlanSameTopicAcrossMonthsKeepsFreshest(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	oldSeen := now.Add(-20 * 24 * time.Hour)
	freshSeen := now.Add(-24 * time.Hour)

	old := topicDoc("s1", "2026-10", 1, "ALG", "一元二次方程求根", MasteryPartial, oldSeen, 1)
	recent := topicDoc("s1", "2026-11", 2, "ALG", "一元二次方程求根", MasteryMastered, freshSeen, 0)

	docs := &recallDocs{months: map[string]Month{"2026-10": old, "2026-11": recent}, indexes: []Document[IndexContent]{old.Index, recent.Index}}
	s := &Service{Documents: docs}
	plan, err := s.RecallPlan(context.Background(), "s1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("same topic must merge to one item, got %d", len(plan.Items))
	}
	// The freshest month (mastered, seen 1 day ago) must win.
	if plan.Items[0].Mastery != MasteryMastered || plan.Items[0].Due {
		t.Fatalf("freshest evidence not preferred: %+v", plan.Items[0])
	}
}
