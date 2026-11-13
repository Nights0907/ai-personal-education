package teaching

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/photos"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"strings"
	"time"
)

//go:embed schema.sql
var ddl string

type Repository struct {
	DB     *sql.DB
	Photos *photo.Service
}

func New(db *sql.DB) *Repository { return &Repository{DB: db} }

func (r *Repository) resolveSource(ctx context.Context, tx *sql.Tx, student string, src query.Record) (query.Record, *photo.Result, error) {
	if src.ImageAssetID == "" {
		return src, nil, nil
	}
	if r.Photos == nil || r.Photos.OCR == nil {
		return src, nil, photo.ErrUnavailable
	}
	v, e := photos.Cached(ctx, tx, student, src.ImageAssetID, r.Photos.OCR.ConfigHash())
	if e != nil {
		return src, nil, e
	}
	if e := photo.ValidateQuestionText(v.Text); e != nil {
		return src, nil, e
	}
	src.Query = v.Text
	return src, &v, nil
}
func (r *Repository) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(ddl, ";") {
		if strings.TrimSpace(stmt) != "" {
			if _, e := r.DB.ExecContext(ctx, stmt); e != nil {
				return e
			}
		}
	}
	return r.ValidateSchema(ctx)
}
func (r *Repository) ValidateSchema(ctx context.Context) error {
	for _, table := range []string{"teaching_question", "teaching_solution", "teaching_solution_step", "teaching_solution_draft", "teaching_solution_receipt"} {
		rows, e := r.DB.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if e != nil {
			return e
		}
		rows.Close()
	}
	return nil
}
func js(v any) string { b, _ := json.Marshal(v); return string(b) }
func active(ctx context.Context, tx *sql.Tx, in state.Input) error {
	var raw []byte
	if e := tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", in.Identity.Scope.Key()).Scan(&raw); e != nil {
		return e
	}
	var c struct {
		Deleted bool
		Active  *state.Run
	}
	if e := json.Unmarshal(raw, &c); e != nil {
		return e
	}
	if c.Deleted || c.Active == nil || state.Hash(c.Active.Input) != state.Hash(in) || !in.Lease.Until.After(time.Now()) {
		return state.ErrConflict
	}
	return nil
}
func source(ctx context.Context, tx *sql.Tx, in state.Input) (query.Record, error) {
	var content, ext []byte
	r := query.Record{ID: in.InputRef, TurnID: in.Identity.TurnID, Input: query.Input{ConversationID: in.Identity.ConversationID}}
	e := tx.QueryRowContext(ctx, "SELECT content,ext FROM agent_message WHERE scope_key=? AND message_id=? AND turn_id=? AND role='user' AND status='RECEIVED'", in.Identity.Scope.Key(), in.InputRef, in.Identity.TurnID).Scan(&content, &ext)
	if e != nil {
		return r, e
	}
	var m struct {
		OriginalInput *schema.Message
		ImageAssetID  string
	}
	var block struct {
		Items []*schema.Message `json:"items"`
	}
	if e = json.Unmarshal(ext, &m); e != nil {
		return r, e
	}
	r.ImageAssetID = m.ImageAssetID
	if m.OriginalInput != nil {
		r.Query = m.OriginalInput.Content
	} else {
		if e = json.Unmarshal(content, &block); e != nil || len(block.Items) != 1 {
			return r, state.ErrProtocol
		}
		r.Query = block.Items[0].Content
	}
	return r, nil
}
func (r *Repository) SaveDraft(ctx context.Context, in state.Input, hash string, c solve.Candidate, review solve.Review) (string, error) {
	if e := solve.Validate(c); e != nil {
		return "", e
	}
	if !review.Passed || len(review.Issues) > 0 {
		return "", solve.ErrReview
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, in); e != nil {
		return "", e
	}
	src, e := source(ctx, tx, in)
	if e == nil {
		src, _, e = r.resolveSource(ctx, tx, in.Identity.SubjectID, src)
	}
	if e != nil || solve.SourceHash(src) != hash {
		return "", state.ErrConflict
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM teaching_solution_draft WHERE scope_key=? AND turn_id=? AND run_id=?", in.Identity.Scope.Key(), in.Identity.TurnID, in.Identity.RunID).Scan(&count); e != nil {
		return "", e
	}
	if count >= 3 {
		return "", fmt.Errorf("solve: draft revision limit")
	}
	id := idgen.New("d")
	_, e = tx.ExecContext(ctx, `INSERT INTO teaching_solution_draft(draft_id,scope_key,turn_id,run_id,lease_token,source_hash,payload,payload_hash,review_json,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 1 HOUR),UTC_TIMESTAMP(6))`, id, in.Identity.Scope.Key(), in.Identity.TurnID, in.Identity.RunID, in.Lease.Token, hash, js(c), state.Hash(c), js(review))
	if e != nil {
		return "", e
	}
	return id, tx.Commit()
}

type row interface{ Scan(...any) error }

func readSaved(row row) (v solve.Saved, err error) {
	var a, b, c []byte
	var status string
	err = row.Scan(&a, &b, &c, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return v, state.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	for _, p := range []struct {
		b []byte
		v any
	}{{a, &v.Input}, {b, &v.Receipt}, {c, &v.View}} {
		if err = json.Unmarshal(p.b, p.v); err != nil {
			return v, err
		}
	}
	v.View.HistoryStatus = status
	return v, nil
}

const savedColumns = "input_json,receipt_json,view_json,history_status"

func (r *Repository) Find(ctx context.Context, scope state.Scope, turn string) (solve.Saved, error) {
	if e := scope.Validate(); e != nil {
		return solve.Saved{}, e
	}
	return readSaved(r.DB.QueryRowContext(ctx, "SELECT "+savedColumns+" FROM teaching_solution_receipt WHERE scope_key=? AND turn_id=? AND student_id=?", scope.Key(), turn, scope.SubjectID))
}
func (r *Repository) Get(ctx context.Context, student, id string) (solve.View, error) {
	v, e := readSaved(r.DB.QueryRowContext(ctx, "SELECT "+savedColumns+" FROM teaching_solution_receipt WHERE student_id=? AND question_id=?", student, id))
	return v.View, e
}
func (r *Repository) List(ctx context.Context, student string, limit int) ([]solve.View, error) {
	if limit < 1 || limit > 100 {
		return nil, solve.ErrInvalid
	}
	rows, e := r.DB.QueryContext(ctx, "SELECT "+savedColumns+" FROM teaching_solution_receipt WHERE student_id=? AND scope_key IN (SELECT scope_key FROM agent_conversation WHERE status='ACTIVE') ORDER BY id DESC LIMIT ?", student, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []solve.View{}
	for rows.Next() {
		v, e := readSaved(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v.View)
	}
	return out, rows.Err()
}

func (r *Repository) Commit(ctx context.Context, in state.Input, draftID, modelRef string, call state.TerminalObservation, prefix []*schema.Message) (saved solve.Saved, err error) {
	if (call.ToolName != "finalize_solution" && call.ToolName != "submit_solution") || call.CallID == "" || call.ArgumentsHash != state.Hash(call.Arguments) || len(prefix) == 0 {
		return saved, state.ErrProtocol
	}
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return saved, err
	}
	defer tx.Rollback()
	var control []byte
	if err = tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", in.Identity.Scope.Key()).Scan(&control); err != nil {
		return saved, err
	}
	old, e := readSaved(tx.QueryRowContext(ctx, "SELECT "+savedColumns+" FROM teaching_solution_receipt WHERE scope_key=? AND turn_id=?", in.Identity.Scope.Key(), in.Identity.TurnID))
	if e == nil {
		return old, nil
	}
	if !errors.Is(e, state.ErrNotFound) {
		return saved, e
	}
	if err = active(ctx, tx, in); err != nil {
		return saved, err
	}
	var raw []byte
	var hash, sourceHash string
	var reviewJSON []byte
	err = tx.QueryRowContext(ctx, "SELECT payload,payload_hash,source_hash,review_json FROM teaching_solution_draft WHERE draft_id=? AND scope_key=? AND turn_id=? AND run_id=? AND lease_token=? AND expires_at>UTC_TIMESTAMP(6) FOR UPDATE", draftID, in.Identity.Scope.Key(), in.Identity.TurnID, in.Identity.RunID, in.Lease.Token).Scan(&raw, &hash, &sourceHash, &reviewJSON)
	if err != nil {
		return saved, state.ErrConflict
	}
	var c solve.Candidate
	var review solve.Review
	if json.Unmarshal(raw, &c) != nil || json.Unmarshal(reviewJSON, &review) != nil || solve.Validate(c) != nil || state.Hash(c) != hash || !review.Passed || len(review.Issues) > 0 {
		return saved, state.ErrConflict
	}
	src, e := source(ctx, tx, in)
	rawInput := src.Query
	var imageSource *photo.Result
	if e == nil {
		src, imageSource, e = r.resolveSource(ctx, tx, in.Identity.SubjectID, src)
	}
	if e != nil || solve.SourceHash(src) != sourceHash {
		return saved, state.ErrConflict
	}
	now := time.Now().UTC()
	v := solve.View{Committed: true, ResultID: idgen.New("r"), QuestionID: idgen.New("q"), SolutionID: idgen.New("s"), ConversationID: in.Identity.ConversationID, SourceMessageID: in.InputRef, TurnID: in.Identity.TurnID, OriginalText: src.Query, InputType: "text", Question: c.Question, Solution: c.Solution, ModelRef: modelRef, ReviewStatus: "structure_validated", CreatedAt: now, HistoryStatus: "PENDING"}
	if imageSource != nil {
		v.InputType = "image"
		v.SourceImageObjectKey = &imageSource.ImageObjectKey
		v.OCRResultObjectKey = &imageSource.RawObjectKey
	}
	for i := range v.Solution.Steps {
		v.Solution.Steps[i].StepID = idgen.New("st")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO teaching_question(question_id,student_id,scope_key,turn_id,source_message_id,original_text,source_hash,input_type,source_image_object_key,ocr_result_object_key,question_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.QuestionID, in.Identity.SubjectID, in.Identity.Scope.Key(), in.Identity.TurnID, in.InputRef, src.Query, sourceHash, v.InputType, v.SourceImageObjectKey, v.OCRResultObjectKey, js(c.Question), now); err != nil {
		return saved, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO teaching_solution(solution_id,question_id,answer,analysis,knowledge_points,model_ref,review_status,schema_version,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.SolutionID, v.QuestionID, js(c.Solution.Answer), c.Solution.Analysis, js(c.Solution.KnowledgePoints), modelRef, v.ReviewStatus, "math-solution-v1", now); err != nil {
		return saved, err
	}
	for _, step := range v.Solution.Steps {
		if _, err = tx.ExecContext(ctx, `INSERT INTO teaching_solution_step(step_id,solution_id,ordinal,subquestion_id,title,content) VALUES(?,?,?,?,?,?)`, step.StepID, v.SolutionID, step.Ordinal, step.SubquestionID, step.Title, step.Content); err != nil {
			return saved, err
		}
	}
	call.Result = js(v)
	call.SchemaVersion = "math-solution-v1"
	call.Hash = call.Digest()
	receipt := state.Receipt{OriginalInput: schema.UserMessage(rawInput), Kind: "business", ResultID: v.ResultID, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID, InputVersion: sourceHash, CommittedAt: now, Terminal: &call, FrozenPrefix: state.CleanMessages(prefix)}
	if _, e = state.RestoreTerminal(receipt.FrozenPrefix, &receipt); e != nil {
		return saved, e
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO teaching_solution_receipt(result_id,scope_key,turn_id,student_id,question_id,solution_id,input_json,receipt_json,view_json,repair_due_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ResultID, in.Identity.Scope.Key(), in.Identity.TurnID, in.Identity.SubjectID, v.QuestionID, v.SolutionID, js(in), js(receipt), js(v), now, now); err != nil {
		return saved, err
	}
	if err = tx.Commit(); err != nil {
		return saved, err
	}
	return solve.Saved{Input: in, Receipt: receipt, View: v}, nil
}
func (r *Repository) RepairPending(ctx context.Context, repair func(context.Context, solve.Saved) error) error {
	rows, e := r.DB.QueryContext(ctx, "SELECT "+savedColumns+" FROM teaching_solution_receipt WHERE history_status='PENDING' AND repair_due_at<=UTC_TIMESTAMP(6) ORDER BY id LIMIT 5")
	if e != nil {
		return e
	}
	var items []solve.Saved
	for rows.Next() {
		v, e := readSaved(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range items {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		e = repair(c, v)
		cancel()
		if e == nil {
			_, e = r.DB.ExecContext(ctx, "UPDATE teaching_solution_receipt SET history_status='SEALED' WHERE result_id=?", v.View.ResultID)
		} else {
			_, e = r.DB.ExecContext(ctx, "UPDATE teaching_solution_receipt SET repair_attempts=repair_attempts+1,repair_due_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND) WHERE result_id=?", v.View.ResultID)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
