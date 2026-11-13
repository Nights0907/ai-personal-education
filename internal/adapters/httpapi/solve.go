package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/modelstream"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/query"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (s Server) solveQuestion(w http.ResponseWriter, r *http.Request) {
	pipeline := s.Agentic
	if pipeline == nil {
		pipeline = s.Solver
	} // compatibility for older API integrations
	if pipeline == nil || s.Teaching == nil {
		failure(w, 503, "解题服务尚未配置模型")
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		failure(w, 415, "请使用 JSON 提交文字题目")
		return
	}
	var input query.Input
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		failure(w, 400, "题目格式不正确，请提交单题文字或已上传图片编号")
		return
	}
	var tail any
	input.Normalize()
	if d.Decode(&tail) != io.EOF || input.Validate() != nil {
		failure(w, 400, "请输入完整文字题目及有效请求编号")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(200 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 185*time.Second)
	defer cancel()
	if !s.checkImage(w, r, input.ImageAssetID) {
		return
	}
	record, _, e := s.Repo.Create(ctx, s.StudentID, input)
	if e != nil {
		repoError(w, e)
		return
	}
	scope := state.Scope{Namespace: "teacher", SubjectID: s.StudentID, ConversationID: record.ConversationID}
	saved, e := s.Teaching.Repo.Find(ctx, scope, record.TurnID)
	if e != nil && !errors.Is(e, state.ErrNotFound) {
		failure(w, 503, "暂时无法核对已保存的解答，请使用相同请求重试")
		return
	}
	var stream *solveStream
	sink := agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil })
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		var stopStream func()
		stream, ctx, stopStream = startSolveStream(w, ctx, record)
		defer stopStream()
		if input.IsGuide() {
			ctx = modelstream.WithObserver(ctx, func() func(*schema.Message) error { return nil })
		} else {
			ctx = modelstream.WithObserver(ctx, stream.observer())
			sink = stream.packet
		}
	}
	if e == nil {
		if stream != nil {
			if input.IsGuide() {
				stream.guideStart(saved.View)
			} else {
				stream.result(saved.View)
			}
		} else {
			if input.IsGuide() {
				writeJSON(w, 200, map[string]any{"committed": true, "question_id": saved.View.QuestionID, "conversation_id": record.ConversationID, "source_message_id": record.ID, "mode": "guide"})
			} else {
				writeJSON(w, 200, saved.View)
			}
		}
		return
	}
	diagnostics := solveDiagnostics{}
	output := sink
	sink = func(c context.Context, p agentic.Packet) error { diagnostics.observe(p); return output(c, p) }
	turn, runErr := pipeline.Run(ctx, agentic.Request{Query: record.Query, Input: state.Input{Identity: state.Identity{Scope: scope, TurnID: record.TurnID}, InputRef: record.ID, BindingRef: record.ID}}, sink)
	// A committed receipt wins even if history projection failed or the caller
	// disconnected. Do not send the same task back through model execution.
	check, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	saved, e = s.Teaching.Repo.Find(check, scope, record.TurnID)
	if e == nil {
		if stream != nil {
			if input.IsGuide() {
				stream.guideStart(saved.View)
			} else {
				stream.result(saved.View)
			}
		} else {
			if input.IsGuide() {
				writeJSON(w, 200, map[string]any{"committed": true, "question_id": saved.View.QuestionID, "conversation_id": record.ConversationID, "source_message_id": record.ID, "mode": "guide"})
			} else {
				writeJSON(w, 200, saved.View)
			}
		}
		return
	}
	log.Printf("solve failed: turn=%s stages=%v validation_passed=%v error=%v", record.TurnID, diagnostics.Stages, diagnostics.Passed, runErr)
	status, code, message := solveFailure(record.ImageAssetID != "", turn, runErr)
	if errors.Is(runErr, state.ErrIncomplete) {
		code, message = diagnostics.failure()
	}
	if stream != nil {
		stream.failure(code, message)
	} else {
		failure(w, status, message)
	}
}
func solveFailure(image bool, turn *agentic.Turn, err error) (int, string, string) {
	if errors.Is(err, state.ErrConflict) {
		return 409, "TURN_CONFLICT", "题目正在处理或状态已变化，请稍后使用相同请求核对结果。"
	}
	code := "SOLUTION_NOT_COMMITTED"
	if turn != nil && turn.Result.ErrorCode != "" {
		code = turn.Result.ErrorCode
	}
	if code == "CONTEXT_BUDGET_EXCEEDED" {
		return 503, code, "本次对话整理暂未完成，原题已保留。可点击重试；若仍失败，请新建会话继续。"
	}
	if errors.Is(err, photo.ErrAmbiguousText) {
		return 422, "OCR_NEEDS_CONFIRMATION", "图片题干识别存在断裂或矛盾，请核对后改用文字重新发题；原图已保留。"
	}
	if errors.Is(err, photo.ErrUnavailable) {
		return 503, "OCR_UNAVAILABLE", "图片识别未完成，原图已保留，请稍后重试。"
	}
	if errors.Is(err, photo.ErrBusy) {
		return 409, "OCR_PROCESSING", "图片仍在识别，请稍后使用相同请求重试。"
	}
	if errors.Is(err, context.DeadlineExceeded) || code == "DEADLINE_EXCEEDED" {
		return 503, "SOLVE_TIMEOUT", "处理超时，原题已保留；重试会先核对已保存的解答。"
	}
	if image {
		return 503, code, "未取得已校验的完整解答。请先核对上方OCR原文；公式识别不完整时，请上传更清晰的完整题目或改用文字输入。原图已保留。"
	}
	return 503, code, "未取得已校验的完整解答，原题已保留。请参考上方说明或校验反馈后重试。"
}
func (s Server) questions(w http.ResponseWriter, r *http.Request) {
	if s.Teaching == nil {
		failure(w, 503, "题目查询未配置")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	items, e := s.Teaching.Repo.List(ctx, s.StudentID, 100)
	if e != nil {
		failure(w, 503, "暂时无法读取题目")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": 100})
}
func (s Server) question(w http.ResponseWriter, r *http.Request) {
	if s.Teaching == nil {
		failure(w, 503, "题目查询未配置")
		return
	}
	id := r.PathValue("question")
	if !query.ValidID(id) {
		failure(w, 400, "题目编号无效")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	v, e := s.Teaching.Repo.Get(ctx, s.StudentID, id)
	if errors.Is(e, state.ErrNotFound) {
		failure(w, 404, "题目不存在")
		return
	}
	if e != nil {
		failure(w, 503, "暂时无法读取题目")
		return
	}
	writeJSON(w, 200, v)
}

// Only stages and boolean outcomes are recorded; never model reasoning or drafts.
type solveDiagnostics struct {
	Stages                        []string
	Validated, Passed, Committing bool
}

func (d *solveDiagnostics) observe(p agentic.Packet) {
	if p.Type == "progress" && p.Stage != "" {
		if len(d.Stages) == 0 || d.Stages[len(d.Stages)-1] != p.Stage {
			d.Stages = append(d.Stages, p.Stage)
		}
		if p.Stage == "validating" {
			d.Validated = true
		}
		if p.Stage == "saving" {
			d.Committing = true
		}
	}
	if p.Type == "validation" {
		var v struct {
			Passed bool `json:"passed"`
		}
		raw, _ := json.Marshal(p.Data)
		if json.Unmarshal(raw, &v) == nil {
			d.Validated = true
			d.Passed = v.Passed
		}
	}
}
func (d solveDiagnostics) failure() (string, string) {
	if !d.Validated {
		return "MODEL_STOPPED_BEFORE_VALIDATION", "模型在提交校验前结束了回复，尚未保存解答。原题已保留，可重试。"
	}
	if !d.Passed {
		return "SOLUTION_VALIDATION_FAILED", "解答未通过校验，模型没有完成修订。原题已保留，请查看校验反馈后重试。"
	}
	if !d.Committing {
		return "MODEL_STOPPED_BEFORE_COMMIT", "解答已通过校验，但模型没有调用保存工具。原题已保留，可重试。"
	}
	return "SOLUTION_COMMIT_FAILED", "模型已尝试保存，但未取得提交回执。重试会先核对已有结果，避免重复保存。"
}
