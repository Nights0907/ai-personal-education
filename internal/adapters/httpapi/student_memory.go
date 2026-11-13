package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mem "github.com/musi-ai/education/internal/modules/student/memory"
)

func (s Server) requireStudentMemory(w http.ResponseWriter) bool {
	if s.StudentMemory == nil {
		failure(w, 503, "学生画像与长期记忆尚未配置")
		return false
	}
	return true
}
func (s Server) studentProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	p, err := s.StudentMemory.Repo.Profile(ctx, s.StudentID)
	if err != nil {
		failure(w, 503, "暂时无法读取学生画像")
		return
	}
	writeJSON(w, 200, p)
}
func (s Server) saveStudentProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		failure(w, 415, "请使用JSON保存画像")
		return
	}
	var in struct {
		Revision int64            `json:"revision"`
		Basic    mem.BasicProfile `json:"basic"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	var tail any
	if d.Decode(&in) != nil || d.Decode(&tail) != io.EOF || in.Revision < 0 || !validBasic(in.Basic) {
		failure(w, 400, "画像格式或字段长度不正确")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	p, err := s.StudentMemory.Repo.SaveBasic(ctx, s.StudentID, in.Basic, in.Revision)
	if errors.Is(err, mem.ErrConflict) {
		failure(w, 409, "画像已更新，请重新读取后保存")
		return
	}
	if err != nil {
		failure(w, 503, "暂时无法保存画像")
		return
	}
	writeJSON(w, 200, p)
}
func validBasic(b mem.BasicProfile) bool {
	if utf8.RuneCountInString(b.Name) > 50 || utf8.RuneCountInString(b.Grade) > 30 || utf8.RuneCountInString(b.ExplanationPreference) > 300 {
		return false
	}
	for _, list := range [][]string{b.LearningGoals} {
		if len(list) > 30 {
			return false
		}
		for _, v := range list {
			if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 100 {
				return false
			}
		}
	}
	return true
}
func (s Server) studentIndexes(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	indexes, err := s.StudentMemory.Documents.Indexes(ctx, s.StudentID, 120)
	if err != nil {
		failure(w, 503, "暂时无法读取记忆索引")
		return
	}
	writeJSON(w, 200, map[string]any{"items": indexes})
}
func (s Server) studentDocument(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	offset, limit := 0, 3
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			failure(w, 400, "分页格式不正确")
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			failure(w, 400, "分页格式不正确")
			return
		}
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	b, err := s.StudentMemory.Read(ctx, s.StudentID, r.URL.Query().Get("path"), offset, limit)
	if errors.Is(err, mem.ErrNotFound) {
		failure(w, 404, "记忆文档不存在")
		return
	}
	if err != nil {
		failure(w, 400, "无法读取指定文档，请检查路径、分页或存储状态")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
func (s Server) studentMemoryStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	status, err := s.StudentMemory.Repo.Status(ctx, s.StudentID)
	if err != nil {
		failure(w, 503, "暂时无法读取提取进度")
		return
	}
	writeJSON(w, 200, status)
}
func (s Server) studentRecall(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	plan, err := s.StudentMemory.RecallPlan(ctx, s.StudentID, time.Now())
	if err != nil {
		failure(w, 503, "暂时无法读取记忆状态")
		return
	}
	writeJSON(w, 200, plan)
}
func (s Server) studentRecallPlan(w http.ResponseWriter, r *http.Request) {
	if s.RecallStatus == nil {
		failure(w, 503, "防遗忘计划尚未配置")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	writeJSON(w, 200, s.RecallStatus.Status(ctx, s.StudentID))
}

// studentMemoryOverview reads only the server-bound student's stored documents.
func (s Server) studentMemoryOverview(w http.ResponseWriter, r *http.Request) {
	if !s.requireStudentMemory(w) {
		return
	}
	offset, limit := 0, 20
	var err error
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			failure(w, 400, "分页不正确")
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 100 {
			failure(w, 400, "每页应为1至100条")
			return
		}
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	profile, err := s.StudentMemory.Repo.Profile(ctx, s.StudentID)
	if err != nil {
		failure(w, 503, "暂时无法读取基础画像")
		return
	}
	indexes, err := s.StudentMemory.Documents.Indexes(ctx, s.StudentID, 120)
	if err != nil {
		failure(w, 503, "暂时无法读取记忆索引")
		return
	}
	month := r.URL.Query().Get("month")
	if month == "" && len(indexes) > 0 {
		month = indexes[0].Month
	}
	result := map[string]any{"student_id": s.StudentID, "l0": profile, "l1": indexes, "month": month, "l2": nil, "l3": []mem.OriginalTurn{}, "offset": offset, "limit": limit, "total": 0, "has_more": false, "indexes_limit": 120}
	if month != "" {
		if _, err = mem.MonthlyPaths(s.StudentID, month); err != nil {
			failure(w, 400, "月份格式应为YYYY-MM")
			return
		}
		doc, e := s.StudentMemory.Documents.Load(ctx, s.StudentID, month)
		if errors.Is(e, mem.ErrNotFound) {
			failure(w, 404, "该月记忆不存在")
			return
		}
		if e != nil {
			failure(w, 503, "暂时无法读取月度记忆")
			return
		}
		turns := doc.Raw.Content.Turns
		total := len(turns)
		start := min(offset, total)
		end := start + min(limit, total-start)
		result["l2"] = doc.Summary
		numbered := append([]mem.OriginalTurn{}, turns[start:end]...)
		counts := map[string]int{}
		for i, turn := range turns[:end] {
			counts[turn.Source.ConversationID]++
			if i >= start {
				numbered[i-start].ConversationTurn = counts[turn.Source.ConversationID]
			}
		}
		result["l3"] = numbered
		result["total"] = total
		result["has_more"] = end < total
	}
	writeJSON(w, 200, result)
}

func (s Server) claimRecallNotification(w http.ResponseWriter, r *http.Request) {
	if s.RecallStatus == nil {
		failure(w, 503, "复习通知尚未配置")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		failure(w, 400, "通知编号无效")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	conversation, err := s.RecallStatus.ClaimNotification(ctx, s.StudentID, id)
	if err != nil {
		failure(w, 409, "通知不存在、已过期或暂时无法领取")
		return
	}
	writeJSON(w, 200, map[string]string{"conversation_id": conversation})
}
