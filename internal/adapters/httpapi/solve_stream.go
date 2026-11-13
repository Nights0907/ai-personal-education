package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/modelstream"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
	"net/http"
	"strings"
	"sync"
	"time"
)

type solveStream struct {
	w            http.ResponseWriter
	mu           sync.Mutex
	seq          int
	err          error
	lastDraft    string
	lastProgress string
	cancel       context.CancelFunc
}

func startSolveStream(w http.ResponseWriter, ctx context.Context, r query.Record) (*solveStream, context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	s := &solveStream{w: w, cancel: cancel}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	_ = s.send("accepted", map[string]any{"conversation_id": r.ConversationID, "turn_id": r.TurnID, "source_message_id": r.ID})
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_ = s.send("heartbeat", map[string]bool{"alive": true})
			}
		}
	}()
	return s, ctx, func() { cancel(); <-done }
}
func (s *solveStream) send(name string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	raw, e := json.Marshal(data)
	if e != nil {
		return e
	}
	if name == "draft_reset" {
		s.lastDraft = ""
	}
	if packet, ok := data.(agentic.Packet); ok {
		if name == "draft" {
			snapshot, err := json.Marshal(packet.Data)
			if err != nil {
				return err
			}
			if string(snapshot) == s.lastDraft {
				return nil
			}
			s.lastDraft = string(snapshot)
		}
		if name == "progress" {
			value := packet.Stage + "\n" + packet.Text
			if value == s.lastProgress {
				return nil
			}
			s.lastProgress = value
		}
	}
	s.seq++
	_ = http.NewResponseController(s.w).SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, e = fmt.Fprintf(s.w, "id: %d\nevent: %s\ndata: %s\n\n", s.seq, name, raw)
	if e == nil {
		e = http.NewResponseController(s.w).Flush()
	}
	if e != nil {
		s.err = e
		s.cancel()
	}
	return e
}
func (s *solveStream) packet(_ context.Context, p agentic.Packet) error {
	switch p.Type {
	case "progress", "draft", "ocr", "validation":
		return s.send(p.Type, p)
	}
	return nil
}
func (s *solveStream) result(v solve.View) {
	_ = s.send("solution", v)
	_ = s.send("done", map[string]string{"status": "succeeded", "question_id": v.QuestionID})
}

// guideStart signals that the solution has been committed but withheld: guided
// tutoring begins instead of revealing the answer. The payload carries the
// question identity and stem (no answer/analysis/steps) so the client can enter
// the guide conversation.
func (s *solveStream) guideStart(v solve.View) {
	_ = s.send("guide_start", map[string]any{
		"question_id":      v.QuestionID,
		"solution_id":      v.SolutionID,
		"conversation_id":  v.ConversationID,
		"stem":             v.Question.Stem,
		"type":             v.Question.Type,
		"knowledge_points": v.Solution.KnowledgePoints,
	})
	_ = s.send("done", map[string]string{"status": "guided", "question_id": v.QuestionID})
}
func (s *solveStream) failure(code, text string) {
	_ = s.send("error", map[string]string{"error_code": code, "error": text})
	_ = s.send("done", map[string]string{"status": "failed"})
}

// Actual provider chunks are observed while the synchronous Eino loop keeps a
// complete canonical message. Only public text and allowlisted candidate fields
// reach SSE; reasoning, raw arguments and reviewer responses never do.
func (s *solveStream) observer() modelstream.Observer {
	return func() func(*schema.Message) error {
		type partial struct {
			name string
			args strings.Builder
		}
		calls := map[int]*partial{}
		last := time.Time{}
		started := false
		return func(chunk *schema.Message) error {
			if chunk.Content != "" {
				if e := s.send("text_delta", map[string]string{"text": chunk.Content}); e != nil {
					return e
				}
			}
			for _, call := range chunk.ToolCalls {
				index := 0
				if call.Index != nil {
					index = *call.Index
				}
				p := calls[index]
				if p == nil {
					p = &partial{}
					calls[index] = p
				}
				if call.Function.Name != "" {
					p.name += call.Function.Name
				}
				p.args.WriteString(call.Function.Arguments)
				if (p.name != "validate_solution" && p.name != "submit_solution") || time.Since(last) < 120*time.Millisecond {
					continue
				}
				v := solve.Preview(p.args.String())
				if v == nil {
					continue
				}
				if !started {
					if e := s.send("draft_reset", map[string]bool{"provisional": true}); e != nil {
						return e
					}
					started = true
				}
				last = time.Now()
				if e := s.send("draft", agentic.Packet{Type: "draft", Data: v}); e != nil {
					return e
				}
			}
			return nil
		}
	}
}
