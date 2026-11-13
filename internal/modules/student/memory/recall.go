package memory

import (
	"context"
	"sort"
	"time"
)

// RecallItem is one knowledge point's current memory state, computed at read
// time. It is the unit the recall engine ranks and the unit every downstream
// consumer (review reminder, in-conversation adaptation, profile report,
// personalized selection) acts on.
type RecallItem struct {
	Assisted       bool     `json:"assisted"`
	Incorrect      bool     `json:"incorrect"`
	EvidenceStatus string   `json:"evidence_status"`
	TopicID        string   `json:"topic_id,omitempty"`
	BestMastery    string   `json:"best_mastery,omitempty"`
	TotalErrors    int      `json:"total_errors"`
	Area           string   `json:"area"`
	Topic          string   `json:"topic"`
	Mastery        string   `json:"mastery"`
	Ability        string   `json:"ability"`
	ErrorCount     int      `json:"error_count"`
	LastSeen       string   `json:"last_seen"`
	Retrievability float64  `json:"retrievability"`
	Due            bool     `json:"due"`
	QuestionIDs    []string `json:"question_ids"`
}

// RecallPlan is the engine output: every knowledge point the student has
// touched, ranked by forgetting priority (lowest retrievability first), with
// due items flagged for review.
type RecallPlan struct {
	StudentID string       `json:"student_id"`
	Items     []RecallItem `json:"items"`
	DueCount  int          `json:"due_count"`
	DerivedAt string       `json:"derived_at"`
}

// RecallPlan scans the student's L2 profiles across months and returns the
// merged, forgetting-ranked memory state. It is the single engine consumed by
// all downstream capabilities; retrieval of an individual month's L2 for
// evidence still goes through read_student_memory.
//
// It never mutates any document: retrievability and due are derived from the
// stored anchors (last_seen, stability, difficulty) relative to now.
func (s *Service) RecallPlan(ctx context.Context, student string, now time.Time) (RecallPlan, error) {
	plan := RecallPlan{StudentID: student, DerivedAt: now.UTC().Format(time.RFC3339)}
	indexes, err := s.Documents.Indexes(ctx, student, 120)
	if err != nil {
		return plan, err
	}
	// Merge across months by (area, topic): keep the most recent last_seen so a
	// topic that appeared last month and again this month is judged by its latest
	// evidence, while error_count and mastery reflect the freshest month.
	byKey := map[string]*RecallItem{}
	for _, idx := range indexes {
		summary, err := loadSummary(ctx, s.Documents, student, idx.Month)
		if err != nil {
			return plan, err
		}
		for _, a := range summary.Content.KnowledgeAreas {
			for _, tp := range a.Topics {
				stability, difficulty := tp.Stability, tp.Difficulty
				if tp.StateVersion == 0 && stability == 0 {
					stability = DefaultStability
				}
				if tp.StateVersion == 0 && difficulty == 0 {
					difficulty = DefaultDifficulty
				}
				var elapsed time.Duration
				if ts, err := time.Parse(time.RFC3339, tp.LastSeen); err == nil {
					elapsed = now.Sub(ts)
				}
				r := Retrievability(stability, difficulty, elapsed)
				evidenceStatus := "observed"
				if tp.StateVersion < 2 {
					evidenceStatus = "legacy"
				}
				item := RecallItem{Assisted: tp.Assisted, Incorrect: tp.Incorrect, EvidenceStatus: evidenceStatus,
					Area:    a.Area,
					TopicID: CanonicalTopicID(a.Area, tp.Topic, tp.TopicID), BestMastery: tp.BestMastery, TotalErrors: tp.TotalErrors,
					Topic:          tp.Topic,
					Mastery:        tp.Mastery,
					Ability:        tp.Ability,
					ErrorCount:     tp.ErrorCount,
					LastSeen:       tp.LastSeen,
					Retrievability: r,
					Due:            IsDue(r),
					QuestionIDs:    tp.QuestionIDs,
				}
				key := a.Area + "/" + CanonicalTopicID(a.Area, tp.Topic, tp.TopicID)
				if existing, ok := byKey[key]; ok {
					// Prefer the fresher evidence; a later month's adjudicated
					// mastery supersedes an earlier one.
					if item.LastSeen >= existing.LastSeen {
						item.QuestionIDs = mergeSorted(existing.QuestionIDs, item.QuestionIDs)
						byKey[key] = &item
					} else {
						existing.QuestionIDs = mergeSorted(existing.QuestionIDs, item.QuestionIDs)
					}
					continue
				}
				byKey[key] = &item
			}
		}
	}
	for _, it := range byKey {
		plan.Items = append(plan.Items, *it)
	}
	// Forgetting priority: due first, then lowest retrievability (most forgotten).
	sort.SliceStable(plan.Items, func(i, j int) bool {
		if plan.Items[i].Due != plan.Items[j].Due {
			return plan.Items[i].Due
		}
		if plan.Items[i].Retrievability != plan.Items[j].Retrievability {
			return plan.Items[i].Retrievability < plan.Items[j].Retrievability
		}
		return plan.Items[i].Area+"/"+plan.Items[i].Topic < plan.Items[j].Area+"/"+plan.Items[j].Topic
	})
	for _, it := range plan.Items {
		if it.Due {
			plan.DueCount++
		}
	}
	return plan, nil
}

type SummaryDocuments interface {
	LoadSummary(context.Context, string, string) (Document[SummaryContent], error)
}

func loadSummary(ctx context.Context, docs Documents, student, month string) (Document[SummaryContent], error) {
	if reader, ok := docs.(SummaryDocuments); ok {
		return reader.LoadSummary(ctx, student, month)
	}
	m, e := docs.Load(ctx, student, month)
	return m.Summary, e
}
