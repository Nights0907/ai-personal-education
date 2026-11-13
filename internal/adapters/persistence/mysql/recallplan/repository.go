package recallplan

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"github.com/musi-ai/education/internal/platform/idgen"
	"strings"
	"time"

	rp "github.com/musi-ai/education/internal/modules/teaching/recallplan"
)

//go:embed schema.sql
var schemaSQL string

type Repository struct{ DB *sql.DB }

var _ rp.Repository = (*Repository)(nil)
var _ rp.StatusStore = (*Repository)(nil)

func (r *Repository) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(schemaSQL, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := r.DB.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return r.upgrade(ctx)
}

func (r *Repository) Plan(ctx context.Context, student string) (rp.Plan, error) {
	var p rp.Plan
	err := r.DB.QueryRowContext(ctx, "SELECT id,student_id,status,daily_quota,send_hour,timezone,last_run_date,created_at,updated_at FROM recall_plan WHERE student_id=? AND status='ACTIVE'", student).
		Scan(&p.ID, &p.StudentID, &p.Status, &p.DailyQuota, &p.SendHour, &p.Timezone, &p.LastRunDate, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, rp.ErrNotFound
	}
	return p, err
}

func (r *Repository) UpsertPlan(ctx context.Context, student string, quota, sendHour int) (rp.Plan, error) {
	if quota < 1 {
		quota = 1
	}
	if sendHour < 0 || sendHour > 23 {
		sendHour = 19
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO recall_plan(student_id,status,daily_quota,send_hour,timezone,last_run_date,created_at,updated_at)
		VALUES(?,'ACTIVE',?,?,'Asia/Shanghai','',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))
		ON DUPLICATE KEY UPDATE daily_quota=VALUES(daily_quota),send_hour=VALUES(send_hour),updated_at=UTC_TIMESTAMP(6)`, student, quota, sendHour)
	if err != nil {
		return rp.Plan{}, err
	}
	return r.Plan(ctx, student)
}

func (r *Repository) AddItems(ctx context.Context, planID int64, items []rp.Item) (int, error) {
	added := 0
	for _, it := range items {
		res, e := r.DB.ExecContext(ctx, `INSERT IGNORE INTO recall_plan_item(plan_id,question_id,area,topic,retrievability,status,cycle_no,created_at)
 SELECT ?,?,?,?,?,'PENDING',COALESCE((SELECT MAX(history.cycle_no) FROM recall_plan_item history WHERE history.plan_id=? AND history.question_id=?),0)+1,UTC_TIMESTAMP(6) FROM DUAL
 WHERE NOT EXISTS(SELECT 1 FROM recall_plan_item active WHERE active.plan_id=? AND active.question_id=? AND active.status IN ('PENDING','SENT'))`, planID, it.QuestionID, it.Area, it.Topic, it.Retrievability, planID, it.QuestionID, planID, it.QuestionID)
		if e != nil {
			return added, e
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return added, nil
}

func (r *Repository) PendingItems(ctx context.Context, planID int64, limit int) ([]rp.Item, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT id,plan_id,question_id,area,topic,retrievability,status,sent_at,conversation_id,created_at
		FROM recall_plan_item WHERE plan_id=? AND status='PENDING' ORDER BY retrievability ASC, id ASC LIMIT ?`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rp.Item{}
	for rows.Next() {
		var it rp.Item
		var sentAt sql.NullTime
		if err := rows.Scan(&it.ID, &it.PlanID, &it.QuestionID, &it.Area, &it.Topic, &it.Retrievability, &it.Status, &sentAt, &it.ConversationID, &it.CreatedAt); err != nil {
			return nil, err
		}
		if sentAt.Valid {
			t := sentAt.Time
			it.SentAt = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *Repository) MarkSent(ctx context.Context, itemID int64, conversationID string, at time.Time) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE recall_plan_item SET status='SENT',sent_at=?,conversation_id=? WHERE id=? AND status='PENDING'`, at.UTC(), conversationID, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return rp.ErrConflict
	}
	return nil
}

func (r *Repository) MarkReviewed(ctx context.Context, itemID int64) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE recall_plan_item SET status='REVIEWED' WHERE id=? AND status='SENT'`, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return rp.ErrConflict
	}
	return nil
}

func (r *Repository) SentItemsIn(ctx context.Context, planID int64, conversationIDs []string) ([]rp.Item, error) {
	if len(conversationIDs) == 0 {
		return []rp.Item{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(conversationIDs)), ",")
	args := make([]any, 0, len(conversationIDs)+1)
	args = append(args, planID)
	for _, c := range conversationIDs {
		args = append(args, c)
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT id,plan_id,question_id,area,topic,retrievability,status,sent_at,conversation_id,created_at
		FROM recall_plan_item WHERE plan_id=? AND status='SENT' AND conversation_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rp.Item{}
	for rows.Next() {
		var it rp.Item
		var sentAt sql.NullTime
		if err := rows.Scan(&it.ID, &it.PlanID, &it.QuestionID, &it.Area, &it.Topic, &it.Retrievability, &it.Status, &sentAt, &it.ConversationID, &it.CreatedAt); err != nil {
			return nil, err
		}
		if sentAt.Valid {
			t := sentAt.Time
			it.SentAt = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *Repository) DuePlans(ctx context.Context, now time.Time) ([]rp.Plan, error) {
	rows, e := r.DB.QueryContext(ctx, `SELECT id,student_id,status,daily_quota,send_hour,timezone,last_run_date,created_at,updated_at FROM recall_plan WHERE status='ACTIVE' AND lease_until<=? AND next_attempt_at<=?`, now.UTC(), now.UTC())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []rp.Plan
	for rows.Next() {
		var p rp.Plan
		if e = rows.Scan(&p.ID, &p.StudentID, &p.Status, &p.DailyQuota, &p.SendHour, &p.Timezone, &p.LastRunDate, &p.CreatedAt, &p.UpdatedAt); e != nil {
			return nil, e
		}
		local := rp.LocalTime(p, now)
		if p.LastRunDate != local.Format("2006-01-02") {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func (r *Repository) MarkRan(ctx context.Context, planID int64, date string) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE recall_plan SET last_run_date=?,lease_token='',lease_until='1970-01-01',next_attempt_at='1970-01-01',updated_at=UTC_TIMESTAMP(6) WHERE id=?`, date, planID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return rp.ErrConflict
	}
	return nil
}

func (r *Repository) PlanItemCounts(ctx context.Context, planID int64) (pending, sent, reviewed int, err error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM recall_plan_item WHERE plan_id=? GROUP BY status`, planID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case rp.ItemPending:
			pending = n
		case rp.ItemSent:
			sent = n
		case rp.ItemReviewed:
			reviewed = n
		}
	}
	return pending, sent, reviewed, rows.Err()
}

func (r *Repository) upgrade(ctx context.Context) error {
	for _, col := range []struct{ table, name, ddl string }{
		{"recall_plan", "timezone", "VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai'"},
		{"recall_plan", "lease_token", "VARCHAR(36) NOT NULL DEFAULT ''"},
		{"recall_plan", "lease_until", "DATETIME(6) NOT NULL DEFAULT '1970-01-01'"},
		{"recall_plan", "next_attempt_at", "DATETIME(6) NOT NULL DEFAULT '1970-01-01'"},
		{"recall_plan_item", "cycle_no", "BIGINT UNSIGNED NOT NULL DEFAULT 1"},
	} {
		var n int
		if e := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, col.table, col.name).Scan(&n); e != nil {
			return e
		}
		if n == 0 {
			if _, e := r.DB.ExecContext(ctx, "ALTER TABLE "+col.table+" ADD COLUMN "+col.name+" "+col.ddl); e != nil {
				return e
			}
		}
	}
	var old int
	if e := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='recall_plan_item' AND index_name='uk_plan_question_pending'`).Scan(&old); e != nil {
		return e
	}
	if old > 0 {
		if _, e := r.DB.ExecContext(ctx, `ALTER TABLE recall_plan_item DROP INDEX uk_plan_question_pending`); e != nil {
			return e
		}
		// Preserve all historical rows while assigning distinct cycle identities.
		if _, e := r.DB.ExecContext(ctx, `UPDATE recall_plan_item SET cycle_no=id`); e != nil {
			return e
		}
		if _, e := r.DB.ExecContext(ctx, `UPDATE recall_plan_item newer JOIN recall_plan_item older ON older.plan_id=newer.plan_id AND older.question_id=newer.question_id AND older.id<newer.id AND older.status IN ('PENDING','SENT') SET newer.status='CANCELLED' WHERE newer.status IN ('PENDING','SENT')`); e != nil {
			return e
		}
	}
	var n int
	if e := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='recall_plan_item' AND column_name='active_question'`).Scan(&n); e != nil {
		return e
	}
	if n == 0 {
		_, e := r.DB.ExecContext(ctx, `ALTER TABLE recall_plan_item ADD COLUMN active_question VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin GENERATED ALWAYS AS (CASE WHEN status IN ('PENDING','SENT') THEN question_id ELSE NULL END) STORED, ADD UNIQUE KEY uk_plan_active_question(plan_id,active_question), ADD UNIQUE KEY uk_plan_question_cycle(plan_id,question_id,cycle_no)`)
		return e
	}
	return nil
}
func (r *Repository) DiscoverPlans(ctx context.Context, hour int) error {
	_, e := r.DB.ExecContext(ctx, `INSERT IGNORE INTO recall_plan(student_id,status,daily_quota,send_hour,timezone,last_run_date,created_at,updated_at) SELECT student_id,'ACTIVE',1,?,'Asia/Shanghai','',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6) FROM student_memory_state WHERE last_extracted_at IS NOT NULL`, hour)
	return e
}
func (r *Repository) AcquirePlan(ctx context.Context, p rp.Plan, now time.Time) (bool, error) {
	duration := time.Duration(p.DailyQuota*90+60) * time.Second
	if duration < 3*time.Minute {
		duration = 3 * time.Minute
	}
	res, e := r.DB.ExecContext(ctx, `UPDATE recall_plan SET lease_token=?,lease_until=? WHERE id=? AND status='ACTIVE' AND last_run_date<>? AND lease_until<=? AND next_attempt_at<=?`, idgen.New("rl"), now.UTC().Add(duration), p.ID, rp.LocalTime(p, now).Format("2006-01-02"), now.UTC(), now.UTC())
	if e != nil {
		return false, e
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (r *Repository) ReleasePlan(ctx context.Context, id int64, next time.Time) error {
	_, e := r.DB.ExecContext(ctx, `UPDATE recall_plan SET lease_token='',lease_until='1970-01-01',next_attempt_at=? WHERE id=?`, next.UTC(), id)
	return e
}
func (r *Repository) SyncPending(ctx context.Context, id int64, items []rp.Item) error {
	allowed := map[string]rp.Item{}
	for _, it := range items {
		if prev, ok := allowed[it.QuestionID]; !ok || it.Retrievability < prev.Retrievability {
			allowed[it.QuestionID] = it
		}
	}
	pending, e := r.PendingItems(ctx, id, 100000)
	if e != nil {
		return e
	}
	for _, it := range pending {
		if cur, ok := allowed[it.QuestionID]; ok {
			_, e = r.DB.ExecContext(ctx, `UPDATE recall_plan_item SET retrievability=?,topic=?,area=? WHERE id=? AND status='PENDING'`, cur.Retrievability, cur.Topic, cur.Area, it.ID)
		} else {
			e = r.CancelItem(ctx, it.ID)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) SentBetween(ctx context.Context, id int64, start, end time.Time) (int, error) {
	var n int
	e := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM recall_plan_item WHERE plan_id=? AND sent_at>=? AND sent_at<?`, id, start.UTC(), end.UTC()).Scan(&n)
	return n, e
}
func (r *Repository) CancelItem(ctx context.Context, id int64) error {
	_, e := r.DB.ExecContext(ctx, `UPDATE recall_plan_item SET status='CANCELLED' WHERE id=? AND status='PENDING'`, id)
	return e
}

func (r *Repository) ReviewNotifications(ctx context.Context, id int64) ([]rp.ReviewNotification, error) {
	rows, e := r.DB.QueryContext(ctx, `SELECT question_id,conversation_id,sent_at FROM recall_plan_item WHERE plan_id=? AND status='SENT' ORDER BY sent_at DESC LIMIT 20`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []rp.ReviewNotification
	for rows.Next() {
		var n rp.ReviewNotification
		if e = rows.Scan(&n.QuestionID, &n.ConversationID, &n.SentAt); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *Repository) NotificationItem(ctx context.Context, planID, itemID int64) (rp.Item, error) {
	var item rp.Item
	err := r.DB.QueryRowContext(ctx, `SELECT id,plan_id,question_id,status,COALESCE(conversation_id,'') FROM recall_plan_item WHERE plan_id=? AND id=?`, planID, itemID).Scan(&item.ID, &item.PlanID, &item.QuestionID, &item.Status, &item.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return item, rp.ErrNotFound
	}
	return item, err
}
