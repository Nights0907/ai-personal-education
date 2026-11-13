package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/query"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// chat is a new entry point. The existing raw-message endpoint stays independent.
func (s Server) chat(w http.ResponseWriter, r *http.Request) {
	if s.Agentic == nil {
		failure(w, 503, "新链路尚未配置模型，请设置百炼模型环境变量")
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		failure(w, 415, "请使用 JSON 提交文字问题")
		return
	}
	var in query.Input
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if readErr != nil || !utf8.Valid(body) {
		failure(w, 400, "请输入有效的 UTF-8 文字")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&in); e != nil {
		failure(w, 400, "请求格式不正确")
		return
	}
	var trailing any
	in.Normalize()
	if decoder.Decode(&trailing) != io.EOF || in.Validate() != nil {
		failure(w, 400, "请输入有效问题与请求编号")
		return
	}
	if err := s.resumeTeaching(r.Context(), &in); err != nil {
		repoError(w, err)
		return
	}
	// One public entry and one agent; this adapter only selects the response contract.
	if in.GuideQuestionID == "" && in.Mode != query.ReviewMode && s.Teaching != nil {
		raw, _ := json.Marshal(in)
		next := r.Clone(r.Context())
		next.Body = io.NopCloser(bytes.NewReader(raw))
		s.solveQuestion(w, next)
		return
	}
	record, _, e := s.Repo.Create(r.Context(), s.StudentID, in)
	if e != nil {
		repoError(w, e)
		return
	}
	if record.TurnID == "" {
		failure(w, 500, "消息未绑定有效轮次")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	sink := agentic.SinkFunc(func(ctx context.Context, p agentic.Packet) error {
		b, e := json.Marshal(p)
		if e != nil {
			return e
		}
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", p.Type, b); e != nil {
			return e
		}
		return http.NewResponseController(w).Flush()
	})
	_ = sink.Send(r.Context(), agentic.Packet{Type: "accepted", MessageID: record.ID, TurnID: record.TurnID})
	pipeline := s.Agentic

	if pipeline == nil {
		failure(w, 503, "新链路尚未配置模型，请设置百炼模型环境变量")
		return
	}
	_, e = pipeline.Run(r.Context(), agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: s.StudentID, ConversationID: record.ConversationID}, TurnID: record.TurnID}, InputRef: record.ID, BindingRef: record.ID}, Query: record.Query}, sink)
	if e != nil && r.Context().Err() == nil {
		log.Printf("agentic chat failed: %v", e)
		_ = sink.Send(r.Context(), agentic.Packet{Type: "error", BizName: "teacher", ErrorCode: "AGENTIC_REQUEST_FAILED"})
	}
}

// Recover the server-owned teaching task; clients need not remember a question ID.
func (s Server) resumeTeaching(ctx context.Context, in *query.Input) error {
	if in.RegenerateOf != "" {
		source, e := s.Repo.Get(ctx, s.StudentID, in.RegenerateOf)
		if e != nil {
			return e
		}
		if source.ConversationID != in.ConversationID || source.Query != in.Query || source.ImageAssetID != in.ImageAssetID {
			return query.ErrConflict
		}
		in.Mode = source.Mode
		in.GuideQuestionID = source.GuideQuestionID
		return nil
	}

	if in.Mode == query.SolveMode {
		return nil
	}
	records, _, err := s.Repo.List(ctx, s.StudentID, in.ConversationID, nil, 100)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.ClientRequestID == in.ClientRequestID {
			if in.Mode == "" {
				in.Mode = r.Mode
			}
			if in.GuideQuestionID == "" {
				in.GuideQuestionID = r.GuideQuestionID
			}
			return nil
		}
	}
	for _, r := range records {
		if r.Mode != query.GuideMode && r.Mode != query.ReviewMode {
			return nil
		}
		if in.GuideQuestionID != "" {
			return nil
		}
		if in.Mode != "" && in.Mode != r.Mode {
			return nil
		}
		in.Mode = r.Mode
		in.GuideQuestionID = r.GuideQuestionID
		if in.GuideQuestionID == "" && s.Teaching != nil {
			saved, e := s.Teaching.Repo.Find(ctx, state.Scope{Namespace: "teacher", SubjectID: s.StudentID, ConversationID: in.ConversationID}, r.TurnID)
			if e == nil {
				in.GuideQuestionID = saved.View.QuestionID
			}
		}
		return nil
	}
	return nil
}
