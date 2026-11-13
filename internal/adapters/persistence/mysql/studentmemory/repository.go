package studentmemory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/platform/idgen"
)

//go:embed schema.sql
var schemaSQL string

type Repository struct{ DB *sql.DB }

var _ mem.Repository = (*Repository)(nil)

func (r *Repository) Migrate(ctx context.Context) error {
	for _, statement := range strings.Split(schemaSQL, ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := r.DB.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if err := r.dropLegacyObservations(ctx); err != nil {
		return err
	}
	return r.ValidateSchema(ctx)
}

// dropLegacyObservations removes the v1 observations_json column. It is a
// one-way migration: the column is backed up to _archive via a caller-supplied
// dump before this runs. Idempotent when the column is already absent.
func (r *Repository) dropLegacyObservations(ctx context.Context) error {
	var count int
	if err := r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='student_profile_l0' AND COLUMN_NAME='observations_json'").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	_, err := r.DB.ExecContext(ctx, "ALTER TABLE student_profile_l0 DROP COLUMN observations_json")
	return err
}
func (r *Repository) ValidateSchema(ctx context.Context) error {
	for _, statement := range []string{"SELECT student_id,revision,basic_json FROM student_profile_l0 LIMIT 0", "SELECT handled_conversation_id,lease_until,next_attempt_at FROM student_memory_state LIMIT 0", "SELECT source_message_id,month,processed_at FROM student_memory_turn LIMIT 0"} {
		rows, err := r.DB.QueryContext(ctx, statement)
		if err != nil {
			return err
		}
		rows.Close()
	}
	return nil
}
func validStudent(id string) error { _, err := mem.MonthlyPaths(id, "2026-01"); return err }
func jsonText(v any) string        { b, _ := json.Marshal(v); return string(b) }

func (r *Repository) Profile(ctx context.Context, student string) (mem.Profile, error) {
	p := mem.Profile{StudentID: student}
	if err := validStudent(student); err != nil {
		return p, err
	}
	var basic []byte
	err := r.DB.QueryRowContext(ctx, "SELECT revision,basic_json,updated_at FROM student_profile_l0 WHERE student_id=?", student).Scan(&p.Revision, &basic, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(basic, &p.Basic)
	return p, err
}
func (r *Repository) SaveBasic(ctx context.Context, student string, basic mem.BasicProfile, revision int64) (mem.Profile, error) {
	if err := validStudent(student); err != nil {
		return mem.Profile{}, err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return mem.Profile{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO student_profile_l0(student_id,revision,basic_json,updated_at) VALUES(?,0,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE student_id=student_id", student, jsonText(mem.BasicProfile{}))
	if err != nil {
		return mem.Profile{}, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE student_profile_l0 SET basic_json=?,revision=revision+1,updated_at=UTC_TIMESTAMP(6) WHERE student_id=? AND revision=?", jsonText(basic), student, revision)
	if err != nil {
		return mem.Profile{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return mem.Profile{}, mem.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return mem.Profile{}, err
	}
	return r.Profile(ctx, student)
}

// Discover reconciles after crashes and client disconnects. Only SEALED pairs
// on active conversations become eligible. No model or Redis access occurs here.
func (r *Repository) Discover(ctx context.Context) error {
	if _, e := r.DB.ExecContext(ctx, `UPDATE student_memory_turn m JOIN agent_conversation c ON c.scope_key=m.scope_key SET m.processed_at=UTC_TIMESTAMP(6) WHERE m.processed_at IS NULL AND c.status<>'ACTIVE'`); e != nil {
		return e
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO student_memory_turn(student_id,source_message_id,scope_key,month,created_at)
 SELECT c.subject_id,a.message_id,a.scope_key,DATE_FORMAT(DATE_ADD(COALESCE(STR_TO_DATE(LEFT(JSON_UNQUOTE(JSON_EXTRACT(u.ext,'$.SubmittedAt')),19),'%Y-%m-%dT%H:%i:%s'),u.created_at),INTERVAL 8 HOUR),'%Y-%m'),a.created_at
 FROM agent_message a JOIN agent_message u ON u.scope_key=a.scope_key AND u.turn_id=a.turn_id AND u.role='user' AND u.status='SEALED'
 JOIN agent_conversation c ON c.scope_key=a.scope_key AND c.namespace='teacher' AND c.status='ACTIVE'
 LEFT JOIN student_memory_turn m ON m.source_message_id=a.message_id
 WHERE a.role='assistant' AND a.status='SEALED' AND m.id IS NULL ORDER BY a.id LIMIT 200
 ON DUPLICATE KEY UPDATE source_message_id=student_memory_turn.source_message_id`)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO student_memory_state(student_id,updated_at) SELECT DISTINCT student_id,UTC_TIMESTAMP(6) FROM student_memory_turn WHERE processed_at IS NULL ON DUPLICATE KEY UPDATE student_id=student_memory_state.student_id`)
	return err
}

func (r *Repository) Claim(ctx context.Context, now time.Time) (mem.Claim, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT s.student_id,s.handled_conversation_id,COUNT(m.id),
 (SELECT MAX(c.id) FROM agent_conversation c WHERE c.namespace='teacher' AND c.subject_id=s.student_id AND c.status='ACTIVE'),
 (SELECT MAX(u.created_at) FROM agent_message u JOIN agent_conversation c ON c.scope_key=u.scope_key WHERE c.namespace='teacher' AND c.subject_id=s.student_id AND c.status='ACTIVE' AND u.role='user'),
 (SELECT MIN(c.id) FROM student_memory_turn p JOIN agent_conversation c ON c.scope_key=p.scope_key WHERE p.student_id=s.student_id AND p.processed_at IS NULL)
 FROM student_memory_state s JOIN student_memory_turn m ON m.student_id=s.student_id AND m.processed_at IS NULL
 WHERE s.lease_until<=? AND s.next_attempt_at<=? GROUP BY s.student_id,s.handled_conversation_id ORDER BY MIN(m.id) LIMIT 100`, now, now)
	if err != nil {
		return mem.Claim{}, err
	}
	var candidates []mem.Claim
	for rows.Next() {
		var student string
		var handled, latest, oldestConversation int64
		var count int
		var last sql.NullTime
		if err = rows.Scan(&student, &handled, &count, &latest, &last, &oldestConversation); err != nil {
			break
		}
		reason := mem.Due(count, latest > oldestConversation, last.Time, now)
		if reason == "" {
			var completed bool
			e := r.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM student_memory_turn m JOIN agent_message a ON a.message_id=m.source_message_id JOIN agent_message u ON u.scope_key=a.scope_key AND u.turn_id=a.turn_id AND u.role='user' WHERE m.student_id=? AND m.processed_at IS NULL AND a.status='SEALED' AND (JSON_SEARCH(a.content,'one','reveal_solution') IS NOT NULL OR JSON_UNQUOTE(JSON_EXTRACT(u.ext,'$.Mode'))='review'))`, student).Scan(&completed)
			if e != nil {
				err = e
				break
			}
			if completed {
				reason = "completed_task"
			}
		}
		if reason != "" {
			candidates = append(candidates, mem.Claim{StudentID: student, ConversationVersion: latest, Reason: reason})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return mem.Claim{}, err
	}
	for _, c := range candidates {
		c.Token = idgen.New("lm")
		res, e := r.DB.ExecContext(ctx, "UPDATE student_memory_state SET lease_token=?,lease_until=?,last_reason=?,updated_at=? WHERE student_id=? AND lease_until<=? AND next_attempt_at<=?", c.Token, now.Add(3*time.Minute), c.Reason, now, c.StudentID, now, now)
		if e != nil {
			return c, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		c.Turns, e = r.pending(ctx, c.StudentID)
		if e != nil {
			_ = r.Retry(ctx, c, now.Add(time.Minute))
			return c, e
		}
		return c, nil
	}
	return mem.Claim{}, mem.ErrNotFound
}

func (r *Repository) pending(ctx context.Context, student string) ([]mem.PendingTurn, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT m.id,m.month,c.conversation_id,a.branch_id,a.turn_id,u.message_id,a.message_id,u.content,u.ext,a.content,u.created_at,COALESCE(q.question_id,''),v.view_json
 FROM student_memory_turn m JOIN agent_message a ON a.scope_key=m.scope_key AND a.message_id=m.source_message_id
 JOIN agent_message u ON u.scope_key=a.scope_key AND u.turn_id=a.turn_id AND u.role='user'
 JOIN agent_conversation c ON c.scope_key=a.scope_key AND c.status='ACTIVE'
 LEFT JOIN teaching_question q ON q.student_id=c.subject_id AND ((q.scope_key=a.scope_key AND q.turn_id=a.turn_id) OR q.question_id=JSON_UNQUOTE(JSON_EXTRACT(u.ext,'$.GuideQuestionID')))
 LEFT JOIN teaching_solution_receipt v ON v.question_id=q.question_id AND v.student_id=c.subject_id
 WHERE m.student_id=? AND m.processed_at IS NULL ORDER BY m.id LIMIT 10`, student)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mem.PendingTurn{}
	for rows.Next() {
		t := mem.PendingTurn{StudentID: student}
		var userID, assistantID string
		var user, ext, assistant, view []byte
		if err = rows.Scan(&t.ID, &t.Month, &t.Raw.Source.ConversationID, &t.Raw.Source.BranchID, &t.Raw.Source.TurnID, &userID, &assistantID, &user, &ext, &assistant, &t.Raw.CreatedAt, &t.Raw.Source.QuestionID, &view); err != nil {
			return nil, err
		}
		t.Raw.Source.MessageIDs = []string{userID, assistantID}
		var u, a struct {
			Items []*schema.Message `json:"items"`
		}
		var meta struct {
			OriginalInput   *schema.Message
			ImageAssetID    string
			GuideQuestionID string
			SubmittedAt     *time.Time
			Mode            string
		}
		if err = json.Unmarshal(user, &u); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(assistant, &a); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(ext, &meta); err != nil {
			return nil, err
		}
		for _, msg := range u.Items {
			if msg != nil && msg.Role == schema.User {
				t.Raw.User += msg.Content
			}
		}
		// For OCR turns retain the recognized canonical question plus the student's
		// original note. Tool payloads and hidden reasoning never become memory.
		if meta.SubmittedAt != nil {
			t.Raw.CreatedAt = meta.SubmittedAt.UTC()
		}
		t.Raw.Assisted = meta.Mode == "guide" && meta.GuideQuestionID != ""
		t.Raw.Purpose = "question"
		if meta.GuideQuestionID != "" {
			t.Raw.Purpose = "attempt"
		}
		for _, m := range a.Items {
			if m != nil && m.Role == schema.Tool && (m.ToolName == "evaluate_student_attempt" || m.ToolName == "reveal_solution") {
				var v mem.AttemptEvaluation
				if json.Unmarshal([]byte(m.Content), &v) == nil {
					if t.Raw.Evaluation == nil {
						t.Raw.Evaluation = &v
					} else {
						t.Raw.Evaluation.Verified = t.Raw.Evaluation.Verified || v.Verified
						t.Raw.Evaluation.Incorrect = t.Raw.Evaluation.Incorrect || v.Incorrect
					}
				}
			}
		}

		t.Raw.ImageAssetID = meta.ImageAssetID
		if meta.ImageAssetID != "" && meta.GuideQuestionID != "" {
			t.Raw.AttemptUnavailable = true
			for _, msg := range a.Items {
				if msg != nil && msg.Role == schema.Tool && msg.ToolName == "recognize_review_image" {
					var o struct {
						AssetID string `json:"asset_id"`
						Text    string `json:"text"`
						OCRID   string `json:"ocr_result_id"`
					}
					if json.Unmarshal([]byte(msg.Content), &o) == nil && o.AssetID == meta.ImageAssetID && o.Text != "" {
						t.Raw.AttemptOCR = o.Text
						t.Raw.AttemptOCRID = o.OCRID
						t.Raw.AttemptUnavailable = false
					}
				}
			}
		}
		if meta.Mode == "review" && meta.GuideQuestionID != "" {
			// Earlier teacher feedback in this task means the latest answer was
			// not a cold independent attempt. System delivery has no sealed reply.
			var hints int
			e := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_message prior JOIN agent_message pu ON pu.scope_key=prior.scope_key AND pu.turn_id=prior.turn_id AND pu.role='user' WHERE prior.scope_key=? AND prior.role='assistant' AND prior.status='SEALED' AND prior.created_at<? AND JSON_UNQUOTE(JSON_EXTRACT(pu.ext,'$.GuideQuestionID'))=?`, (state.Scope{Namespace: "teacher", SubjectID: student, ConversationID: t.Raw.Source.ConversationID}).Key(), t.Raw.CreatedAt, meta.GuideQuestionID).Scan(&hints)
			if e != nil {
				return nil, e
			}
			t.Raw.Assisted = hints > 0
		}

		if meta.OriginalInput != nil {
			t.Raw.User = meta.OriginalInput.Content
		}
		for _, msg := range a.Items {
			if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
				t.Raw.Assistant += msg.Content + "\n"
			}
		}
		if len(view) > 0 {
			var solved solve.View
			if err = json.Unmarshal(view, &solved); err != nil {
				return nil, err
			}
			if solved.TurnID == t.Raw.Source.TurnID {
				t.Raw.Assistant = jsonText(solved.Solution)
			}
			t.Raw.QuestionText = solved.Question.Stem
			if solved.InputType == "image" {
				t.Raw.QuestionText = solved.Question.Stem
				t.Raw.InputType = "image"
			} else {
				t.Raw.InputType = "text"
			}
			t.Raw.Difficulty = solved.Question.Difficulty
			// Project the solve catalog's knowledge codes onto the memory taxonomy
			// as answer-free background for the extractor. Unknown codes are
			// dropped, never guessed.
			tags := []string{}
			seen := map[string]bool{}
			for _, k := range solved.Solution.KnowledgePoints {
				if area := mem.MapSolveKnowledge(k.Code); area != "" && !seen[area] {
					seen[area] = true
					tags = append(tags, area)
				}
			}
			t.Raw.KnowledgeTags = tags
		}
		if meta.ImageAssetID != "" && meta.GuideQuestionID != "" {
			t.Raw.InputType = "image"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) Complete(ctx context.Context, c mem.Claim) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var token string
	var until time.Time
	if err = tx.QueryRowContext(ctx, "SELECT lease_token,lease_until FROM student_memory_state WHERE student_id=? FOR UPDATE", c.StudentID).Scan(&token, &until); err != nil {
		return err
	}
	if token != c.Token || !until.After(time.Now().UTC()) {
		return mem.ErrConflict
	}
	for _, t := range c.Turns {
		if t.Raw.Assessment != nil && t.Raw.Assessment.Authored && t.Raw.Source.QuestionID != "" {
			sid := mem.SourceID(t.Raw.Source)
			sum := sha256.Sum256([]byte(c.StudentID + "/" + sid))
			if _, e := tx.ExecContext(ctx, `INSERT IGNORE INTO student_memory_evidence(event_key,student_id,conversation_id,question_id,source_id,occurred_at) VALUES(?,?,?,?,?,?)`, fmt.Sprintf("%x", sum), c.StudentID, t.Raw.Source.ConversationID, t.Raw.Source.QuestionID, sid, t.Raw.CreatedAt); e != nil {
				return e
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE student_memory_turn SET processed_at=UTC_TIMESTAMP(6) WHERE id=? AND student_id=? AND processed_at IS NULL", t.ID, c.StudentID); err != nil {
			return err
		}
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM student_memory_turn WHERE student_id=? AND processed_at IS NULL", c.StudentID).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		_, err = tx.ExecContext(ctx, "UPDATE student_memory_state SET handled_conversation_id=GREATEST(handled_conversation_id,?) WHERE student_id=?", c.ConversationVersion, c.StudentID)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE student_memory_state SET lease_token='',lease_until='1970-01-01',next_attempt_at='1970-01-01',last_error='',last_extracted_at=UTC_TIMESTAMP(6),updated_at=UTC_TIMESTAMP(6) WHERE student_id=?", c.StudentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repository) Retry(ctx context.Context, c mem.Claim, next time.Time) error {
	_, err := r.DB.ExecContext(ctx, "UPDATE student_memory_state SET lease_token='',lease_until='1970-01-01',next_attempt_at=?,last_error='EXTRACTION_PENDING_RETRY',updated_at=UTC_TIMESTAMP(6) WHERE student_id=? AND lease_token=?", next, c.StudentID, c.Token)
	return err
}
func (r *Repository) Status(ctx context.Context, student string) (any, error) {
	var pending, done int
	err := r.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(processed_at IS NULL),0),COALESCE(SUM(processed_at IS NOT NULL),0) FROM student_memory_turn WHERE student_id=?", student).Scan(&pending, &done)
	if err != nil {
		return nil, err
	}
	var reason, lastError string
	var extracted sql.NullTime
	err = r.DB.QueryRowContext(ctx, "SELECT last_reason,last_error,last_extracted_at FROM student_memory_state WHERE student_id=?", student).Scan(&reason, &lastError, &extracted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return map[string]any{"pending_turns": pending, "extracted_turns": done, "last_reason": reason, "last_error": lastError, "last_extracted_at": extracted.Time, "threshold_turns": 10, "idle_hours": 2}, nil
}

func (r *Repository) PendingEvidence(ctx context.Context, now time.Time) (mem.EvidenceEvent, error) {
	var e mem.EvidenceEvent
	err := r.DB.QueryRowContext(ctx, `SELECT id,student_id,conversation_id,question_id,source_id,occurred_at FROM student_memory_evidence WHERE processed_at IS NULL AND next_attempt_at<=? ORDER BY id LIMIT 1`, now).Scan(&e.ID, &e.StudentID, &e.ConversationID, &e.QuestionID, &e.SourceID, &e.At)
	if errors.Is(err, sql.ErrNoRows) {
		err = mem.ErrNotFound
	}
	return e, err
}
func (r *Repository) CompleteEvidence(ctx context.Context, id int64) error {
	_, e := r.DB.ExecContext(ctx, `UPDATE student_memory_evidence SET processed_at=UTC_TIMESTAMP(6) WHERE id=? AND processed_at IS NULL`, id)
	return e
}
func (r *Repository) RetryEvidence(ctx context.Context, id int64, now time.Time) error {
	_, e := r.DB.ExecContext(ctx, `UPDATE student_memory_evidence SET next_attempt_at=? WHERE id=? AND processed_at IS NULL`, now, id)
	return e
}

// EnsureEvidenceSchema is an additive startup migration; it never touches
// historical profile columns or rewrites existing student documents.
func (r *Repository) EnsureEvidenceSchema(ctx context.Context) error {
	for _, stmt := range strings.Split(schemaSQL, ";") {
		if strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS student_memory_evidence") {
			_, e := r.DB.ExecContext(ctx, stmt)
			return e
		}
	}
	return errors.New("memory evidence schema missing")
}
