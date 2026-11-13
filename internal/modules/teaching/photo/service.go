package photo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/musi-ai/education/internal/platform/idgen"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"time"
)

type Service struct {
	Repo  Repository
	Files Files
	OCR   Recognizer
}

func Hash(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func (s *Service) Upload(ctx context.Context, student string, data []byte) (Asset, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Asset{}, ErrInvalid
	}
	c, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || c.Width < 16 || c.Height < 16 || int64(c.Width)*int64(c.Height) > 40_000_000 {
		return Asset{}, ErrInvalid
	}
	mime, ext := "", ""
	switch format {
	case "jpeg":
		mime, ext = "image/jpeg", ".jpg"
	case "png":
		mime, ext = "image/png", ".png"
	case "webp":
		mime, ext = "image/webp", ".webp"
	default:
		return Asset{}, ErrInvalid
	}
	id := idgen.New("im")
	a := Asset{AssetID: id, StudentID: student, ObjectKey: "images/" + Hash([]byte(student))[:16] + "/" + id + ext, SHA256: Hash(data), MIME: mime, Size: len(data), Width: c.Width, Height: c.Height, CreatedAt: time.Now().UTC()}
	if e = s.Files.Put(a.ObjectKey, data); e != nil {
		return Asset{}, e
	}
	saved, e := s.Repo.CreateAsset(ctx, a)
	if e != nil || saved.AssetID != a.AssetID {
		_ = s.Files.Delete(a.ObjectKey)
	}
	return saved, e
}
func (s *Service) Cached(ctx context.Context, student, id string) (Result, error) {
	if s.OCR == nil {
		return Result{}, ErrUnavailable
	}
	return s.Repo.Result(ctx, student, id, s.OCR.ConfigHash())
}
func (s *Service) Recognize(ctx context.Context, student, id string) (Result, error) {
	if s.OCR == nil {
		return Result{}, ErrUnavailable
	}
	a, e := s.Repo.Asset(ctx, student, id)
	if e != nil {
		return Result{}, e
	}
	cfg := s.OCR.ConfigHash()
	lease, cached, e := s.Repo.Claim(ctx, student, id, cfg)
	if e != nil {
		return Result{}, e
	}
	if cached != nil {
		return *cached, nil
	}
	failed := func(err error) (Result, error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Repo.Fail(c, student, id, cfg, lease)
		return Result{}, err
	}
	raw, e := s.Files.Read(a.ObjectKey, MaxImageBytes)
	if e != nil || Hash(raw) != a.SHA256 {
		return failed(ErrInvalid)
	}
	out, e := s.OCR.Recognize(ctx, raw)
	if e != nil {
		return failed(e)
	}
	if out.Text == "" || len(out.Raw) == 0 {
		return failed(ErrUnavailable)
	}
	key := "ocr/" + id + "/" + Hash(out.Raw) + ".json"
	if e = s.Files.Put(key, out.Raw); e != nil {
		return failed(e)
	}
	result := Result{AssetID: id, ImageObjectKey: a.ObjectKey, ImageHash: a.SHA256, ConfigHash: cfg, Text: out.Text, TextHash: Hash([]byte(out.Text)), RawObjectKey: key, RequestID: out.RequestID, ToolName: out.ToolName, Transport: out.Transport}
	return s.Repo.Complete(ctx, student, id, cfg, lease, result)
}
