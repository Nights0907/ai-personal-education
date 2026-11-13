package solve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"

	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/query"
	"io"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

var Catalog = map[string]string{"sets_logic": "集合与逻辑", "functions_derivatives": "函数与导数", "sequences": "数列", "trigonometry": "三角函数", "analytic_geometry": "平面解析几何", "solid_geometry": "立体几何", "probability_statistics": "概率与统计", "equations_inequalities": "方程与不等式"}

type Service struct {
	RadarDB  *sql.DB
	Photos   *photo.Service
	Repo     Repository
	Queries  query.Repository
	Store    storage.Storage
	Reviewer model.BaseChatModel
	ModelRef string
}

func Decode(raw string, out any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var more any
	if d.Decode(&more) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func SourceHash(r query.Record) string {
	if r.ImageAssetID != "" {
		return state.Hash([]string{r.Query, r.ID, r.TurnID, r.ImageAssetID})
	}
	return state.Hash([]string{r.Query, r.ID, r.TurnID})
}

// ResolveSource resolves immutable, owned OCR output. Raw captions remain in Agent Store.
func ResolveSource(ctx context.Context, p *photo.Service, student string, r query.Record) (query.Record, *photo.Result, error) {
	if r.ImageAssetID == "" {
		return r, nil, nil
	}
	if p == nil {
		return r, nil, photo.ErrUnavailable
	}
	v, e := p.Cached(ctx, student, r.ImageAssetID)
	if e != nil {
		return r, nil, e
	}
	if e := photo.ValidateQuestionText(v.Text); e != nil {
		return r, nil, e
	}
	r.Query = v.Text
	return r, &v, nil
}
func (s *Service) Input(ctx context.Context, in state.Input) (query.Record, error) {
	r, e := s.Queries.Get(ctx, in.Identity.SubjectID, in.InputRef)
	if e != nil {
		return r, e
	}
	if in.Identity.Namespace != "teacher" || r.ConversationID != in.Identity.ConversationID || r.TurnID != in.Identity.TurnID {
		return r, state.ErrConflict
	}
	return r, nil
}
func Validate(c Candidate) error {
	if utf8.RuneCountInString(c.Solution.TeacherResponse) > 16000 {
		return fmt.Errorf("%w: teacher_response过长", ErrInvalid)
	}
	good := func(s string, n int) bool {
		return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= n
	}
	if !good(c.Question.Stem, 20000) || !good(c.Solution.Analysis, 12000) || len(c.Solution.Answer) < 1 || len(c.Solution.Answer) > 30 || len(c.Solution.Steps) < 1 || len(c.Solution.Steps) > 60 || len(c.Question.Subquestions) > 30 || len(c.Question.Options) > 20 {
		return fmt.Errorf("%w: question.stem、solution.analysis不能为空；答案1-30条、步骤1-60条、小问最多30条、选项最多20条，且文字须在字段长度限制内", ErrInvalid)
	}
	if !validDifficulty[c.Question.Difficulty] {
		return fmt.Errorf("%w: question.difficulty必须为easy、medium或hard", ErrInvalid)
	}
	switch c.Question.Type {
	case "single_choice", "multiple_choice", "fill_blank", "constructed_response":
	default:
		return fmt.Errorf("%w: question.type必须为single_choice、multiple_choice、fill_blank或constructed_response", ErrInvalid)
	}
	subs := map[string]bool{}
	for _, q := range c.Question.Subquestions {
		if !good(q.ID, 64) || !good(q.Text, 8000) || subs[q.ID] {
			return fmt.Errorf("%w: question.subquestions须有非空且唯一的id及非空text", ErrInvalid)
		}
		subs[q.ID] = true
	}
	validRef := func(id *string) bool {
		if len(subs) == 0 {
			return id == nil
		}
		return id != nil && subs[*id]
	}
	answered := map[string]bool{}
	for _, a := range c.Solution.Answer {
		if !good(a.Content, 8000) || !validRef(a.SubquestionID) {
			return fmt.Errorf("%w: answer.content不能为空；有小问时subquestion_id须引用已声明的小问，无小问时须为null", ErrInvalid)
		}
		key := ""
		if a.SubquestionID != nil {
			key = *a.SubquestionID
		}
		if answered[key] {
			return fmt.Errorf("%w: 答案的subquestion_id重复。无小问时将多个结果合并成一条subquestion_id=null的答案；有多个小问时先声明question.subquestions并分别引用唯一id", ErrInvalid)
		}
		answered[key] = true
	}
	if len(subs) > 0 && len(answered) != len(subs) {
		return fmt.Errorf("%w: 每个question.subquestions的小问必须恰有一条对应答案", ErrInvalid)
	}
	for n, step := range c.Solution.Steps {
		if step.StepID != "" || step.Ordinal != n+1 || !validRef(step.SubquestionID) || !good(step.Title, 200) || !good(step.Content, 8000) {
			return fmt.Errorf("%w: invalid step %d", ErrInvalid, n+1)
		}
	}
	keys := map[string]bool{}
	for _, o := range c.Question.Options {
		if !good(o.Key, 32) || !good(o.Text, 8000) || keys[o.Key] {
			return fmt.Errorf("%w: options的key须唯一，key和text不能为空", ErrInvalid)
		}
		keys[o.Key] = true
	}
	if (c.Question.Type == "single_choice" || c.Question.Type == "multiple_choice") && len(keys) < 2 {
		return fmt.Errorf("%w: 选择题至少需要两个选项", ErrInvalid)
	}
	primary := 0
	codes := map[string]bool{}
	if len(c.Solution.KnowledgePoints) < 1 || len(c.Solution.KnowledgePoints) > 15 {
		return fmt.Errorf("%w: knowledge_points须有1-15项", ErrInvalid)
	}
	for _, k := range c.Solution.KnowledgePoints {
		if _, ok := Catalog[k.Code]; !ok || codes[k.Code] {
			return fmt.Errorf("%w: 知识点code须来自get_knowledge_catalog且不可重复", ErrInvalid)
		}
		codes[k.Code] = true
		if k.IsPrimary {
			primary++
		}
	}
	if primary != 1 {
		return fmt.Errorf("%w: knowledge_points必须恰有一个is_primary=true", ErrInvalid)
	}
	encoded, _ := json.Marshal(c)
	if len(encoded) > 128*1024 {
		return fmt.Errorf("%w: 候选解答超过128KB限制", ErrInvalid)
	}
	return nil
}
func (s *Service) ValidateDraft(ctx context.Context, in state.Input, c Candidate) (map[string]any, error) {
	if err := Validate(c); err != nil {
		return map[string]any{"passed": false, "draft_id": nil, "issues": []string{err.Error()}, "review_status": "structure_failed"}, nil
	}
	r, e := s.Input(ctx, in)
	if e != nil {
		return nil, e
	}
	r, _, e = ResolveSource(ctx, s.Photos, in.Identity.SubjectID, r)
	if e != nil {
		return nil, e
	}
	if r.RadarSourceID != "" {
		_, err := s.Repo.Get(ctx, in.Identity.SubjectID, r.RadarSourceID)
		if err != nil {
			return nil, err
		}
		if err = s.CheckRadar(ctx, in.Identity.SubjectID, r.RadarSourceID, r.RadarSeed, c); err != nil {
			return map[string]any{"passed": false, "issues": []string{err.Error()}, "review_status": "radar_verification_failed"}, nil
		}
	}
	// Structural validation remains; external mathematical review is disabled.
	review := Review{Passed: true, Issues: []string{}}
	id, e := s.Repo.SaveDraft(ctx, in, SourceHash(r), c, review)
	if e != nil {
		return nil, e
	}
	return map[string]any{"passed": true, "draft_id": id, "issues": []string{}, "review_status": "structure_validated"}, nil
}
func (s *Service) FindCommittedResult(ctx context.Context, in state.Input) (*state.Receipt, error) {
	v, e := s.Repo.Find(ctx, in.Identity.Scope, in.Identity.TurnID)
	if errors.Is(e, state.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &v.Receipt, nil
}
func (s *Service) Repair(ctx context.Context, v Saved) error {
	if _, e := s.Store.GetTurn(ctx, v.Input.Identity.Scope, v.Input.Identity.TurnID); e == nil {
		return nil
	} else if !errors.Is(e, state.ErrNotFound) {
		return e
	}
	ms, e := state.RestoreTerminal(v.Receipt.FrozenPrefix, &v.Receipt)
	if e != nil {
		return e
	}
	return s.Store.Finish(ctx, v.Input, "succeeded", "", &v.Receipt, ms)
}
func (s *Service) RunRepair(ctx context.Context) {
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.Repo.RepairPending(ctx, s.Repair); err != nil {
				log.Printf("teaching history repair pending retry (%T)", err)
			}
		}
	}
}
