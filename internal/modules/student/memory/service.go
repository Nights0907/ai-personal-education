package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Navigator Navigator
	Repo      Repository
	Documents Documents
	Extractor Extractor
	Wake      chan struct{}
	// AfterExtract, when set, is invoked once a student's memory has been
	// successfully extracted and committed. It carries the conversation IDs
	// whose turns were just processed and the extraction timestamp, so downstream
	// consumers (e.g. the forgetting-prevention plan) can tell which review
	// conversations the student answered in and compute retrievability from a
	// consistent clock. Failures are logged, not propagated.
	AfterExtract  func(context.Context, string, []string, time.Time)
	AfterEvidence func(context.Context, EvidenceEvent) error
}

// notifyExtract invokes the AfterExtract hook best-effort.
func (s *Service) notifyExtract(ctx context.Context, student string, conversations []string, now time.Time) {
	if s.AfterExtract == nil {
		return
	}
	s.AfterExtract(ctx, student, conversations, now)
}

func (s *Service) Notify() {
	select {
	case s.Wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context, onError func(error)) {
	for ctx.Err() == nil {
		worked, err := s.Step(ctx, time.Now().UTC())
		if err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		delay := 15 * time.Second
		if worked && err == nil {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.Wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Service) Step(ctx context.Context, now time.Time) (bool, error) {
	if events, ok := s.Repo.(EvidenceEvents); ok && s.AfterEvidence != nil {
		event, e := events.PendingEvidence(ctx, now)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return false, e
		}
		if e == nil {
			if e = s.AfterEvidence(ctx, event); e != nil {
				_ = events.RetryEvidence(ctx, event.ID, now.Add(time.Minute))
				return true, e
			}
			return true, events.CompleteEvidence(ctx, event.ID)
		}
	}

	if err := s.Repo.Discover(ctx); err != nil {
		return false, err
	}
	claim, err := s.Repo.Claim(ctx, now)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 150*time.Second)
	err = s.process(work, claim, now)
	cancel()
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if retryErr := s.Repo.Retry(cleanup, claim, now.Add(time.Minute)); retryErr != nil {
			return true, errors.Join(err, retryErr)
		}
	}
	return true, err
}

func (s *Service) process(ctx context.Context, claim Claim, now time.Time) error {
	months := map[string][]OriginalTurn{}
	order := []string{}
	conversations := map[string]bool{}
	for _, t := range claim.Turns {
		if _, ok := months[t.Month]; !ok {
			order = append(order, t.Month)
		}
		months[t.Month] = append(months[t.Month], t.Raw)

	}
	for _, month := range order {
		old, err := s.Documents.Load(ctx, claim.StudentID, month)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if old.Raw.Revision == 0 {
			indexes, e := s.Documents.Indexes(ctx, claim.StudentID, 120)
			if e != nil {
				return e
			}
			for _, idx := range indexes {
				if idx.Month != "" && idx.Month < month {
					prior, e := loadSummary(ctx, s.Documents, claim.StudentID, idx.Month)
					if e != nil {
						return e
					}
					old.Summary.Content.KnowledgeAreas = prior.Content.KnowledgeAreas
					break
				}
			}
		}
		seen := map[string]bool{}
		for _, raw := range old.Raw.Content.Turns {
			seen[SourceID(raw.Source)] = true
		}
		fresh := []OriginalTurn{}
		for _, raw := range months[month] {
			if !seen[SourceID(raw.Source)] {
				fresh = append(fresh, raw)
				seen[SourceID(raw.Source)] = true
			}
		}
		for i := range claim.Turns {
			for _, raw := range old.Raw.Content.Turns {
				if SourceID(claim.Turns[i].Raw.Source) == SourceID(raw.Source) {
					claim.Turns[i].Raw = raw
				}
			}
		}
		if len(fresh) == 0 {
			// Redis committed but SQL acknowledgement failed. The month is
			// already correct; nothing to recompute without calling the model.
			continue
		}
		result, err := s.Extractor.Extract(ctx, old.Summary.Content, questionContext(old, fresh), fresh)
		if err != nil {
			return err
		}
		next, err := BuildMonth(ctx, claim.StudentID, month, old, fresh, result, now)
		if err != nil {
			return err
		}
		for i := range claim.Turns {
			for _, raw := range next.Raw.Content.Turns {
				if SourceID(claim.Turns[i].Raw.Source) == SourceID(raw.Source) {
					claim.Turns[i].Raw = raw
				}
			}
		}

		if s.Navigator != nil {
			nav, navErr := s.Navigator.GenerateNavigation(ctx, next.Summary)
			if navErr == nil {
				nav.SourceRevision = next.Summary.Revision
				nav.Stale = false
				navErr = ValidateNavigation(nav, next.Summary)
			}
			if navErr == nil {
				next.Index.Content.Navigation = &nav
			} else if old.Index.Content.Navigation != nil {
				prior := *old.Index.Content.Navigation
				prior.Stale = true
				if ValidateNavigation(prior, next.Summary) == nil {
					next.Index.Content.Navigation = &prior
				}
			}
		}
		if err = s.Documents.Publish(ctx, claim.StudentID, month, old, next); err != nil {
			return err
		}
	}
	if err := s.Repo.Complete(ctx, claim); err != nil {
		return err
	}
	for _, t := range claim.Turns {
		if t.Raw.Assessment != nil && t.Raw.Assessment.Authored {
			conversations[t.Raw.Source.ConversationID] = true
		}
	}
	convList := make([]string, 0, len(conversations))
	for c := range conversations {
		convList = append(convList, c)
	}
	s.notifyExtract(ctx, claim.StudentID, convList, now)
	return nil
}

// questionContext builds the answer-free background for one extraction batch.
// QuestionMeta is populated from whatever objective metadata is attached to the
// fresh turns (callers inject solve-side knowledge tags); RecentTopics reuses
// the already-adjudicated L2 anchors so the model sees recent error history
// without re-deriving it.
func questionContext(old Month, fresh []OriginalTurn) QuestionContext {
	qc := QuestionContext{QuestionMeta: map[string]QuestionMeta{}, RecentTopics: []RecentTopic{}}
	for _, t := range fresh {
		sid := SourceID(t.Source)
		if t.Source.QuestionID == "" && len(t.KnowledgeTags) == 0 {
			continue
		}
		qc.QuestionMeta[sid] = QuestionMeta{
			QuestionID:    t.Source.QuestionID,
			KnowledgeTags: t.KnowledgeTags,
			Difficulty:    t.Difficulty,
		}
	}
	seen := map[string]bool{}
	for _, a := range old.Summary.Content.KnowledgeAreas {
		for _, tp := range a.Topics {
			key := a.Area + "/" + tp.Topic
			if seen[key] {
				continue
			}
			seen[key] = true
			qc.RecentTopics = append(qc.RecentTopics, RecentTopic{
				TopicID:    CanonicalTopicID(a.Area, tp.Topic, tp.TopicID),
				Area:       a.Area,
				Topic:      tp.Topic,
				ErrorCount: tp.ErrorCount,
				LastSeen:   tp.LastSeen,
				Stability:  tp.Stability,
				Mastery:    tp.Mastery,
			})
		}
	}
	relevant := map[string]bool{}
	for _, raw := range fresh {
		for _, area := range raw.KnowledgeTags {
			relevant[area] = true
		}
	}
	if len(relevant) > 0 {
		filtered := []RecentTopic{}
		for _, topic := range qc.RecentTopics {
			if relevant[topic.Area] {
				filtered = append(filtered, topic)
			}
		}
		qc.RecentTopics = filtered
	}
	sort.SliceStable(qc.RecentTopics, func(i, j int) bool { return qc.RecentTopics[i].LastSeen > qc.RecentTopics[j].LastSeen })
	if len(qc.RecentTopics) > 40 {
		qc.RecentTopics = qc.RecentTopics[:40]
	}
	return qc
}

func BuildMonth(ctx context.Context, student, month string, old Month, fresh []OriginalTurn, result Extracted, now time.Time) (Month, error) {
	paths, err := MonthlyPaths(student, month)
	if err != nil {
		return Month{}, err
	}
	if strings.TrimSpace(result.Summary) == "" || utf8.RuneCountInString(result.Summary) > 2000 {
		return Month{}, errors.New("student memory: invalid extraction size")
	}
	if len(result.Topics) > 200 {
		return Month{}, errors.New("student memory: too many extracted topics")
	}
	// Resolve source coordinates; only turns that actually exist may be cited.
	sources := map[string]OriginalTurn{}
	for _, t := range append(append([]OriginalTurn{}, old.Raw.Content.Turns...), fresh...) {
		sources[SourceID(t.Source)] = t
	}
	// Preserve old profile, then merge fresh authored turns.
	areas := map[string]*KnowledgeArea{}
	order := []string{}
	for _, a := range old.Summary.Content.KnowledgeAreas {
		cp := a
		cp.Topics = append([]Topic{}, a.Topics...)
		// Legacy v2 topics predate the forgetting dimension; backfill neutral
		// priors so their retention math does not degenerate to zero.
		for i := range cp.Topics {
			if cp.Topics[i].StateVersion == 0 && cp.Topics[i].Stability == 0 {
				cp.Topics[i].Stability = DefaultStability
			}
			if cp.Topics[i].StateVersion == 0 && cp.Topics[i].Difficulty == 0 {
				cp.Topics[i].Difficulty = DefaultDifficulty
			}
		}
		areas[a.Area] = &cp
		order = append(order, a.Area)
	}
	// Build per-turn adjudicated topics from extracted candidates.
	byKey := map[string]*Topic{}
	for _, a := range areas {
		for i := range a.Topics {
			topic := a.Topics[i]
			topic.TopicID = CanonicalTopicID(a.Area, topic.Topic, topic.TopicID)
			key := a.Area + "/" + topic.TopicID
			if prior, ok := byKey[key]; ok {
				topic.QuestionIDs = mergeSorted(prior.QuestionIDs, topic.QuestionIDs)
				if prior.LastSeen > topic.LastSeen {
					prior.QuestionIDs = topic.QuestionIDs
					continue
				}
			}
			byKey[key] = &topic
		}
	}
	result.Topics = append([]ExtractedTopic(nil), result.Topics...)
	sort.SliceStable(result.Topics, func(i, j int) bool {
		var a, b time.Time
		if len(result.Topics[i].SourceIDs) > 0 {
			a = sources[result.Topics[i].SourceIDs[0]].CreatedAt
		}
		if len(result.Topics[j].SourceIDs) > 0 {
			b = sources[result.Topics[j].SourceIDs[0]].CreatedAt
		}
		return a.Before(b)
	})
	freshIDs := map[string]bool{}
	for _, raw := range fresh {
		freshIDs[SourceID(raw.Source)] = true
	}
	previousSources := map[string]bool{}
	for _, raw := range old.Raw.Content.Turns {
		previousSources[SourceID(raw.Source)] = true
	}
	applied := map[string]bool{}
	for _, et := range result.Topics {
		if !ValidArea(et.Area) || !ValidAbility(et.Ability) {
			return Month{}, errors.New("student memory: unregistered taxonomy code")
		}
		if et.Ability == "AB-SPACE" && (et.Area == "ALG" || et.Area == "SEQ" || et.Area == "PROB" || et.Area == "CALC") {
			et.Ability = "AB-REASON"
		}
		if !et.Authored {
			continue // teacher demonstration / answer-only turns never change mastery
		}
		if strings.TrimSpace(et.Topic) == "" || utf8.RuneCountInString(et.Topic) > 60 {
			return Month{}, errors.New("student memory: invalid topic")
		}
		if !ValidMastery(et.Suggested) {
			return Month{}, errors.New("student memory: invalid suggested mastery")
		}
		// Server-side normalization: a mastered turn can never be incorrect.
		// This guards against a contradictory model output (mastered + incorrect).
		assisted, unavailable := false, false
		for _, sid := range et.SourceIDs {
			if raw, ok := sources[sid]; ok {
				assisted = assisted || raw.Assisted
				unavailable = unavailable || raw.AttemptUnavailable || raw.Purpose == "question" || raw.Purpose == "self_report"
				if raw.Purpose == "attempt" && raw.Evaluation != nil && raw.Evaluation.Incorrect {
					et.Incorrect = true
					if et.Suggested == MasteryMastered {
						et.Suggested = MasteryFamiliar
					}
				}
				if raw.Purpose == "attempt" && (raw.Evaluation == nil || !raw.Evaluation.Verified) && et.Suggested == MasteryMastered {
					et.Suggested = MasteryPartial
				}
			}
		}
		if unavailable {
			continue
		}
		if et.Suggested == MasteryMastered && assisted {
			et.Suggested = MasteryPartial
		}
		if et.Suggested == MasteryMastered {
			et.Incorrect = false
		}
		if len(et.SourceIDs) != 1 {
			return Month{}, errors.New("student memory: each observation requires one authored turn")
		}
		canonical := resolveExistingTopic(et.Area, et.Topic, et.TopicID, byKey)
		newSources := []string{}
		for _, sid := range dedupe(et.SourceIDs) {
			if previousSources[sid] {
				continue
			}
			if !freshIDs[sid] {
				return Month{}, errors.New("student memory: observation must cite fresh evidence")
			}
			key := et.Area + "/" + canonical + "/" + sid
			if !applied[key] {
				applied[key] = true
				newSources = append(newSources, sid)
			}
		}
		if len(newSources) == 0 {
			continue
		}
		et.SourceIDs = newSources
		lastSeen := ""
		questionIDs := []string{}
		for _, id := range et.SourceIDs {
			raw, ok := sources[id]
			if !ok {
				return Month{}, fmt.Errorf("student memory: fabricated source")
			}
			if raw.CreatedAt.Format(time.RFC3339) > lastSeen {
				lastSeen = raw.CreatedAt.Format(time.RFC3339)
			}
			if raw.Source.QuestionID != "" {
				questionIDs = append(questionIDs, raw.Source.QuestionID)
			}
		}
		if et.QuestionID != "" {
			found := false
			for _, qid := range questionIDs {
				found = found || qid == et.QuestionID
			}
			if !found {
				return Month{}, errors.New("student memory: question id not bound to evidence")
			}
		}
		for i := range fresh {
			for _, sid := range et.SourceIDs {
				if SourceID(fresh[i].Source) == sid {
					if fresh[i].Assessment == nil {
						fresh[i].Assessment = &AttemptAssessment{Authored: true, Mastery: et.Suggested, Incorrect: et.Incorrect}
					} else {
						fresh[i].Assessment.Incorrect = fresh[i].Assessment.Incorrect || et.Incorrect
					}
				}
			}
		}

		questionIDs = dedupe(questionIDs)
		// Adjudicate mastery: highest rank wins across turns. Same (area, topic)
		// reoccurring updates error_count: a correct answer (mastered) resets it
		// to zero, otherwise an incorrect turn increments it.
		topicID := resolveExistingTopic(et.Area, et.Topic, et.TopicID, byKey)
		key := et.Area + "/" + topicID
		if existing, ok := byKey[key]; ok {
			// Zero-value guard for legacy v2 documents written before the
			// forgetting dimension: treat missing stability/difficulty as the
			// neutral prior instead of drifting toward zero.
			if existing.StateVersion == 0 && existing.Stability == 0 {
				existing.Stability = DefaultStability
			}
			if existing.StateVersion == 0 && existing.Difficulty == 0 {
				existing.Difficulty = DefaultDifficulty
			}
			if existing.BestMastery == "" || existing.StateVersion < 2 {
				existing.BestMastery = et.Suggested
			}
			if MasteryRank(et.Suggested) > MasteryRank(existing.BestMastery) {
				existing.BestMastery = et.Suggested
			}
			if lastSeen < existing.LastSeen {
				continue
			}
			existing.Mastery = et.Suggested
			existing.Assisted = assisted
			existing.Incorrect = et.Incorrect
			existing.StateVersion = 2
			existing.Ability = et.Ability
			if lastSeen > existing.LastSeen {
				existing.LastSeen = lastSeen
			}
			if et.Suggested == MasteryMastered {
				existing.ErrorCount = 0
			} else if et.Incorrect {
				existing.ErrorCount++
				existing.TotalErrors++
			}
			existing.Stability = UpdateStability(existing.Stability, et.Suggested)
			existing.Difficulty = UpdateDifficulty(existing.ErrorCount)
			existing.QuestionIDs = mergeSorted(existing.QuestionIDs, questionIDs)
			continue
		}
		area := areas[et.Area]
		if area == nil {
			areas[et.Area] = &KnowledgeArea{Area: et.Area}
			area = areas[et.Area]
			order = append(order, et.Area)
		}
		errCount := 0
		if et.Incorrect && et.Suggested != MasteryMastered {
			errCount = 1
		}
		stability := UpdateStability(DefaultStability, et.Suggested)
		difficulty := UpdateDifficulty(errCount)
		t := Topic{Assisted: assisted, Incorrect: et.Incorrect, TopicID: topicID, BestMastery: et.Suggested, TotalErrors: errCount, StateVersion: 2, Topic: et.Topic, Mastery: et.Suggested, Ability: et.Ability, ErrorCount: errCount, LastSeen: lastSeen, Stability: stability, Difficulty: difficulty, QuestionIDs: questionIDs}
		area.Topics = append(area.Topics, t)
		byKey[key] = &t
	}
	// Build ordered area slice.
	areasSorted := []KnowledgeArea{}
	for _, a := range order {
		if aa, ok := areas[a]; ok {
			aa.Topics = nil
			for key, topic := range byKey {
				if strings.HasPrefix(key, a+"/") {
					aa.Topics = append(aa.Topics, *topic)
				}
			}
			sort.Slice(aa.Topics, func(i, j int) bool { return aa.Topics[i].Topic < aa.Topics[j].Topic })
			areasSorted = append(areasSorted, *aa)
		}
	}
	sort.Slice(areasSorted, func(i, j int) bool { return areasSorted[i].Area < areasSorted[j].Area })
	// L3 is append-only: old turns keep their order; only the fresh batch is
	// time-ordered before being appended. Renumber seq contiguously by append
	// order. Span is computed across all turns regardless of order.
	oldTurns := append([]OriginalTurn{}, old.Raw.Content.Turns...)
	freshSorted := append([]OriginalTurn{}, fresh...)
	sort.SliceStable(freshSorted, func(i, j int) bool {
		if freshSorted[i].CreatedAt.Equal(freshSorted[j].CreatedAt) {
			return SourceID(freshSorted[i].Source) < SourceID(freshSorted[j].Source)
		}
		return freshSorted[i].CreatedAt.Before(freshSorted[j].CreatedAt)
	})
	allTurns := append(oldTurns, freshSorted...)
	for i := range allTurns {
		allTurns[i].Seq = i + 1
	}
	span := Span{}
	if len(allTurns) > 0 {
		first, last := allTurns[0].CreatedAt, allTurns[0].CreatedAt
		for _, t := range allTurns[1:] {
			if t.CreatedAt.Before(first) {
				first = t.CreatedAt
			}
			if t.CreatedAt.After(last) {
				last = t.CreatedAt
			}
		}
		span.First = first.Format("2006-01-02")
		span.Last = last.Format("2006-01-02")
	}
	// Derive evidence dimensions from accepted source turns, never ask the extractor to guess.
	for ai := range areasSorted {
		for ti := range areasSorted[ai].Topics {
			topic := &areasSorted[ai].Topics[ti]
			topic.EvidenceVersion = 1
			topic.EvidenceCount = 0
			topic.IndependentCorrect = 0
			topic.AssistedAttempts = 0
			for _, turn := range allTurns {
				if turn.Assessment == nil || !turn.Assessment.Authored {
					continue
				}
				matches := false
				for _, qid := range topic.QuestionIDs {
					if qid != "" && qid == turn.Source.QuestionID {
						matches = true
						break
					}
				}
				if !matches {
					continue
				}
				topic.EvidenceCount++
				if turn.Assisted {
					topic.AssistedAttempts++
				} else if turn.Evaluation != nil && turn.Evaluation.Verified && !turn.Evaluation.Incorrect {
					topic.IndependentCorrect++
				}
			}
			topic.NextAction = "需要新的独立作答证据"
			if topic.Incorrect {
				topic.NextAction = "围绕最近错误安排一次独立重做"
			} else if topic.Assisted {
				topic.NextAction = "减少提示，安排一道独立变式题"
			} else if topic.IndependentCorrect > 0 {
				topic.NextAction = "间隔后复习，验证能否保持"
			}
		}
	}
	revision := old.Raw.Revision + 1
	summaryContent := SummaryContent{LegacyKeywords: old.Summary.Content.LegacyKeywords, LegacyInformation: old.Summary.Content.LegacyInformation, Summary: evidenceSummary(areasSorted), KnowledgeAreas: areasSorted, TurnCount: len(allTurns), Span: span}
	if err := ValidateSummaryContent(summaryContent); err != nil {
		return Month{}, err
	}
	next := Month{
		Summary: Document[SummaryContent]{SchemaVersion: SchemaVersion, Layer: "L2", StudentID: student, Month: month, Revision: revision, UpdatedAt: now.UTC(), NextPaths: []string{paths.Content}, Content: summaryContent},
		Raw:     Document[RawContent]{SchemaVersion: SchemaVersion, Layer: "L3", StudentID: student, Month: month, Revision: revision, UpdatedAt: now.UTC(), NextPaths: []string{}, Content: RawContent{Turns: allTurns}},
	}
	next.Index, err = indexThroughTool(ctx, next.Summary)
	if err != nil {
		return Month{}, err
	}
	return next, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func mergeSorted(a, b []string) []string {
	return dedupe(append(append([]string{}, a...), b...))
}

// The narrative is derived from adjudicated state, so a free-form extraction
// summary cannot turn a teacher demonstration into a claim of mastery.
func evidenceSummary(areas []KnowledgeArea) string {
	topics := []Topic{}
	for _, a := range areas {
		topics = append(topics, a.Topics...)
	}
	if len(topics) == 0 {
		return "已保存题目与教学对话，暂无足够的学生作答证据判断掌握情况。"
	}
	sort.SliceStable(topics, func(i, j int) bool { return topics[i].LastSeen > topics[j].LastSeen })
	parts := []string{fmt.Sprintf("已有%d个知识点的来源记录。近期状态：", len(topics))}
	for i, t := range topics {
		if i >= 12 {
			break
		}
		label := "仍需巩固"
		if t.StateVersion < 2 {
			label = "历史记录，待验证"
		} else if t.Assisted {
			label = "提示后完成或练习，不能视为独立掌握"
		} else if t.Mastery == MasteryMastered {
			label = "已有无提示完整作答证据"
		} else if t.Mastery == MasteryPartial {
			label = "部分理解"
		}
		parts = append(parts, t.Topic+"："+label+"。")
	}
	return strings.Join(parts, "")
}

// Reuse only an authorized existing identity or an exact normalized label;
// similarity alone must not merge mathematically distinct concepts.
func resolveExistingTopic(area, label, id string, existing map[string]*Topic) string {
	if _, ok := existing[area+"/"+id]; id != "" && ok {
		return id
	}
	normalized := strings.Join(strings.Fields(label), "")
	for key, topic := range existing {
		if strings.HasPrefix(key, area+"/") && strings.Join(strings.Fields(topic.Topic), "") == normalized {
			return topic.TopicID
		}
	}
	return CanonicalTopicID(area, label, id)
}
