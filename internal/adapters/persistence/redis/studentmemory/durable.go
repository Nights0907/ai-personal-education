package studentmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
)

// DurableDocuments stores the full month transactionally in MySQL. Redis is a cache.
type DurableDocuments struct {
	DB    *sql.DB
	Cache *Documents
}

func (d *DurableDocuments) Migrate(ctx context.Context) error {
	_, e := d.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS student_memory_document(student_id VARCHAR(128) NOT NULL,month VARCHAR(7) NOT NULL,revision BIGINT NOT NULL,document LONGTEXT NOT NULL,PRIMARY KEY(student_id,month))`)
	return e
}
func (d *DurableDocuments) Load(ctx context.Context, student, month string) (mem.Month, error) {
	var raw string
	e := d.DB.QueryRowContext(ctx, "SELECT document FROM student_memory_document WHERE student_id=? AND month=?", student, month).Scan(&raw)
	if e == nil {
		var m mem.Month
		e = json.Unmarshal([]byte(raw), &m)
		return m, e
	}
	if e != sql.ErrNoRows {
		return mem.Month{}, e
	}
	if d.Cache == nil {
		return mem.Month{}, mem.ErrNotFound
	}
	m, e := d.Cache.Load(ctx, student, month)
	if e != nil {
		return m, e
	}
	rawBytes, _ := json.Marshal(m)
	_, e = d.DB.ExecContext(ctx, "INSERT IGNORE INTO student_memory_document VALUES(?,?,?,?)", student, month, m.Raw.Revision, string(rawBytes))
	return m, e
}
func (d *DurableDocuments) Publish(ctx context.Context, student, month string, old, next mem.Month) error {
	saved, e := validatedMonth(student, month, old, next)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(saved)
	if e != nil {
		return e
	}
	if old.Raw.Revision == 0 {
		res, e := d.DB.ExecContext(ctx, "INSERT IGNORE INTO student_memory_document VALUES(?,?,?,?)", student, month, saved.Raw.Revision, string(raw))
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return mem.ErrConflict
		}
		return nil
	}
	res, e := d.DB.ExecContext(ctx, "UPDATE student_memory_document SET revision=?,document=? WHERE student_id=? AND month=? AND revision=?", saved.Raw.Revision, string(raw), student, month, old.Raw.Revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return mem.ErrConflict
	}
	return nil
}
func (d *DurableDocuments) Indexes(ctx context.Context, student string, limit int) ([]mem.Document[mem.IndexContent], error) {
	if limit < 1 || limit > 120 {
		return nil, mem.ErrConflict
	}
	// Import existing Redis months once without overwriting durable records.
	var cached []mem.Document[mem.IndexContent]
	var e error
	if d.Cache != nil {
		cached, e = d.Cache.Indexes(ctx, student, 120)
	}
	if e == nil {
		for _, idx := range cached {
			if _, e = d.Load(ctx, student, idx.Month); e != nil {
				return nil, e
			}
		}
	}
	rows, e := d.DB.QueryContext(ctx, "SELECT document FROM student_memory_document WHERE student_id=? ORDER BY month DESC LIMIT ?", student, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []mem.Document[mem.IndexContent]{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		var m mem.Month
		if e = json.Unmarshal([]byte(raw), &m); e != nil {
			return nil, e
		}
		out = append(out, m.Index)
	}
	return out, rows.Err()
}
