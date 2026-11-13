package httpapi

import (
	"encoding/json"
	"github.com/musi-ai/education/internal/query"
	"io"
	"net/http"
	"strings"
)

func (s Server) createConversation(w http.ResponseWriter, r *http.Request) {
	creator, ok := s.Repo.(query.ConversationCreator)
	if !ok {
		failure(w, 503, "创建会话尚未配置")
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		failure(w, 415, "请使用 JSON 创建会话")
		return
	}
	var input struct {
		ClientRequestID string `json:"client_request_id"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		failure(w, 400, "请输入有效的会话创建请求编号")
		return
	}
	var tail any
	if d.Decode(&tail) != io.EOF || !query.ValidID(input.ClientRequestID) {
		failure(w, 400, "请输入有效的会话创建请求编号")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	out, err := creator.CreateConversation(ctx, s.StudentID, input.ClientRequestID)
	if err != nil {
		repoError(w, err)
		return
	}
	writeJSON(w, 200, out)
	if s.StudentMemory != nil {
		s.StudentMemory.Notify()
	}
}
