package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/modelstream"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/modules/teaching/tasks"
	"github.com/musi-ai/education/internal/query"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func taskError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, tasks.ErrNotFound):
		failure(w, 404, "学习任务不存在")
	case errors.Is(e, tasks.ErrConflict):
		failure(w, 409, "学习任务状态已变化，请刷新核对")
	default:
		failure(w, 503, "暂时无法读取学习任务，请重试")
	}
}
func (s Server) createTeachingTask(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "后台教学任务尚未配置")
		return
	}
	var body struct {
		query.Input
		ParentID string `json:"parent_task_id"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	var tail any
	if e := d.Decode(&body); e != nil {
		failure(w, 400, "学习任务格式不正确")
		return
	}
	if d.Decode(&tail) != io.EOF {
		failure(w, 400, "学习任务仅接受一个JSON对象")
		return
	}
	body.Input.Normalize()
	if body.Input.Validate() != nil || (body.Mode != query.GuideMode && body.Mode != query.SolveMode && body.Mode != query.ReviewMode) || (body.ParentID != "" && !query.ValidID(body.ParentID)) {
		failure(w, 400, "请输入有效的引导题目")
		return
	}
	if !s.checkImage(w, r, body.ImageAssetID) {
		return
	}
	if body.RegenerateOf != "" {
		if e := s.resumeTeaching(r.Context(), &body.Input); e != nil {
			repoError(w, e)
			return
		}
	}
	t, e := s.Tasks.Repo.Create(r.Context(), s.StudentID, body.Input, body.ParentID)
	if e != nil {
		taskError(w, e)
		return
	}
	s.Tasks.Notify()
	writeJSON(w, 202, t)
}
func (s Server) teachingTask(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "后台教学任务尚未配置")
		return
	}
	id := r.PathValue("task")
	after, e := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "" {
		after = 0
		e = nil
	}
	if e != nil || after < 0 || !query.ValidID(id) {
		failure(w, 400, "任务编号或事件位置不正确")
		return
	}
	t, e := s.Tasks.Repo.Get(r.Context(), s.StudentID, id)
	if e != nil {
		taskError(w, e)
		return
	}
	events, e := s.Tasks.Repo.Events(r.Context(), s.StudentID, id, after)
	if e != nil {
		taskError(w, e)
		return
	}
	// Older tasks predate visible-node events. Recover only actual persisted calls;
	// "recorded" deliberately does not assert a tool's business success.
	if after == 0 && t.Status == "READY" && s.Teaching != nil && s.Teaching.Store != nil && t.Result["turn_id"] != "" {
		hasCalls := false
		for _, ev := range events {
			if ev.Type == "execution_node" && (ev.Data["kind"] == "tool" || ev.Data["kind"] == "skill") {
				hasCalls = true
				break
			}
		}
		if !hasCalls {
			scope := state.Scope{Namespace: "teacher", SubjectID: s.StudentID, ConversationID: t.ConversationID}
			if turn, err := s.Teaching.Store.GetTurn(r.Context(), scope, t.Result["turn_id"]); err == nil {
				for _, message := range turn.Messages {
					if message == nil {
						continue
					}
					for _, call := range message.ToolCalls {
						kind, name := "tool", call.Function.Name
						if name == "read_file" {
							var args struct {
								Path string `json:"path"`
							}
							if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && strings.HasPrefix(args.Path, "/skills/") && strings.HasSuffix(args.Path, "/SKILL.md") {
								kind, name = "skill", strings.TrimSuffix(strings.TrimPrefix(args.Path, "/skills/"), "/SKILL.md")
							}
						}
						events = append(events, tasks.Event{Type: "execution_node", Data: map[string]string{"id": call.ID, "kind": kind, "name": name, "status": "recorded"}})
					}
				}
			}
		}
	}
	if s.StudentMemory != nil && t.Status == "READY" && t.Result["turn_id"] != "" {
		// Stored evidence, rather than task completion, establishes memory persistence.
		month := t.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01")
		if doc, err := s.StudentMemory.Documents.Load(r.Context(), s.StudentID, month); err == nil {
			for _, raw := range doc.Raw.Content.Turns {
				if raw.Source.TurnID == t.Result["turn_id"] {
					events = append(events, tasks.Event{Type: "execution_node", Data: map[string]string{"id": "memory-extraction", "kind": "memory", "name": "长期记忆已存储（画像按作答证据更新）", "status": "completed"}})
					break
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"task": t, "events": events})
}
func (s Server) teachingTasks(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "后台教学任务尚未配置")
		return
	}
	items, e := s.Tasks.Repo.List(r.Context(), s.StudentID, r.URL.Query().Get("conversation_id"))
	if e != nil {
		taskError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s Server) retryTeachingTask(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "后台教学任务尚未配置")
		return
	}
	if e := s.Tasks.Repo.Retry(r.Context(), s.StudentID, r.PathValue("task")); e != nil {
		taskError(w, e)
		return
	}
	s.Tasks.Notify()
	t, e := s.Tasks.Repo.Get(r.Context(), s.StudentID, r.PathValue("task"))
	if e != nil {
		taskError(w, e)
		return
	}
	writeJSON(w, 202, t)
}

// ExecuteTeachingTask runs the same teacher pipeline outside the HTTP request
// lifetime. Only safe progress and visible dialogue enter the durable event log.
func (s Server) ExecuteTeachingTask(ctx context.Context, t tasks.Task, emit func(string, map[string]string) (string, error)) (map[string]string, error) {
	in := t.Input
	in.SubmittedAt = t.CreatedAt
	if t.ParentID != "" {
		parent, e := s.Tasks.Repo.Get(ctx, t.StudentID, t.ParentID)
		if e != nil {
			return nil, e
		}
		if parent.Status != "READY" || parent.Result["question_id"] == "" {
			return nil, errors.New("参考解答尚未就绪，作答已保留")
		}
		in.GuideQuestionID = parent.Result["question_id"]
		in.Mode = query.GuideMode
	}
	record, _, e := s.Repo.Create(ctx, t.StudentID, in)
	if e != nil {
		return nil, errors.New("原题暂时无法绑定，已保留任务，可重试")
	}
	if _, e = emit("accepted", map[string]string{"message_id": record.ID, "turn_id": record.TurnID, "assistant_message_id": state.MessageID(state.Scope{Namespace: "teacher", SubjectID: t.StudentID, ConversationID: record.ConversationID}, record.TurnID, "assistant")}); e != nil {
		return nil, e
	}

	canNarrate := false
	originalEmit := emit
	emit = func(kind string, data map[string]string) (string, error) {
		if kind == "execution_node" && data["name"] == "evaluate_student_attempt" && data["status"] == "completed" {
			canNarrate = true
		}
		return originalEmit(kind, data)
	}
	ctx = modelstream.WithObserver(ctx, teachingNarrativeObserver(in.Mode == query.SolveMode && in.GuideQuestionID == "", func() bool { return canNarrate }, emit))
	scope := state.Scope{Namespace: "teacher", SubjectID: t.StudentID, ConversationID: record.ConversationID}
	result := map[string]string{"source_message_id": record.ID, "turn_id": record.TurnID, "question_id": in.GuideQuestionID, "assistant_message_id": state.MessageID(scope, record.TurnID, "assistant")}
	if in.GuideQuestionID == "" {
		saved, err := s.Teaching.Repo.Find(ctx, scope, record.TurnID)
		if errors.Is(err, state.ErrNotFound) {
			if in.Mode == query.GuideMode {
				ctx = solve.WithOpeningPublisher(ctx, func(c context.Context, text string) (string, error) {
					return emit("guide_question", map[string]string{"text": text})
				})
			}
			_, runErr := s.Agentic.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: scope, TurnID: record.TurnID}, InputRef: record.ID, BindingRef: record.ID}, Query: record.Query}, agentic.SinkFunc(func(c context.Context, p agentic.Packet) error {
				if p.Type == "draft" && in.Mode == query.SolveMode {
					if data, ok := p.Data.(map[string]any); ok {
						if text, ok := data["teacher_response"].(string); ok {
							_, e := emit("text", map[string]string{"text": text})
							return e
						}
					}
				}
				if p.Type == "progress" {
					if p.Stage == "execution_node" {
						if data, ok := p.Data.(map[string]string); ok {
							_, e := emit("execution_node", data)
							return e
						}
					}
					_, e := emit("progress", map[string]string{"stage": p.Stage, "text": p.Text})
					return e
				}
				return nil
			}))
			saved, err = s.Teaching.Repo.Find(ctx, scope, record.TurnID)
			if err != nil && errors.Is(runErr, photo.ErrAmbiguousText) {
				result["needs_input"] = "true"
				return result, errors.New("图片题干识别存在断裂或矛盾，请核对后用文字重新发题。原图和作答已保留。")
			}
			if err != nil {
				if errors.Is(runErr, state.ErrBudget) {
					return result, fmt.Errorf("%w: 正在整理对话，完成后将自动继续", state.ErrBudget)
				}
				log.Printf("background reference failed task=%s error=%v", t.ID, runErr)
				if in.RadarSourceID != "" {
					return nil, errors.New("新题生成或严格校验尚未完成，未展示候选题目。请重新尝试。")
				}
				return nil, errors.New("参考解答暂未准备完成，原题和作答已保存，可以重试")
			}
		} else if err != nil {
			return nil, errors.New("暂时无法核对参考解答，请重试")
		}
		// Old synchronous submissions may already have a receipt but no durable
		// opening event. Supply a safe restart prompt rather than exposing answers.
		if current, e := s.Tasks.Repo.Get(ctx, t.StudentID, t.ID); in.Mode == query.GuideMode && e == nil && current.Opening == "" {
			if _, e = emit("guide_question", map[string]string{"text": "先想一想：题目给出了哪些条件，你准备先求什么？"}); e != nil {
				return nil, e
			}
		}
		if in.Mode == query.SolveMode {
			result["text"] = saved.View.Solution.TeacherResponse
			if result["text"] == "" {
				result["text"] = saved.View.Solution.Analysis
				for _, step := range saved.View.Solution.Steps {
					result["text"] += "\n\n" + step.Content
				}
			}
		}
		if _, e := emit("execution_node", map[string]string{"id": "solution-saved", "kind": "storage", "name": "解答已正式保存", "status": "completed"}); e != nil {
			return nil, e
		}
		if s.StudentMemory != nil {
			if _, e := emit("execution_node", map[string]string{"id": "memory-extraction", "kind": "memory", "name": "长期记忆待后台提取（按触发条件处理）", "status": "pending"}); e != nil {
				return nil, e
			}
		}
		result["question_id"] = saved.View.QuestionID
		result["solution_id"] = saved.View.SolutionID
		result["stem"] = saved.View.Question.Stem
		result["question_type"] = saved.View.Question.Type
		result["created_at"] = saved.View.CreatedAt.Format(time.RFC3339Nano)
		for _, kp := range saved.View.Solution.KnowledgePoints {
			if kp.IsPrimary {
				result["knowledge_point"] = kp.Code
			}
		}

		return result, nil
	}
	var text string
	turn, runErr := s.Agentic.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: scope, TurnID: record.TurnID}, InputRef: record.ID, BindingRef: record.ID}, Query: record.Query}, agentic.SinkFunc(func(c context.Context, p agentic.Packet) error {
		if p.Type == "progress" {
			if p.Stage == "execution_node" {
				if data, ok := p.Data.(map[string]string); ok {
					_, e := emit("execution_node", data)
					return e
				}
			}
			_, e := emit("progress", map[string]string{"stage": p.Stage, "text": p.Text})
			return e
		}
		if p.Type == "text" {
			text = p.Text
			_, e := emit("text", map[string]string{"text": p.Text})
			return e
		}
		return nil
	}))
	if runErr != nil || turn == nil || turn.Result.Status != "succeeded" {
		log.Printf("background tutoring failed task=%s error=%v", t.ID, runErr)
		return nil, errors.New("教师反馈暂未完成，作答已保存，可以重试")
	}
	if text == "" && s.Teaching.Store != nil {
		sealed, e := s.Teaching.Store.GetTurn(ctx, scope, record.TurnID)
		if e != nil {
			return nil, errors.New("教师回复已完成，正在恢复历史，请重试核对")
		}
		for _, m := range sealed.Messages {
			if m != nil && m.Role == schema.Assistant && len(m.ToolCalls) == 0 && m.Content != "" {
				if text != "" {
					text += "\n\n"
				}
				text += m.Content
			}
		}
	}
	if text == "" {
		return nil, errors.New("未取得完整教师反馈，作答已保留，请重试")
	}
	result["text"] = text
	return result, nil
}

// Each observer instance follows one provider call, not a post-generation animation.
func teachingNarrativeObserver(solution bool, canNarrate func() bool, emit func(string, map[string]string) (string, error)) modelstream.Observer {
	return func() func(*schema.Message) error {
		type partial struct {
			name string
			args strings.Builder
		}
		calls := map[int]*partial{}
		var plain strings.Builder
		lastText := ""
		last := time.Time{}
		return func(chunk *schema.Message) error {
			text := ""
			if canNarrate() && chunk.Content != "" {
				plain.WriteString(chunk.Content)
				text = plain.String()
			}
			for _, call := range chunk.ToolCalls {
				i := 0
				if call.Index != nil {
					i = *call.Index
				}
				p := calls[i]
				if p == nil {
					p = &partial{}
					calls[i] = p
				}
				p.name += call.Function.Name
				p.args.WriteString(call.Function.Arguments)
				if solution && p.name == "submit_solution" {
					if v := solve.Preview(p.args.String()); v != nil {
						text, _ = v["teacher_response"].(string)
					}
				}
			}
			if text == "" || text == lastText || time.Since(last) < 100*time.Millisecond {
				return nil
			}
			lastText = text
			last = time.Now()
			_, err := emit("stream_text", map[string]string{"text": text, "provisional": "true"})
			return err
		}
	}
}

func (s Server) teachingTaskStream(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "后台任务未配置")
		return
	}
	id := r.PathValue("task")
	after, parseErr := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "" {
		parseErr = nil
	}
	if parseErr != nil || after < 0 || !query.ValidID(id) {
		failure(w, 400, "任务参数无效")
		return
	}
	if _, err := s.Tasks.Repo.Get(r.Context(), s.StudentID, id); err != nil {
		taskError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		failure(w, 500, "不支持流式连接")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	lastStatus := ""
	lastSend := time.Time{}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := s.Tasks.Repo.Get(r.Context(), s.StudentID, id)
		if err != nil {
			return
		}
		events, err := s.Tasks.Repo.Events(r.Context(), s.StudentID, id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			if event.ID > after {
				after = event.ID
			}
		}
		if len(events) > 0 || task.Status != lastStatus || time.Since(lastSend) > 10*time.Second {
			payload, _ := json.Marshal(map[string]any{"task": task, "events": events})
			if _, err = fmt.Fprintf(w, "event: task\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
			lastStatus = task.Status
			lastSend = time.Now()
		}
		if (task.Status == "READY" || task.Status == "FAILED" || task.Status == "CANCELLED") && len(events) < 200 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
