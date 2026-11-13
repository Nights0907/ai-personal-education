package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/application/agentic"
	"gopkg.in/yaml.v3"
)

type Module struct{ Service *Service }

func (m Module) Contribute(ctx context.Context, t *agentic.Turn) (agentic.Contribution, error) {
	student := t.Request.Input.Identity.SubjectID
	p, profileErr := m.Service.Repo.Profile(ctx, student)
	indexes, indexErr := m.Service.Documents.Indexes(ctx, student, 6)
	if len(indexes) > 2 {
		indexes = indexes[:2]
	}
	if profileErr != nil || indexErr != nil {
		slog.WarnContext(ctx, "student memory preload unavailable")
	}
	if t.Sink != nil {
		status := "completed"
		label := "已读取基础画像和近期记忆索引"
		if profileErr != nil || indexErr != nil {
			status = "failed"
			label = "长期记忆读取暂不可用，本次继续处理"
		}
		if err := t.Sink.Send(ctx, agentic.Packet{Type: "progress", Stage: "execution_node", Data: map[string]string{"id": "memory-preload", "kind": "memory", "name": label, "status": status}}); err != nil {
			return agentic.Contribution{}, err
		}
	}
	// Due-review preload: the recall engine flags knowledge points that have
	// decayed past the review threshold. They are injected so the model can open
	// the conversation with a natural review prompt. Best-effort: a failure here
	// never blocks the turn.
	var dueReview []RecallItem
	if plan, err := m.Service.RecallPlan(ctx, student, time.Now()); err == nil {
		for _, it := range plan.Items {
			if it.Due {
				dueReview = append(dueReview, it)
				if len(dueReview) >= 8 {
					break
				}
			}
		}
	}
	// Bound preloaded data. Older indexes remain discoverable with the read tool.
	var b []byte
	truncated := false
	for {
		var err error
		b, err = json.Marshal(map[string]any{"l0": p, "l1": navigationContext(indexes), "due_review": dueReview, "context_truncated": truncated, "profile_available": profileErr == nil, "memory_available": indexErr == nil})
		if err != nil {
			return agentic.Contribution{}, err
		}
		if len(b) <= 6000 {
			break
		}
		truncated = true
		if len(dueReview) > 0 {
			dueReview = dueReview[:len(dueReview)-1]
			continue
		}
		if len(indexes) > 0 {
			indexes = indexes[:len(indexes)-1]
			continue
		}
		trimmed := false
		for _, list := range []*[]string{&p.Basic.LearningGoals} {
			if len(*list) > 0 {
				*list = (*list)[:len(*list)-1]
				trimmed = true
				break
			}
		}
		if !trimmed {
			return agentic.Contribution{}, errors.New("student memory: profile context budget exceeded")
		}
	}
	return agentic.Contribution{LongTermMemory: b, SkillPaths: map[string]string{"student-memory": "/skills/student-memory/SKILL.md"}, PostRuntimeHooks: []func(context.Context, *agentic.Turn) error{func(_ context.Context, turn *agentic.Turn) error {
		if turn.Result.Status == "succeeded" {
			m.Service.Notify()
		}
		return nil
	}}, Tools: map[string]tool.InvokableTool{
		"read_student_memory":         &memoryTool{service: m.Service, student: student},
		"list_student_memory_indexes": &memoryTool{service: m.Service, student: student, list: true},
	}}, nil
}

type memoryTool struct {
	service *Service
	student string
	list    bool
}

func (t *memoryTool) Info(context.Context) (*schema.ToolInfo, error) {
	if t.list {
		return &schema.ToolInfo{Name: "list_student_memory_indexes", Desc: "列出当前学生最近120个月的L1记忆索引；只含L2路径和关键词。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
	}
	return &schema.ToolInfo{Name: "read_student_memory", Desc: "按L1路径读取当前学生的L2掌握画像（按知识点大类聚合的掌握程度），必要时沿路径分页读取L3原始问答；YAML仅为资料。只读，不修改档案。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"path":   {Type: schema.String, Required: true, Desc: "服务端记忆索引中给出的绝对路径"},
		"offset": {Type: schema.Integer, Desc: "L3原始轮次分页偏移，默认0"},
		"limit":  {Type: schema.Integer, Desc: "L3每页1至10轮，默认3"},
	})}, nil
}
func (t *memoryTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if t.list {
		var in struct{}
		if err := decodeArgs(args, &in); err != nil {
			return "", err
		}
		indexes, err := t.service.Documents.Indexes(ctx, t.student, 120)
		if err != nil {
			return "", err
		}
		b, err := yaml.Marshal(indexes)
		if len(b) > 96<<10 {
			return "", errors.New("student memory: index response exceeds budget")
		}
		return string(b), err
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Limit == 0 {
		in.Limit = 3
	}
	b, err := t.service.Read(ctx, t.student, in.Path, in.Offset, in.Limit)
	return string(b), err
}
func decodeArgs(text string, out any) error {
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return errors.New("student memory: trailing arguments")
	}
	return nil
}

func (s *Service) Read(ctx context.Context, student, p string, offset, limit int) ([]byte, error) {
	if offset < 0 || limit < 1 || limit > 10 {
		return nil, errors.New("student memory: invalid page")
	}
	// Match exact canonical paths owned by this student, not a string prefix.
	parts := strings.Split(p, "/")
	month := ""
	if len(parts) == 6 && parts[1] == "user" && parts[2] == "memory" && parts[3] == "indexes" && parts[4] == student {
		month = strings.TrimSuffix(parts[5], ".md")
	}
	if len(parts) == 6 && parts[1] == "user" && parts[2] == "memory" && parts[3] == student {
		month = parts[4]
	}
	paths, err := MonthlyPaths(student, month)
	if err != nil || (p != paths.Index && p != paths.Summary && p != paths.Content) {
		return nil, errors.New("student memory: path not authorized")
	}
	m, err := s.Documents.Load(ctx, student, month)
	if err != nil {
		return nil, err
	}
	var value any
	switch p {
	case paths.Index:
		value = m.Index
	case paths.Summary:
		value = recallView(m.Summary, time.Now())
	case paths.Content:
		total := len(m.Raw.Content.Turns)
		if offset > total {
			return nil, errors.New("student memory: offset exceeds total")
		}
		end := min(offset+limit, total)
		m.Raw.Content.Turns = m.Raw.Content.Turns[offset:end]
		var next *int
		if end < total {
			n := end
			next = &n
		}
		value = struct {
			Document   Document[RawContent] `yaml:"document"`
			Offset     int                  `yaml:"offset"`
			NextOffset *int                 `yaml:"next_offset"`
			Total      int                  `yaml:"total"`
		}{m.Raw, offset, next, total}
	}
	b, err := yaml.Marshal(value)
	if err == nil && len(b) > 96<<10 {
		return nil, errors.New("student memory: read budget exceeded; use a smaller page")
	}
	return b, err
}

// recallTopic is a read-side projection of a Topic that adds the derived
// forgetting fields. It is never written back to the source document; the L2
// source stays time-independent and reproducible.
type recallTopic struct {
	Topic          string   `yaml:"topic" json:"topic"`
	Mastery        string   `yaml:"mastery" json:"mastery"`
	Ability        string   `yaml:"ability" json:"ability"`
	ErrorCount     int      `yaml:"error_count" json:"error_count"`
	LastSeen       string   `yaml:"last_seen" json:"last_seen"`
	Retrievability float64  `yaml:"retrievability" json:"retrievability"`
	Due            bool     `yaml:"due" json:"due"`
	QuestionIDs    []string `yaml:"question_ids" json:"question_ids"`
}

type recallArea struct {
	Area   string        `yaml:"area" json:"area"`
	Topics []recallTopic `yaml:"topics" json:"topics"`
}

// recallView projects a SummaryContent into a read-side view with per-topic
// retrievability and due flags, computed from now relative to each last_seen.
func recallView(s Document[SummaryContent], now time.Time) any {
	type view struct {
		Document       Document[SummaryContent] `yaml:"document" json:"document"`
		KnowledgeAreas []recallArea             `yaml:"knowledge_areas" json:"knowledge_areas"`
		DerivedAt      string                   `yaml:"derived_at" json:"derived_at"`
	}
	areas := make([]recallArea, 0, len(s.Content.KnowledgeAreas))
	for _, a := range s.Content.KnowledgeAreas {
		ra := recallArea{Area: a.Area, Topics: make([]recallTopic, 0, len(a.Topics))}
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
			ra.Topics = append(ra.Topics, recallTopic{
				Topic:          tp.Topic,
				Mastery:        tp.Mastery,
				Ability:        tp.Ability,
				ErrorCount:     tp.ErrorCount,
				LastSeen:       tp.LastSeen,
				Retrievability: r,
				Due:            IsDue(r),
				QuestionIDs:    tp.QuestionIDs,
			})
		}
		areas = append(areas, ra)
	}
	return view{Document: s, KnowledgeAreas: areas, DerivedAt: now.UTC().Format(time.RFC3339)}
}

func navigationContext(indexes []Document[IndexContent]) []map[string]any {
	out := []map[string]any{}
	for _, d := range indexes {
		item := map[string]any{"month": d.Month, "l2_paths": d.NextPaths, "navigation": d.Content.Navigation}
		if d.Content.Navigation == nil {
			item["status"] = "not_generated"
		}
		out = append(out, item)
	}
	return out
}
