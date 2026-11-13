package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/platform/idgen"
)

type Repository struct{ DB *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db} }
func (r *Repository) Migrate(ctx context.Context) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS teaching_image_asset(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,asset_id VARCHAR(64) NOT NULL UNIQUE,student_id VARCHAR(128) NOT NULL,object_key VARCHAR(512) NOT NULL,sha256 CHAR(64) NOT NULL,mime VARCHAR(32) NOT NULL,size_bytes INT NOT NULL,width INT NOT NULL,height INT NOT NULL,created_at DATETIME(6) NOT NULL,UNIQUE KEY uq_student_image(student_id,sha256)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS teaching_ocr_result(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,result_id VARCHAR(64) NOT NULL UNIQUE,asset_id VARCHAR(64) NOT NULL,config_hash CHAR(64) NOT NULL,status VARCHAR(16) NOT NULL,lease_token VARCHAR(64) NOT NULL DEFAULT '',lease_until DATETIME(6) NULL,result_json JSON NULL,created_at DATETIME(6) NOT NULL,updated_at DATETIME(6) NOT NULL,UNIQUE KEY uq_image_config(asset_id,config_hash),FOREIGN KEY(asset_id) REFERENCES teaching_image_asset(asset_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		if _, e := r.DB.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) ValidateSchema(ctx context.Context) error {
	for _, t := range []string{"teaching_image_asset", "teaching_ocr_result"} {
		rows, e := r.DB.QueryContext(ctx, "SELECT * FROM "+t+" LIMIT 0")
		if e != nil {
			return e
		}
		rows.Close()
	}
	return nil
}
func (r *Repository) CreateAsset(ctx context.Context, a photo.Asset) (photo.Asset, error) {
	_, e := r.DB.ExecContext(ctx, `INSERT INTO teaching_image_asset(asset_id,student_id,object_key,sha256,mime,size_bytes,width,height,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, a.AssetID, a.StudentID, a.ObjectKey, a.SHA256, a.MIME, a.Size, a.Width, a.Height, a.CreatedAt)
	if e != nil {
		return photo.Asset{}, e
	}
	return readAsset(r.DB.QueryRowContext(ctx, "SELECT "+assetCols+" FROM teaching_image_asset WHERE student_id=? AND sha256=?", a.StudentID, a.SHA256))
}

const assetCols = "asset_id,student_id,object_key,sha256,mime,size_bytes,width,height,created_at"

type scanner interface{ Scan(...any) error }

func readAsset(row scanner) (a photo.Asset, e error) {
	e = row.Scan(&a.AssetID, &a.StudentID, &a.ObjectKey, &a.SHA256, &a.MIME, &a.Size, &a.Width, &a.Height, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = photo.ErrNotFound
	}
	return
}
func (r *Repository) Asset(ctx context.Context, student, id string) (photo.Asset, error) {
	return readAsset(r.DB.QueryRowContext(ctx, "SELECT "+assetCols+" FROM teaching_image_asset WHERE student_id=? AND asset_id=?", student, id))
}
func readResult(row scanner) (v photo.Result, e error) {
	var raw []byte
	e = row.Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, photo.ErrNotFound
	}
	if e != nil {
		return
	}
	e = json.Unmarshal(raw, &v)
	return
}
func (r *Repository) Result(ctx context.Context, student, id, cfg string) (photo.Result, error) {
	return Cached(ctx, r.DB, student, id, cfg)
}

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Cached can use the caller's transaction, avoiding nested connection checkout
// while a teaching commit holds a database connection and conversation lock.
func Cached(ctx context.Context, q Queryer, student, id, cfg string) (photo.Result, error) {
	return readResult(q.QueryRowContext(ctx, `SELECT o.result_json FROM teaching_ocr_result o JOIN teaching_image_asset a ON a.asset_id=o.asset_id WHERE a.student_id=? AND a.asset_id=? AND o.config_hash=? AND o.status='READY'`, student, id, cfg))
}
func (r *Repository) ResultByID(ctx context.Context, student, id string) (photo.Result, error) {
	return readResult(r.DB.QueryRowContext(ctx, `SELECT o.result_json FROM teaching_ocr_result o JOIN teaching_image_asset a ON a.asset_id=o.asset_id WHERE a.student_id=? AND o.result_id=? AND o.status='READY'`, student, id))
}
func (r *Repository) Claim(ctx context.Context, student, id, cfg string) (string, *photo.Result, error) {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", nil, e
	}
	defer tx.Rollback()
	var owned string
	if e = tx.QueryRowContext(ctx, "SELECT asset_id FROM teaching_image_asset WHERE asset_id=? AND student_id=? FOR UPDATE", id, student).Scan(&owned); errors.Is(e, sql.ErrNoRows) {
		return "", nil, photo.ErrNotFound
	} else if e != nil {
		return "", nil, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO teaching_ocr_result(result_id,asset_id,config_hash,status,created_at,updated_at) VALUES(?,?,?,'NEW',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE id=id`, idgen.New("ocr"), id, cfg)
	if e != nil {
		return "", nil, e
	}
	var status string
	var busy bool
	var raw []byte
	if e = tx.QueryRowContext(ctx, `SELECT status,COALESCE(lease_until>UTC_TIMESTAMP(6),false),result_json FROM teaching_ocr_result WHERE asset_id=? AND config_hash=? FOR UPDATE`, id, cfg).Scan(&status, &busy, &raw); e != nil {
		return "", nil, e
	}
	if status == "READY" {
		var v photo.Result
		if e = json.Unmarshal(raw, &v); e != nil {
			return "", nil, e
		}
		return "", &v, tx.Commit()
	}
	if status == "PROCESSING" && busy {
		return "", nil, photo.ErrBusy
	}
	lease := idgen.New("ol")
	_, e = tx.ExecContext(ctx, `UPDATE teaching_ocr_result SET status='PROCESSING',lease_token=?,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 120 SECOND),updated_at=UTC_TIMESTAMP(6) WHERE asset_id=? AND config_hash=?`, lease, id, cfg)
	if e != nil {
		return "", nil, e
	}
	return lease, nil, tx.Commit()
}
func (r *Repository) Complete(ctx context.Context, student, id, cfg, lease string, v photo.Result) (photo.Result, error) {
	a, e := r.Asset(ctx, student, id)
	if e != nil {
		return v, e
	}
	if a.ObjectKey != v.ImageObjectKey || a.SHA256 != v.ImageHash || v.AssetID != id || v.ConfigHash != cfg || v.TextHash != photo.Hash([]byte(v.Text)) {
		return v, photo.ErrInvalid
	}
	if e = r.DB.QueryRowContext(ctx, "SELECT result_id FROM teaching_ocr_result WHERE asset_id=? AND config_hash=?", id, cfg).Scan(&v.ResultID); e != nil {
		return v, e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return v, e
	}
	result, e := r.DB.ExecContext(ctx, `UPDATE teaching_ocr_result SET status='READY',result_json=?,lease_until=NULL,updated_at=UTC_TIMESTAMP(6) WHERE asset_id=? AND config_hash=? AND status='PROCESSING' AND lease_token=? AND lease_until>UTC_TIMESTAMP(6)`, b, id, cfg, lease)
	if e != nil {
		return v, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return v, e
	}
	if n != 1 {
		return v, photo.ErrBusy
	}
	return v, nil
}
func (r *Repository) Fail(ctx context.Context, student, id, cfg, lease string) error {
	_, e := r.DB.ExecContext(ctx, `UPDATE teaching_ocr_result o JOIN teaching_image_asset a ON a.asset_id=o.asset_id SET o.status='FAILED',o.lease_until=NULL,o.updated_at=UTC_TIMESTAMP(6) WHERE a.student_id=? AND o.asset_id=? AND o.config_hash=? AND o.status='PROCESSING' AND o.lease_token=?`, student, id, cfg, lease)
	return e
}
