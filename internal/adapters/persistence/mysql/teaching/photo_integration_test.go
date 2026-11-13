package teaching

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/adapters/assets/local"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/photos"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"sync/atomic"
	"testing"
)

type countedOCR struct {
	calls atomic.Int32
	fail  bool
}

func (*countedOCR) ConfigHash() string { return photo.Hash([]byte("test-config")) }
func (c *countedOCR) Recognize(context.Context, []byte) (photo.Output, error) {
	c.calls.Add(1)
	if c.fail {
		return photo.Output{}, photo.ErrUnavailable
	}
	return photo.Output{Text: "解方程 x+1=2。", Raw: []byte(`{"success":true,"ret":[{"word":"解方程 x+1=2。"}]}`), ToolName: "通用识别GPU", Transport: "streamableHttp"}, nil
}
func TestPhotoUploadOCRSolveAndReplay(t *testing.T) {
	ctx := context.Background()
	db := dbForTest(t)
	// Commit must read cached OCR on its own connection, even with no spare
	// connection available. This catches nested checkout deadlocks.
	db.SetMaxOpenConns(1)
	repo := photos.New(db)
	if e := repo.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	files, e := local.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	ocr := &countedOCR{}
	p := &photo.Service{Repo: repo, Files: files, OCR: ocr}
	svc, server, q := setup(t, db, &testModel{}, &testModel{review: true}, p)
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 32, 32)))
	upload := func() photo.Asset {
		t.Helper()
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		f, _ := w.CreateFormFile("image", "test.png")
		f.Write(imageBytes.Bytes())
		w.Close()
		response, e := http.Post(server.URL+"/api/v1/images", w.FormDataContentType(), &b)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var a photo.Asset
		if response.StatusCode != 201 || json.NewDecoder(response.Body).Decode(&a) != nil {
			t.Fatal("upload failed", response.StatusCode)
		}
		return a
	}
	a := upload()
	if upload().AssetID != a.AssetID {
		t.Fatal("same image not deduplicated")
	}
	if _, e = p.Repo.Asset(ctx, "bob", a.AssetID); !errors.Is(e, photo.ErrNotFound) {
		t.Fatal("cross student image access", e)
	}
	c, e := q.CreateConversation(ctx, "alice", "photo-conversation")
	if e != nil {
		t.Fatal(e)
	}
	input := query.Input{Query: "这是我的题目", ConversationID: c.ConversationID, ClientRequestID: "photo-once", ImageAssetID: a.AssetID}
	raw, _ := json.Marshal(input)
	var first solve.View
	for i := 0; i < 2; i++ {
		response, e := http.Post(server.URL+"/api/v1/solve", "application/json", bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(response.Body)
		response.Body.Close()
		var v solve.View
		if response.StatusCode != 200 || json.Unmarshal(b, &v) != nil || !v.Committed {
			t.Fatalf("photo solve %d %s", response.StatusCode, b)
		}
		if i == 0 {
			first = v
		} else if v.QuestionID != first.QuestionID {
			t.Fatal("duplicate solve")
		}
	}
	if ocr.calls.Load() != 1 || first.InputType != "image" || first.OriginalText != "解方程 x+1=2。" || first.SourceImageObjectKey == nil || first.OCRResultObjectKey == nil {
		t.Fatal(first, ocr.calls.Load())
	}
	record, e := q.Get(ctx, "alice", first.SourceMessageID)
	if e != nil || record.Query != input.Query || record.ImageAssetID != a.AssetID || record.Status != "ANSWERED" {
		t.Fatal("raw image reference lost", record, e)
	}
	saved, e := svc.Repo.Find(ctx, state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: c.ConversationID}, record.TurnID)
	if e != nil || saved.Receipt.OriginalInput.Content != input.Query {
		t.Fatal("raw receipt lost", e)
	}
	var text, imgKey, ocrKey string
	if e = db.QueryRow("SELECT original_text,source_image_object_key,ocr_result_object_key FROM teaching_question WHERE question_id=?", first.QuestionID).Scan(&text, &imgKey, &ocrKey); e != nil || text != first.OriginalText || imgKey != *first.SourceImageObjectKey || ocrKey != *first.OCRResultObjectKey {
		t.Fatal("source columns mismatch", e)
	}
	if _, e = files.Read(ocrKey, 1<<20); e != nil {
		t.Fatal("raw OCR missing", e)
	}
	v, e := p.Recognize(ctx, "alice", a.AssetID)
	if e != nil || v.Text != first.OriginalText || ocr.calls.Load() != 1 {
		t.Fatal("cached OCR lost", e)
	}
	if _, e = p.Repo.ResultByID(ctx, "bob", v.ResultID); !errors.Is(e, photo.ErrNotFound) {
		t.Fatal("cross-student OCR", e)
	}
	// An active claim cannot be stolen or completed by another lease.
	cfg := photo.Hash([]byte("another config"))
	lease, _, e := repo.Claim(ctx, "alice", a.AssetID, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = repo.Claim(ctx, "alice", a.AssetID, cfg); !errors.Is(e, photo.ErrBusy) {
		t.Fatal("duplicate concurrent OCR", e)
	}
	if e = repo.Fail(ctx, "alice", a.AssetID, cfg, "wrong-lease"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = repo.Claim(ctx, "alice", a.AssetID, cfg); !errors.Is(e, photo.ErrBusy) {
		t.Fatal("stale failure released lease")
	}
	if e = repo.Fail(ctx, "alice", a.AssetID, cfg, lease); e != nil {
		t.Fatal(e)
	}
}

func TestOCRFailurePreservesImageAndRawMessage(t *testing.T) {
	ctx := context.Background()
	db := dbForTest(t)
	repo := photos.New(db)
	if e := repo.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	files, e := local.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	ocr := &countedOCR{fail: true}
	p := &photo.Service{Repo: repo, Files: files, OCR: ocr}
	review := &testModel{review: true}
	_, server, q := setup(t, db, &testModel{}, review, p)
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 32)))
	a, e := p.Upload(ctx, "alice", b.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	c, e := q.CreateConversation(ctx, "alice", "failed-ocr")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(query.Input{ConversationID: c.ConversationID, ClientRequestID: "failure", ImageAssetID: a.AssetID})
	response, e := http.Post(server.URL+"/api/v1/solve", "application/json", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal(response.StatusCode)
	}
	for table, want := range map[string]int{"agent_message": 1, "teaching_question": 0, "teaching_solution_draft": 0, "teaching_solution_receipt": 0} {
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); e != nil || n != want {
			t.Fatal(table, n, e)
		}
	}
	if ocr.calls.Load() != 1 || review.calls.Load() != 0 {
		t.Fatal("failure retried or reviewed", ocr.calls.Load(), review.calls.Load())
	}
	if _, e = files.Read(a.ObjectKey, photo.MaxImageBytes); e != nil {
		t.Fatal("original image lost", e)
	}
	records, _, e := q.List(ctx, "alice", c.ConversationID, nil, 10)
	if e != nil || len(records) != 1 || records[0].ImageAssetID != a.AssetID || records[0].Query != "[图片题目]" {
		t.Fatal("raw submission lost", records, e)
	}
	if _, e = p.Cached(ctx, "alice", a.AssetID); !errors.Is(e, photo.ErrNotFound) {
		t.Fatal("failure cached as success", e)
	}
}
