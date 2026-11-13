// Package httpapi exposes local query submission and static web assets.
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/musi-ai/education/internal/application/agentic"
	studentmemory "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/recallplan"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/modules/teaching/tasks"
	"github.com/musi-ai/education/internal/query"
)

func contextTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

type Server struct {
	Accounts      *Accounts
	Tasks         *tasks.Service
	StudentMemory *studentmemory.Service
	Photos        *photo.Service
	Solver        *agentic.Pipeline
	Teaching      *solve.Service
	Agentic       *agentic.Pipeline
	Repo          query.Repository
	RecallStatus  *recallplan.Service
	StudentID     string
	Assets        fs.FS
}

func (s Server) Handler() http.Handler {
	if s.Accounts != nil {
		return s.accountMiddleware()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/model-settings", s.modelSettings)
	mux.HandleFunc("PUT /api/v1/model-settings", s.modelSettings)
	mux.HandleFunc("POST /api/v1/radar", s.createRadar)
	mux.HandleFunc("GET /api/v1/radar", s.radarHistory)
	mux.HandleFunc("GET /api/v1/radar/{question}", s.publicRadarQuestion)
	mux.HandleFunc("POST /api/v1/radar/{question}/reveal", s.revealRadar)
	mux.HandleFunc("POST /api/v1/radar/{question}/attempts", s.attemptRadar)
	mux.HandleFunc("GET /api/v1/teacher/system-prompt", s.editablePrompt)
	mux.HandleFunc("PUT /api/v1/teacher/system-prompt", s.editablePrompt)
	mux.HandleFunc("POST /api/v1/teaching/tasks", s.createTeachingTask)
	mux.HandleFunc("GET /api/v1/teaching/tasks", s.teachingTasks)
	mux.HandleFunc("GET /api/v1/teaching/tasks/{task}", s.teachingTask)
	mux.HandleFunc("GET /api/v1/teaching/tasks/{task}/stream", s.teachingTaskStream)
	mux.HandleFunc("POST /api/v1/teaching/tasks/{task}/retry", s.retryTeachingTask)
	mux.HandleFunc("GET /api/v1/student/profile", s.studentProfile)
	mux.HandleFunc("PUT /api/v1/student/profile", s.saveStudentProfile)
	mux.HandleFunc("GET /api/v1/student/memory/overview", s.studentMemoryOverview)
	mux.HandleFunc("GET /api/v1/student/memory/indexes", s.studentIndexes)
	mux.HandleFunc("GET /api/v1/student/memory/document", s.studentDocument)
	mux.HandleFunc("GET /api/v1/student/memory/status", s.studentMemoryStatus)
	mux.HandleFunc("GET /api/v1/student/memory/recall", s.studentRecall)
	mux.HandleFunc("GET /api/v1/student/recall-plan", s.studentRecallPlan)
	mux.HandleFunc("POST /api/v1/student/recall-notifications/{id}/claim", s.claimRecallNotification)
	mux.HandleFunc("POST /api/v1/images", s.uploadImage)
	mux.HandleFunc("GET /api/v1/images/{image}/content", s.imageContent)
	mux.HandleFunc("GET /api/v1/images/{image}", s.imageMetadata)
	mux.HandleFunc("POST /api/v1/solve", s.solveQuestion)
	mux.HandleFunc("GET /api/v1/questions", s.questions)
	mux.HandleFunc("GET /api/v1/questions/{question}", s.question)
	mux.HandleFunc("POST /api/v1/conversations", s.createConversation)
	mux.HandleFunc("DELETE /api/v1/conversations/{conversation}", s.deleteConversation)
	mux.HandleFunc("POST /api/v1/agentic/chat", s.chat)
	mux.HandleFunc("GET /api/v1/conversations/{conversation}/messages", s.history)
	mux.HandleFunc("POST /api/v1/messages", s.create)
	mux.HandleFunc("GET /api/v1/messages", s.list)
	mux.HandleFunc("GET /api/v1/messages/{id}", s.get)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { failure(w, 404, "接口不存在") })
	assets := http.FileServer(http.FS(s.Assets))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			failure(w, 405, "不支持的请求方法")
			return
		}
		assets.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			failure(w, 403, "请通过本机地址访问")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				failure(w, 403, "不允许跨站请求")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func repoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, query.ErrInvalid):
		failure(w, 400, "请输入有效的文字题目，最多 20000 字")
	case errors.Is(err, query.ErrConflict):
		failure(w, 409, "该请求编号已用于其他内容，请重新提交")
	case errors.Is(err, query.ErrNotFound):
		failure(w, 404, "提交记录不存在")
	default:
		failure(w, 503, "暂时无法保存或读取记录，请稍后重试")
	}
}
func (s Server) create(w http.ResponseWriter, r *http.Request) {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		failure(w, 415, "请使用 JSON 提交文字题目")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
	if err != nil {
		failure(w, 413, "提交内容过大")
		return
	}
	if !utf8.Valid(body) {
		failure(w, 400, "请输入有效的 UTF-8 文字")
		return
	}
	var in query.Input
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		failure(w, 400, "提交格式不正确")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		failure(w, 400, "提交格式不正确")
		return
	}
	in.Normalize()
	if err = in.Validate(); err != nil {
		repoError(w, err)
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	if !s.checkImage(w, r, in.ImageAssetID) {
		return
	}
	record, created, err := s.Repo.Create(ctx, s.StudentID, in)
	if err != nil {
		repoError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", "/api/v1/messages/"+record.ID)
	writeJSON(w, status, record)
}
func (s Server) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !query.ValidID(id) {
		failure(w, 400, "记录编号不正确")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	record, err := s.Repo.Get(ctx, s.StudentID, id)
	if err != nil {
		repoError(w, err)
		return
	}
	writeJSON(w, 200, record)
}
func (s Server) list(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			failure(w, 400, "分页数量应为 1 至 100")
			return
		}
		limit = n
	}
	conversation := r.URL.Query().Get("conversation_id")
	if conversation != "" && !query.ValidID(conversation) {
		failure(w, 400, "会话编号不正确")
		return
	}
	var cursor *query.Cursor
	if raw := r.URL.Query().Get("before"); raw != "" {
		var c query.Cursor
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || json.Unmarshal(b, &c) != nil || c.CreatedAt.IsZero() || !query.ValidID(c.ID) {
			failure(w, 400, "分页游标不正确")
			return
		}
		cursor = &c
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	records, more, err := s.Repo.List(ctx, s.StudentID, conversation, cursor, limit)
	if err != nil {
		repoError(w, err)
		return
	}
	page := query.Page{Items: records}
	if more {
		last := records[len(records)-1]
		b, _ := json.Marshal(query.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	writeJSON(w, 200, page)
}
