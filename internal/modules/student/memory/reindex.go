package memory

import (
	"context"
	"sort"
	"time"
)

// RebuildIndex upgrades legacy L2/L3 documents to schema v2. It deterministically
// renumbers L3 seq and re-projects L1 from the L2 knowledge areas. Old summary
// facts are not reinterpreted into mastery; only missing structural fields are
// normalized. All original conversations are preserved verbatim.
func (s *Service) RebuildIndex(ctx context.Context, student, month string, now time.Time) (Month, error) {
	old, err := s.Documents.Load(ctx, student, month)
	if err != nil {
		return Month{}, err
	}
	next := old
	// Renumber L3 seq deterministically by creation time then source id.
	turns := append([]OriginalTurn{}, next.Raw.Content.Turns...)
	sort.SliceStable(turns, func(i, j int) bool {
		if turns[i].CreatedAt.Equal(turns[j].CreatedAt) {
			return SourceID(turns[i].Source) < SourceID(turns[j].Source)
		}
		return turns[i].CreatedAt.Before(turns[j].CreatedAt)
	})
	for i := range turns {
		turns[i].Seq = i + 1
	}
	next.Raw.Content.Turns = turns
	next.Raw.SchemaVersion = SchemaVersion
	next.Summary.SchemaVersion = SchemaVersion
	next.Summary.Revision++
	next.Summary.UpdatedAt = now.UTC()
	next.Raw.Revision = next.Summary.Revision
	next.Raw.UpdatedAt = now.UTC()
	next.Index, err = indexThroughTool(ctx, next.Summary)
	if err != nil {
		return Month{}, err
	}
	if err = s.Documents.Publish(ctx, student, month, old, next); err != nil {
		return Month{}, err
	}
	return next, nil
}
