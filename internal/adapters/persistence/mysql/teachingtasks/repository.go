package teachingtasks

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/modules/teaching/tasks"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"strings"
	"time"
)

//go:embed schema.sql
var ddl string

type Repository struct{ DB *sql.DB }

func (r *Repository) Migrate(ctx context.Context) error {
	for _, q := range strings.Split(ddl, ";") {
		if strings.TrimSpace(q) != "" {
			if _, e := r.DB.ExecContext(ctx, q); e != nil {
				return e
			}
		}
	}
	return nil
}

const cols = "t.id,t.student_id,t.conversation_id,t.input_json,t.parent_id,t.status,t.opening_text,t.result_json,t.error_text,t.lease_token,t.attempts,t.created_at"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (tasks.Task, error) {
	var t tasks.Task
	var input, result []byte
	e := row.Scan(&t.ID, &t.StudentID, &t.ConversationID, &input, &t.ParentID, &t.Status, &t.Opening, &result, &t.Error, &t.Token, &t.Attempts, &t.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return t, tasks.ErrNotFound
	}
	if e != nil {
		return t, e
	}
	if e = json.Unmarshal(input, &t.Input); e != nil {
		return t, e
	}
	if len(result) > 0 {
		e = json.Unmarshal(result, &t.Result)
	}
	return t, e
}
func js(v any) string { b, _ := json.Marshal(v); return string(b) }
func (r *Repository) Get(ctx context.Context, student, id string) (tasks.Task, error) {
	return scan(r.DB.QueryRowContext(ctx, "SELECT "+cols+" FROM teaching_task t JOIN agent_conversation c ON c.namespace='teacher' AND c.subject_id=t.student_id AND c.conversation_id=t.conversation_id AND c.status='ACTIVE' WHERE t.student_id=? AND t.id=?", student, id))
}
func (r *Repository) Create(ctx context.Context, student string, in query.Input, parent string) (tasks.Task, error) {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return tasks.Task{}, e
	}
	defer tx.Rollback()
	var status string
	e = tx.QueryRowContext(ctx, `SELECT status FROM agent_conversation WHERE namespace='teacher' AND subject_id=? AND conversation_id=? FOR UPDATE`, student, in.ConversationID).Scan(&status)
	if errors.Is(e, sql.ErrNoRows) {
		return tasks.Task{}, tasks.ErrNotFound
	}
	if e != nil {
		return tasks.Task{}, e
	}
	if status != "ACTIVE" {
		return tasks.Task{}, tasks.ErrConflict
	}
	if parent != "" {
		var pc, ps string
		e = tx.QueryRowContext(ctx, `SELECT conversation_id,parent_id FROM teaching_task WHERE id=? AND student_id=?`, parent, student).Scan(&pc, &ps)
		if e != nil || pc != in.ConversationID || ps != "" {
			return tasks.Task{}, tasks.ErrConflict
		}
	}
	hash := state.Hash([]any{in, parent})
	var id, oldhash string
	e = tx.QueryRowContext(ctx, `SELECT id,input_hash FROM teaching_task WHERE student_id=? AND client_request_id=?`, student, in.ClientRequestID).Scan(&id, &oldhash)
	if e == nil {
		if hash != oldhash {
			return tasks.Task{}, tasks.ErrConflict
		}
		_ = tx.Rollback()
		return r.Get(ctx, student, id)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return tasks.Task{}, e
	}
	id = idgen.New("tt")
	_, e = tx.ExecContext(ctx, `INSERT INTO teaching_task(id,student_id,conversation_id,client_request_id,input_json,input_hash,parent_id,opening_text,error_text,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'','',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, id, student, in.ConversationID, in.ClientRequestID, js(in), hash, parent)
	if e != nil {
		return tasks.Task{}, e
	}
	if e = tx.Commit(); e != nil {
		return tasks.Task{}, e
	}
	return r.Get(ctx, student, id)
}
func (r *Repository) List(ctx context.Context, student, conversation string) ([]tasks.Task, error) {
	q := "SELECT " + cols + " FROM teaching_task t JOIN agent_conversation c ON c.namespace='teacher' AND c.subject_id=t.student_id AND c.conversation_id=t.conversation_id AND c.status='ACTIVE' WHERE t.student_id=?"
	args := []any{student}
	if conversation != "" {
		q += " AND t.conversation_id=?"
		args = append(args, conversation)
	}
	q += " ORDER BY t.created_at DESC LIMIT 100"
	rows, e := r.DB.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []tasks.Task{}
	for rows.Next() {
		t, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (r *Repository) Events(ctx context.Context, student, id string, after int64) ([]tasks.Event, error) {
	if _, e := r.Get(ctx, student, id); e != nil {
		return nil, e
	}
	rows, e := r.DB.QueryContext(ctx, `SELECT id,kind,data_json FROM teaching_task_event WHERE task_id=? AND id>? ORDER BY id LIMIT 200`, id, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []tasks.Event{}
	for rows.Next() {
		var v tasks.Event
		var raw []byte
		if e = rows.Scan(&v.ID, &v.Type, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Data); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) Claim(ctx context.Context, now time.Time) (tasks.Task, error) {
	_, e := r.DB.ExecContext(ctx, `UPDATE teaching_task t JOIN agent_conversation c ON c.namespace='teacher' AND c.subject_id=t.student_id AND c.conversation_id=t.conversation_id SET t.status='CANCELLED' WHERE c.status<>'ACTIVE' AND t.status IN ('QUEUED','RUNNING')`)
	if e != nil {
		return tasks.Task{}, e
	}
	if _, e = r.DB.ExecContext(ctx, `UPDATE teaching_task SET status='FAILED',error_text='后台任务多次中断，原题与作答已保留，请重试任务' WHERE status='RUNNING' AND lease_until<=? AND attempts>=3`, now); e != nil {
		return tasks.Task{}, e
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return tasks.Task{}, e
	}
	defer tx.Rollback()
	t, e := scan(tx.QueryRowContext(ctx, "SELECT "+cols+` FROM teaching_task t WHERE (t.status='QUEUED' OR (t.status='RUNNING' AND t.lease_until<=?)) AND t.attempts<3 AND NOT EXISTS(SELECT 1 FROM teaching_task running WHERE running.student_id=t.student_id AND running.conversation_id=t.conversation_id AND running.id<>t.id AND running.status='RUNNING' AND running.lease_until>?) AND (t.parent_id='' OR EXISTS(SELECT 1 FROM teaching_task parent WHERE parent.id=t.parent_id AND parent.status='READY')) ORDER BY t.created_at,t.id LIMIT 1 FOR UPDATE SKIP LOCKED`, now, now))
	if e != nil {
		return t, e
	}
	var active string
	if e = tx.QueryRowContext(ctx, `SELECT status FROM agent_conversation WHERE namespace='teacher' AND subject_id=? AND conversation_id=? FOR UPDATE`, t.StudentID, t.ConversationID).Scan(&active); e != nil {
		return t, e
	}
	if active != "ACTIVE" {
		return t, tasks.ErrNotFound
	}
	var running int
	e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teaching_task WHERE student_id=? AND conversation_id=? AND id<>? AND status='RUNNING' AND lease_until>?`, t.StudentID, t.ConversationID, t.ID, now).Scan(&running)
	if e != nil {
		return t, e
	}
	if running > 0 {
		return t, tasks.ErrNotFound
	}
	t.Token = idgen.New("tl")
	t.Status = "RUNNING"
	t.Attempts++
	_, e = tx.ExecContext(ctx, `UPDATE teaching_task SET status='RUNNING',attempts=attempts+1,lease_token=?,lease_until=?,error_text='',updated_at=? WHERE id=?`, t.Token, now.Add(4*time.Minute), now, t.ID)
	if e != nil {
		return t, e
	}
	return t, tx.Commit()
}
func (r *Repository) Emit(ctx context.Context, t tasks.Task, kind string, data map[string]string) (string, error) {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var token, status, opening, active string
	var until time.Time
	e = tx.QueryRowContext(ctx, `SELECT t.lease_token,t.status,t.lease_until,t.opening_text,c.status FROM teaching_task t JOIN agent_conversation c ON c.namespace='teacher' AND c.subject_id=t.student_id AND c.conversation_id=t.conversation_id WHERE t.id=? FOR UPDATE`, t.ID).Scan(&token, &status, &until, &opening, &active)
	if e != nil {
		return "", e
	}
	if token != t.Token || status != "RUNNING" || active != "ACTIVE" || !until.After(time.Now().UTC()) {
		return "", tasks.ErrConflict
	}
	key := idgen.New("ev")
	if kind == "guide_question" {
		if opening != "" {
			return opening, nil
		}
		key = "opening"
		opening = data["text"]
		if _, e = tx.ExecContext(ctx, `UPDATE teaching_task SET opening_text=? WHERE id=?`, opening, t.ID); e != nil {
			return "", e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO teaching_task_event(task_id,event_key,kind,data_json,created_at) VALUES(?,?,?,?,UTC_TIMESTAMP(6))`, t.ID, key, kind, js(data)); e != nil {
		return "", e
	}
	return opening, tx.Commit()
}
func (r *Repository) Finish(ctx context.Context, t tasks.Task, result map[string]string, runErr error) error {
	status, message := "READY", ""
	if runErr != nil {
		status = "FAILED"
		message = runErr.Error()
		if errors.Is(runErr, state.ErrBudget) && t.Attempts < 3 {
			status = "QUEUED"
		}
	}
	res, e := r.DB.ExecContext(ctx, `UPDATE teaching_task t JOIN agent_conversation c ON c.namespace='teacher' AND c.subject_id=t.student_id AND c.conversation_id=t.conversation_id SET t.status=?,t.error_text=?,t.result_json=?,t.lease_token='',t.lease_until='1970-01-01',t.updated_at=UTC_TIMESTAMP(6) WHERE t.id=? AND t.lease_token=? AND t.status='RUNNING' AND t.lease_until>UTC_TIMESTAMP(6) AND c.status='ACTIVE'`, status, message, js(result), t.ID, t.Token)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return tasks.ErrConflict
	}
	return nil
}
func (r *Repository) Retry(ctx context.Context, student, id string) error {
	if _, e := r.Get(ctx, student, id); e != nil {
		return e
	}
	res, e := r.DB.ExecContext(ctx, `UPDATE teaching_task SET status='QUEUED',attempts=0,error_text='',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND student_id=? AND status='FAILED'`, id, student)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return tasks.ErrConflict
	}
	return nil
}
