package memory

import (
	"context"
	"errors"
	"unicode/utf8"
)

type NavigationRef struct {
	TopicID  string `json:"topic_id" yaml:"topic_id"`
	L2Path   string `json:"l2_path" yaml:"l2_path"`
	Revision int64  `json:"revision" yaml:"revision"`
}
type TeachingNavigation struct {
	LearningFocus        []string        `json:"learning_focus" yaml:"learning_focus"`
	ObservedDifficulties []string        `json:"observed_difficulties" yaml:"observed_difficulties"`
	SupportNeeded        []string        `json:"support_needed" yaml:"support_needed"`
	NextTeachingActions  []string        `json:"next_teaching_actions" yaml:"next_teaching_actions"`
	EvidenceRefs         []NavigationRef `json:"evidence_refs" yaml:"evidence_refs"`
	SourceRevision       int64           `json:"source_revision" yaml:"source_revision"`
	Stale                bool            `json:"stale" yaml:"stale"`
}
type Navigator interface {
	GenerateNavigation(context.Context, Document[SummaryContent]) (TeachingNavigation, error)
}

func ValidateNavigation(n TeachingNavigation, d Document[SummaryContent]) error {
	if n.SourceRevision <= 0 || n.SourceRevision > d.Revision || (!n.Stale && n.SourceRevision != d.Revision) {
		return errors.New("navigation revision mismatch")
	}
	for _, list := range [][]string{n.LearningFocus, n.ObservedDifficulties, n.SupportNeeded, n.NextTeachingActions} {
		if len(list) > 3 {
			return errors.New("navigation too many items")
		}
		for _, v := range list {
			if utf8.RuneCountInString(v) > 120 {
				return errors.New("navigation item too long")
			}
		}
	}
	if len(n.LearningFocus) == 0 || len(n.EvidenceRefs) == 0 || len(n.EvidenceRefs) > 3 {
		return errors.New("navigation requires bounded references")
	}
	path, _ := MonthlyPaths(d.StudentID, d.Month)
	for _, ref := range n.EvidenceRefs {
		found := false
		for _, a := range d.Content.KnowledgeAreas {
			for _, t := range a.Topics {
				if CanonicalTopicID(a.Area, t.Topic, t.TopicID) == ref.TopicID {
					found = true
				}
			}
		}
		if !found || ref.L2Path != path.Summary || ref.Revision != n.SourceRevision {
			return errors.New("navigation reference not bound to L2")
		}
	}
	return nil
}
