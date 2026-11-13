package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
)

func (s Server) createRadar(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil || s.StudentMemory == nil || s.Teaching == nil || s.Teaching.Repo == nil {
		failure(w, 503, "题目雷达尚未配置")
		return
	}
	var body struct {
		ClientRequestID string `json:"client_request_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || !query.ValidID(body.ClientRequestID) {
		failure(w, 400, "请输入有效的出题请求编号")
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	// Reusing a client request must never create another generation or seed.
	if existing, err := s.Tasks.Repo.List(ctx, s.StudentID, ""); err == nil {
		for _, task := range existing {
			if task.Input.ClientRequestID == body.ClientRequestID && task.Input.RadarSourceID != "" {
				writeJSON(w, 200, map[string]any{"task": task, "source_question_id": task.Input.RadarSourceID})
				return
			}
		}
	}
	profile, err := s.StudentMemory.RecallPlan(ctx, s.StudentID, time.Now())
	if err != nil {
		failure(w, 503, "学习档案暂不可用")
		return
	}
	if len(profile.Items) == 0 {
		failure(w, 422, "还没有可用于个性化出题的学习证据，请先完成一道题。")
		return
	}
	items := append([]memory.RecallItem(nil), profile.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if memory.MasteryRank(a.Mastery) != memory.MasteryRank(b.Mastery) {
			return memory.MasteryRank(a.Mastery) < memory.MasteryRank(b.Mastery)
		}
		if a.Assisted != b.Assisted {
			return a.Assisted
		}
		if a.ErrorCount != b.ErrorCount {
			return a.ErrorCount > b.ErrorCount
		}
		return a.Retrievability < b.Retrievability
	})
	sum := sha256.Sum256([]byte(s.StudentID + ":" + body.ClientRequestID))
	seed := int(binary.BigEndian.Uint32(sum[:4])%999999999) + 1
	weakest := items[0]
	var source string
	for _, qid := range weakest.QuestionIDs {
		_, e := s.Teaching.Repo.Get(ctx, s.StudentID, qid)
		if e != nil {
			continue
		}
		if e == nil {
			source = qid
			break
		}
	}
	if source == "" {
		failure(w, 422, "当前最薄弱知识点是“"+weakest.Topic+"”，没有可读取的关联原题，请先完成该知识点的一道题。")
		return
	}
	creator, ok := s.Repo.(interface {
		CreateConversation(context.Context, string, string) (query.Conversation, error)
	})
	if !ok {
		failure(w, 503, "无法创建练习记录")
		return
	}
	conversation, e := creator.CreateConversation(ctx, s.StudentID, "radar-"+body.ClientRequestID)
	if e != nil {
		repoError(w, e)
		return
	}
	input := query.Input{Query: "题目雷达：基于绑定原题生成同知识点换数值新题。先加载question-variant与question-verification，严格校验后原样提交。", ConversationID: conversation.ConversationID, ClientRequestID: body.ClientRequestID, Mode: query.SolveMode, RadarSourceID: source, RadarSeed: seed}
	task, e := s.Tasks.Repo.Create(ctx, s.StudentID, input, "")
	if e != nil {
		taskError(w, e)
		return
	}
	s.Tasks.Notify()
	writeJSON(w, 202, map[string]any{"task": task, "source_question_id": source, "topic": weakest.Topic, "reason": "该知识点掌握证据较弱，优先检验能否独立迁移到新数值。"})
}

func (s Server) radarQuestion(w http.ResponseWriter, r *http.Request) (solve.View, bool) {
	if s.Teaching == nil || s.Teaching.Repo == nil {
		failure(w, 503, "题库暂不可用")
		return solve.View{}, false
	}
	view, err := s.Teaching.Repo.Get(r.Context(), s.StudentID, r.PathValue("question"))
	if err != nil {
		failure(w, 404, "题目不存在")
		return view, false
	}
	record, err := s.Repo.Get(r.Context(), s.StudentID, view.SourceMessageID)
	if err != nil || record.RadarSourceID == "" {
		failure(w, 404, "不是题目雷达练习")
		return view, false
	}
	_, e := s.Teaching.Repo.Get(r.Context(), s.StudentID, record.RadarSourceID)
	if e != nil {
		failure(w, 409, "原题校验依据暂不可用")
		return view, false
	}
	candidate := solve.Candidate{Question: view.Question, Solution: view.Solution}
	candidate.Solution.Steps = append([]solve.Step(nil), view.Solution.Steps...)
	for i := range candidate.Solution.Steps {
		candidate.Solution.Steps[i].StepID = ""
	}
	if s.Teaching.CheckRadar(r.Context(), s.StudentID, record.RadarSourceID, record.RadarSeed, candidate) != nil {
		failure(w, 409, "题目严格校验未通过，暂不展示")
		return view, false
	}
	return view, true
}
func (s Server) revealRadar(w http.ResponseWriter, r *http.Request) {
	view, ok := s.radarQuestion(w, r)
	if !ok {
		return
	}
	_, _, err := s.Repo.Create(r.Context(), s.StudentID, query.Input{Query: "已查看题目雷达参考答案；后续作答按提示后完成处理。", ConversationID: view.ConversationID, ClientRequestID: "radar-reveal-" + view.QuestionID, Mode: query.GuideMode, GuideQuestionID: view.QuestionID})
	if err != nil {
		repoError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"solution": view.Solution, "notice": "答案已展开，后续作答将标记为提示后完成。"})
}
func (s Server) attemptRadar(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		failure(w, 503, "教学任务未配置")
		return
	}
	view, ok := s.radarQuestion(w, r)
	if !ok {
		return
	}
	var body struct {
		Query           string `json:"query"`
		ClientRequestID string `json:"client_request_id"`
		ImageAssetID    string `json:"image_asset_id"`
		Guided          bool   `json:"guided"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 100<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		failure(w, 400, "作答格式无效")
		return
	}
	mode := query.ReviewMode
	if body.Guided {
		mode = query.GuideMode
	}
	if finder, yes := s.Repo.(interface {
		ConversationByRequest(context.Context, string, string) (string, error)
	}); yes {
		if _, e := finder.ConversationByRequest(r.Context(), s.StudentID, "radar-reveal-"+view.QuestionID); e == nil {
			mode = query.GuideMode
		}
	}
	input := query.Input{Query: body.Query, ConversationID: view.ConversationID, ClientRequestID: body.ClientRequestID, ImageAssetID: body.ImageAssetID, Mode: mode, GuideQuestionID: view.QuestionID}
	input.Normalize()
	if input.Validate() != nil {
		failure(w, 400, "请输入作答或上传图片")
		return
	}
	if !s.checkImage(w, r, input.ImageAssetID) {
		return
	}
	task, err := s.Tasks.Repo.Create(r.Context(), s.StudentID, input, "")
	if err != nil {
		taskError(w, err)
		return
	}
	s.Tasks.Notify()
	writeJSON(w, 202, task)
}

func (s Server) publicRadarQuestion(w http.ResponseWriter, r *http.Request) {
	view, ok := s.radarQuestion(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"question_id": view.QuestionID, "conversation_id": view.ConversationID, "question": view.Question, "verified": true, "checks": []string{"数值有效", "可解性", "独立验算", "答案步骤一致"}})
}

func (s Server) radarHistory(w http.ResponseWriter, r *http.Request) {
	if s.Teaching == nil || s.Teaching.Repo == nil {
		failure(w, 503, "题库暂不可用")
		return
	}
	views, e := s.Teaching.Repo.List(r.Context(), s.StudentID, 100)
	if e != nil {
		repoError(w, e)
		return
	}
	items := []map[string]any{}
	for _, v := range views {
		record, e := s.Repo.Get(r.Context(), s.StudentID, v.SourceMessageID)
		if e != nil || record.RadarSourceID == "" {
			continue
		}
		items = append(items, map[string]any{"question_id": v.QuestionID, "stem": v.Question.Stem, "created_at": v.CreatedAt})
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": 100})
}
