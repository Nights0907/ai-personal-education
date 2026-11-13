package photo

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("photo: not found")
var ErrBusy = errors.New("photo: OCR is processing")
var ErrUnavailable = errors.New("photo: OCR unavailable")
var ErrInvalid = errors.New("photo: invalid image")
var ErrAmbiguousText = errors.New("photo: recognized question is ambiguous")

const MaxImageBytes = 5 << 20

type Asset struct {
	AssetID   string    `json:"asset_id"`
	StudentID string    `json:"-"`
	ObjectKey string    `json:"object_key"`
	SHA256    string    `json:"sha256"`
	MIME      string    `json:"mime"`
	Size      int       `json:"size"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CreatedAt time.Time `json:"created_at"`
}
type Result struct {
	ResultID       string `json:"ocr_result_id"`
	AssetID        string `json:"asset_id"`
	ImageObjectKey string `json:"image_object_key"`
	ImageHash      string `json:"image_hash"`
	ConfigHash     string `json:"config_hash"`
	Text           string `json:"text"`
	TextHash       string `json:"text_hash"`
	RawObjectKey   string `json:"raw_object_key"`
	RequestID      string `json:"provider_request_id"`
	ToolName       string `json:"tool_name"`
	Transport      string `json:"transport"`
}
type Output struct {
	Text                           string
	Raw                            []byte
	RequestID, ToolName, Transport string
}
type Recognizer interface {
	ConfigHash() string
	Recognize(context.Context, []byte) (Output, error)
}
type Files interface {
	Put(string, []byte) error
	Read(string, int) ([]byte, error)
	Delete(string) error
}
type Repository interface {
	CreateAsset(context.Context, Asset) (Asset, error)
	Asset(context.Context, string, string) (Asset, error)
	Result(context.Context, string, string, string) (Result, error)
	ResultByID(context.Context, string, string) (Result, error)
	Claim(context.Context, string, string, string) (string, *Result, error)
	Complete(context.Context, string, string, string, string, Result) (Result, error)
	Fail(context.Context, string, string, string, string) error
}
