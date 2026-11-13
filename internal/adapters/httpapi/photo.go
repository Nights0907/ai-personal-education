package httpapi

import (
	"errors"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/query"
	"io"
	"net/http"
)

func (s Server) checkImage(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == "" {
		return true
	}
	if s.Photos == nil || s.Photos.OCR == nil {
		failure(w, 503, "拍照识别尚未配置")
		return false
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	if _, e := s.Photos.Repo.Asset(ctx, s.StudentID, id); e != nil {
		photoError(w, e)
		return false
	}
	return true
}
func photoError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, photo.ErrNotFound):
		failure(w, 404, "图片不存在")
	case errors.Is(e, photo.ErrInvalid):
		failure(w, 400, "请上传清晰完整的单题图片，支持 JPG、PNG、WebP，最大5MB")
	case errors.Is(e, photo.ErrBusy):
		failure(w, 409, "图片正在识别，请稍后重试")
	default:
		failure(w, 503, "暂时无法处理图片，请稍后重试")
	}
}
func (s Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	if s.Photos == nil || s.Photos.OCR == nil {
		failure(w, 503, "拍照识别尚未配置")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, photo.MaxImageBytes+65536)
	if e := r.ParseMultipartForm(photo.MaxImageBytes); e != nil {
		photoError(w, photo.ErrInvalid)
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, e := r.FormFile("image")
	if e != nil {
		photoError(w, photo.ErrInvalid)
		return
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, photo.MaxImageBytes+1))
	if e != nil {
		photoError(w, photo.ErrInvalid)
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	a, e := s.Photos.Upload(ctx, s.StudentID, b)
	if e != nil {
		photoError(w, e)
		return
	}
	writeJSON(w, 201, a)
}
func (s Server) imageContent(w http.ResponseWriter, r *http.Request) {
	if s.Photos == nil {
		photoError(w, photo.ErrNotFound)
		return
	}
	id := r.PathValue("image")
	if !query.ValidID(id) {
		photoError(w, photo.ErrNotFound)
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	a, e := s.Photos.Repo.Asset(ctx, s.StudentID, id)
	if e != nil {
		photoError(w, e)
		return
	}
	b, e := s.Photos.Files.Read(a.ObjectKey, photo.MaxImageBytes)
	if e != nil || photo.Hash(b) != a.SHA256 {
		photoError(w, photo.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", a.MIME)
	w.Header().Set("Content-Disposition", "inline")
	w.Write(b)
}
func (s Server) imageMetadata(w http.ResponseWriter, r *http.Request) {
	if s.Photos == nil {
		photoError(w, photo.ErrNotFound)
		return
	}
	ctx, cancel := contextTimeout(r)
	defer cancel()
	a, e := s.Photos.Repo.Asset(ctx, s.StudentID, r.PathValue("image"))
	if e != nil {
		photoError(w, e)
		return
	}
	var o *photo.Result
	if v, e := s.Photos.Cached(ctx, s.StudentID, a.AssetID); e == nil {
		o = &v
	} else if !errors.Is(e, photo.ErrNotFound) && !errors.Is(e, photo.ErrUnavailable) {
		photoError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"image": a, "ocr": o})
}
