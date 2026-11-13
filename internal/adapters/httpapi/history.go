package httpapi

import (
	"errors"
	"github.com/musi-ai/education/internal/query"
	"net/http"
	"strconv"
)

func (s Server) history(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.Repo.(query.HistoryReader)
	if !ok {
		failure(w, 503, "完整消息查询尚未配置")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			failure(w, 400, "limit 必须为 1 至 100")
			return
		}
		limit = n
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	out, e := reader.Inspect(ctx, s.StudentID, r.PathValue("conversation"), r.URL.Query().Get("cursor"), limit)
	if errors.Is(e, query.ErrConflict) {
		failure(w, 409, "会话已更新，请从第一页重新查询")
		return
	}
	if e != nil {
		repoError(w, e)
		return
	}
	if r.URL.Query().Get("execution") == "1" && s.Tasks != nil {
		traces := []map[string]any{}
		tasks, err := s.Tasks.Repo.List(ctx, s.StudentID, r.PathValue("conversation"))
		if err != nil {
			failure(w, 503, "暂时无法读取执行记录")
			return
		}
		for _, task := range tasks {
			events, e := s.Tasks.Repo.Events(ctx, s.StudentID, task.ID, 0)
			if e != nil {
				failure(w, 503, "暂时无法读取执行事件")
				return
			}
			traces = append(traces, map[string]any{"task_id": task.ID, "turn_id": task.Result["turn_id"], "attempts": task.Attempts, "status": task.Status, "error": task.Error, "created_at": task.CreatedAt, "events": events, "events_may_have_more": len(events) >= 200})
		}
		writeJSON(w, 200, struct {
			query.ConversationHistory
			Traces []map[string]any `json:"execution_traces"`
		}{out, traces})
		return
	}
	writeJSON(w, 200, out)
}
