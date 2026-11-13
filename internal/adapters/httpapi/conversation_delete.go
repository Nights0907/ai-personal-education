package httpapi

import (
	"errors"
	"github.com/musi-ai/education/internal/query"
	"net/http"
)

func (s Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.Repo.(query.ConversationDeleter)
	if !ok {
		failure(w, 503, "会话删除尚未配置")
		return
	}
	id := r.PathValue("conversation")
	if !query.ValidID(id) {
		failure(w, 400, "会话编号不正确")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	if e := repo.DeleteConversation(ctx, s.StudentID, id); e != nil {
		if errors.Is(e, query.ErrConflict) {
			failure(w, 409, "会话正在处理，请等待回复完成后再删除。")
			return
		}
		repoError(w, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
