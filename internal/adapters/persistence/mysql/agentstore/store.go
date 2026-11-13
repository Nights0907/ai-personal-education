// Package agentstore implements turn-atomic messages and versioned compaction.
package agentstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/musi-ai/education/internal/platform/idgen"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

//go:embed migrations/*.sql
var migrations embed.FS

type buffer struct {
	Snapshots map[string]state.Snapshot
	Items     []*schema.Message
}
type Store struct {
	DB      *sql.DB
	mu      sync.Mutex
	buffers map[string]*buffer
}

var _ storage.Store = (*Store)(nil)

type conversation struct {
	HistoryPolicy  state.Policy
	Usage          state.ContextUsage
	Scope          state.Scope
	Next           int64
	Active         *state.Run
	LastAttempt    *state.Input
	Branch         string
	Tip            state.MessageCursor
	Version, Epoch int64
	Deleted        bool
	Summary        state.Summary
	SummaryToken   state.ReadToken
	SummaryThrough state.MessageCursor
	Jobs           map[string]state.SummaryJob
	Event          *state.SealedEvent
}
type messageExt struct {
	RadarSourceID                        string          `json:",omitempty"`
	RadarSeed                            int             `json:",omitempty"`
	DynamicContext                       string          `json:",omitempty"`
	SystemPrompts                        []string        `json:",omitempty"`
	SubmittedAt                          *time.Time      `json:",omitempty"`
	RegenerateOf                         string          `json:",omitempty"`
	ImageAssetID                         string          `json:",omitempty"`
	OriginalInput                        *schema.Message `json:",omitempty"`
	InputHash, ReceiptHash, MessagesHash string
	Input                                state.Input
	QueryHash                            string `json:",omitempty"`
	// Mode and GuideQuestionID persist the teaching entry that produced this
	// message. They must round-trip through storage: later turns (guided
	// tutoring) resolve the entry mode from the stored record, not the request.
	Mode            string `json:",omitempty"`
	GuideQuestionID string `json:",omitempty"`
}

func New(db *sql.DB) *Store           { return &Store{DB: db, buffers: map[string]*buffer{}} }
func bufferKey(in state.Input) string { return in.Identity.Scope.Key() + ":" + in.Identity.RunID }
func jsonText(v any) string           { b, _ := json.Marshal(v); return string(b) }
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func inputHash(in state.Input) string {
	return state.Hash([]string{in.InputRef, in.BindingRef, in.ConfigRef})
}

type scanner interface{ Scan(...any) error }

func decode(row scanner, v any) error {
	var b []byte
	if err := row.Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) Migrate(ctx context.Context) error {
	for _, file := range []string{"migrations/003_three_tables.sql", "migrations/004_memory_compact.sql"} {
		b, err := migrations.ReadFile(file)
		if err != nil {
			return err
		}
		var lines []string
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				lines = append(lines, line)
			}
		}
		for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err = s.DB.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	if err := s.upgradePendingQueries(ctx); err != nil {
		return err
	}
	if err := s.migrateIdentifiers(ctx); err != nil {
		return err
	}
	if err := s.migrateInlineCompacts(ctx); err != nil {
		return err
	}
	return s.ValidateSchema(ctx)
}
func writeConversation(ctx context.Context, tx *sql.Tx, c conversation) error {
	var memoryDue, eventDue any
	for _, j := range c.Jobs {
		due := j.NextAttempt
		if j.State == "running" {
			due = j.LeaseUntil
		} else if j.State != "pending" && j.State != "retry" {
			continue
		}
		if due.IsZero() {
			due = time.Now().UTC()
		}
		if memoryDue == nil || due.Before(memoryDue.(time.Time)) {
			memoryDue = due
		}
	}
	if c.Event != nil && !c.Event.Failed {
		due := c.Event.NextAttempt
		if due.IsZero() {
			due = time.Now().UTC()
		}
		eventDue = due
	}
	status := "ACTIVE"
	if c.Deleted {
		status = "DELETED"
	}
	var active any
	if c.Active != nil {
		active = c.Active.Input.Identity.RunID
	}
	persisted := c
	if persisted.Summary.CompactID != "" {
		persisted.Summary.Content = ""
		persisted.Summary.Structured = nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_conversation SET current_branch_id=?,current_message_id=?,current_position=?,version=?,history_epoch=?,active_run_id=?,status=?,memory_due_at=?,event_due_at=?,ext=?,updated_at=UTC_TIMESTAMP(6) WHERE scope_key=?`, c.Branch, nullable(c.Tip.MessageID), c.Tip.Position, c.Version, c.Epoch, active, status, memoryDue, eventDue, jsonText(persisted), c.Scope.Key())
	return err
}
func (s *Store) transaction(ctx context.Context, scope state.Scope, fn func(*sql.Tx, *conversation) error) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var c conversation
	err = decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", scope.Key()), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return state.ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.Deleted {
		return state.ErrConflict
	}
	if err = fn(tx, &c); err != nil {
		return err
	}
	if err = writeConversation(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}
func validRun(c *conversation, in state.Input, live bool) error {
	if c.Active == nil || state.Hash(c.Active.Input) != state.Hash(in) || (live && !in.Lease.Until.After(time.Now())) {
		return state.ErrConflict
	}
	return nil
}
func (s *Store) Begin(ctx context.Context, in state.Input) error {
	if err := in.Validate(); err != nil {
		return err
	}
	scope := in.Identity.Scope
	root := idgen.New("b")
	c := conversation{Scope: scope, Branch: root, Epoch: 1}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_conversation(scope_key,namespace,subject_id,conversation_id,current_branch_id,ext,created_at,updated_at) VALUES(?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE scope_key=scope_key`, scope.Key(), scope.Namespace, scope.SubjectID, scope.ConversationID, root, jsonText(c))
	if err != nil {
		return err
	}
	err = s.transaction(ctx, scope, func(tx *sql.Tx, c *conversation) error {
		if c.Active != nil {
			old := c.Active.Input
			if old.Identity.TurnID != in.Identity.TurnID || old.Lease.Until.After(time.Now()) || old.Lease.Token >= in.Lease.Token {
				return state.ErrConflict
			}
		}
		if c.LastAttempt != nil && c.LastAttempt.Identity.TurnID == in.Identity.TurnID {
			if inputHash(*c.LastAttempt) != inputHash(in) || c.LastAttempt.Lease.Token >= in.Lease.Token {
				return state.ErrConflict
			}
		}
		var messageID, messageStatus, branch string
		var pendingMeta messageExt
		err := tx.QueryRowContext(ctx, "SELECT message_id,status,branch_id,ext FROM agent_message WHERE scope_key=? AND turn_id=? AND role='user'", scope.Key(), in.Identity.TurnID).Scan(&messageID, &messageStatus, &branch, newJSON(&pendingMeta))
		if err == nil {
			if messageStatus != "RECEIVED" || branch != c.Branch || (in.InputRef != messageID && (pendingMeta.Input.InputRef != in.InputRef || inputHash(pendingMeta.Input) != inputHash(in))) {
				return state.ErrConflict
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_branch(scope_key,branch_id,created_version,op,created_at) VALUES(?,?,0,'ROOT',UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE branch_id=branch_id`, scope.Key(), c.Branch); err != nil {
			return err
		}
		cursor := c.Next + 1
		if c.Active != nil {
			cursor = c.Active.Cursor
			s.mu.Lock()
			delete(s.buffers, bufferKey(c.Active.Input))
			s.mu.Unlock()
		} else {
			c.Next = cursor
		}
		c.Active = &state.Run{Input: in, Cursor: cursor, Status: "running"}
		c.LastAttempt = &in
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.buffers[bufferKey(in)] = &buffer{Snapshots: map[string]state.Snapshot{}}
	s.mu.Unlock()
	return nil
}
func (s *Store) SaveSnapshot(ctx context.Context, in state.Input, snap state.Snapshot) error {
	return s.transaction(ctx, in.Identity.Scope, func(tx *sql.Tx, c *conversation) error {
		if err := validRun(c, in, true); err != nil {
			return err
		}
		if snap.Identity != in.Identity {
			return state.ErrConflict
		}
		if snap.ReadToken.BranchID == "" {
			snap.ReadToken = token(in.Identity.Scope, *c)
		}
		if snap.ReadToken != token(in.Identity.Scope, *c) {
			return state.ErrConflict
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		b := s.buffers[bufferKey(in)]
		if b == nil {
			return state.ErrNotFound
		}
		if old, ok := b.Snapshots[snap.Key()]; ok {
			if state.Hash(old) != state.Hash(snap) {
				return state.ErrConflict
			}
			return nil
		}
		var clone state.Snapshot
		if err := json.Unmarshal([]byte(jsonText(snap)), &clone); err != nil {
			return err
		}
		b.Snapshots[snap.Key()] = clone
		if snap.Phase == "assembled" {
			c.Active.Policy = snap.Policy
			prompts := []string{}
			for _, msg := range snap.Messages {
				if msg != nil && msg.Role == schema.System {
					prompts = append(prompts, msg.Content)
				}
			}
			dynamic := snap.Preamble + "\n\n" + snap.Memory
			if snap.CanonicalInput != nil {
				prefix := snap.CanonicalInput.Content
				if index := strings.Index(prefix, "<student-input>"); index >= 0 {
					dynamic += "\n\n" + prefix[:index]
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_message SET ext=JSON_SET(ext,'$.DynamicContext',?) WHERE scope_key=? AND message_id=? AND role='user'`, dynamic, in.Identity.Scope.Key(), in.InputRef); err != nil {
				return err
			}
			if len(prompts) > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE agent_message SET ext=JSON_SET(ext,'$.SystemPrompts',CAST(? AS JSON)) WHERE scope_key=? AND message_id=? AND role='user'`, jsonText(prompts), in.Identity.Scope.Key(), in.InputRef); err != nil {
					return err
				}
			}
		}
		if snap.Usage.InputTokens > 0 {
			c.Active.Usage = snap.Usage
		}
		return nil
	})
}
func (s *Store) Append(ctx context.Context, in state.Input, n int, m *schema.Message) error {
	return s.transaction(ctx, in.Identity.Scope, func(_ *sql.Tx, c *conversation) error {
		if err := validRun(c, in, true); err != nil {
			return err
		}
		clean := state.CleanMessages([]*schema.Message{m})
		if len(clean) != 1 || n < 1 {
			return state.ErrProtocol
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		b := s.buffers[bufferKey(in)]
		if b == nil {
			return state.ErrNotFound
		}
		if n <= len(b.Items) {
			if state.Hash(b.Items[n-1]) != state.Hash(clean[0]) {
				return state.ErrConflict
			}
			return nil
		}
		if n != len(b.Items)+1 {
			return state.ErrConflict
		}
		if len(jsonText(clean[0])) > 1024*1024 {
			return fmt.Errorf("agent store: item exceeds 1 MiB")
		}
		b.Items = append(b.Items, clean[0])
		return nil
	})
}

func (s *Store) Finish(ctx context.Context, in state.Input, status, code string, receipt *state.Receipt, ms []*schema.Message) error {
	ms = state.CleanMessages(ms)
	err := s.transaction(ctx, in.Identity.Scope, func(tx *sql.Tx, c *conversation) error {
		var old messageExt
		err := decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_message WHERE scope_key=? AND turn_id=? AND role='assistant'", in.Identity.Scope.Key(), in.Identity.TurnID), &old)
		if err == nil {
			if status != "succeeded" || old.ReceiptHash != state.Hash(receipt) || old.MessagesHash != state.Hash(ms) || state.Hash(old.Input) != state.Hash(in) {
				return state.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = validRun(c, in, false); err != nil {
			return err
		}
		if status == "failed" {
			c.Active = nil
			return nil
		}
		if status != "succeeded" {
			return state.ErrConflict
		}
		if receipt == nil || receipt.ResultID == "" || receipt.TurnID != in.Identity.TurnID || receipt.RunID != in.Identity.RunID {
			return state.ErrIncomplete
		}
		if len(ms) < 2 || ms[0].Role != schema.User {
			return state.ErrProtocol
		}
		for _, m := range ms[1:] {
			if m.Role == schema.User {
				return state.ErrProtocol
			}
		}
		if err = state.ValidateProtocol(ms); err != nil {
			return err
		}
		restored, err := state.RestoreTerminal(ms, receipt)
		if err != nil {
			return err
		}
		if state.Hash(restored) != state.Hash(ms) {
			return state.ErrProtocol
		}
		c.Version++
		meta := messageExt{InputHash: inputHash(in), ReceiptHash: state.Hash(receipt), MessagesHash: state.Hash(ms), Input: in}
		var pendingID, pendingStatus string
		var pendingContent []byte
		var pendingMeta messageExt
		err = tx.QueryRowContext(ctx, "SELECT message_id,status,content,ext FROM agent_message WHERE scope_key=? AND turn_id=? AND role='user'", in.Identity.Scope.Key(), in.Identity.TurnID).Scan(&pendingID, &pendingStatus, &pendingContent, newJSON(&pendingMeta))
		if err == nil {
			var block struct {
				Items []*schema.Message `json:"items"`
			}
			if e := json.Unmarshal(pendingContent, &block); e != nil {
				return e
			}
			expected := ms[:1]
			if receipt.OriginalInput != nil {
				if receipt.OriginalInput.Role != schema.User {
					return state.ErrProtocol
				}
				expected = state.CleanMessages([]*schema.Message{receipt.OriginalInput})
			}
			s.mu.Lock()
			buf := s.buffers[bufferKey(in)]
			if buf != nil {
				if snap, ok := buf.Snapshots["assembled:0"]; ok && snap.OriginalInput != nil {
					expected = state.CleanMessages([]*schema.Message{snap.OriginalInput})
				}
			}
			s.mu.Unlock()
			if pendingStatus != "RECEIVED" || state.Hash(block.Items) != state.Hash(expected) {
				return state.ErrProtocol
			}
			// Preserve the raw UI input separately; native history stores the exact
			// UserPrefix + input that the model consumed.
			meta.OriginalInput = state.CleanMessages(block.Items)[0]

			meta.QueryHash = pendingMeta.QueryHash
			meta.ImageAssetID = pendingMeta.ImageAssetID
			meta.RegenerateOf = pendingMeta.RegenerateOf
			meta.SubmittedAt = pendingMeta.SubmittedAt
			meta.SystemPrompts = pendingMeta.SystemPrompts
			meta.DynamicContext = pendingMeta.DynamicContext
			meta.RadarSourceID = pendingMeta.RadarSourceID
			meta.RadarSeed = pendingMeta.RadarSeed
			meta.Mode = pendingMeta.Mode
			meta.GuideQuestionID = pendingMeta.GuideQuestionID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ext := jsonText(meta)
		for n, items := range [][]*schema.Message{ms[:1], ms[1:]} {
			role := []string{"user", "assistant"}[n]
			content := jsonText(struct {
				Items []*schema.Message `json:"items"`
			}{items})
			if len(content) > 1024*1024 {
				return fmt.Errorf("agent store: block exceeds 1 MiB")
			}
			id := state.MessageID(in.Identity.Scope, in.Identity.TurnID, role)
			contentHash := state.Hash(struct{ Schema, Payload string }{"eino-text-v1", content})
			prefix := state.Hash([]string{c.Tip.PrefixHash, id, "eino-text-v1", contentHash})
			parent := c.Tip.MessageID
			if parent == "" {
				var inherited sql.NullString
				if err = tx.QueryRowContext(ctx, "SELECT fork_parent_message_id FROM agent_branch WHERE scope_key=? AND branch_id=?", in.Identity.Scope.Key(), c.Branch).Scan(&inherited); err != nil {
					return err
				}
				parent = inherited.String
			}
			position := c.Tip.Position + 1
			if n == 0 && pendingID != "" {
				if id != pendingID {
					return state.ErrConflict
				}
				_, err = tx.ExecContext(ctx, `UPDATE agent_message SET parent_message_id=?,position=?,version=?,status='SEALED',content=?,content_hash=?,ext=?,run_id=?,turn_cursor=?,prefix_hash=? WHERE scope_key=? AND message_id=? AND status='RECEIVED'`, nullable(parent), position, c.Version, content, contentHash, ext, in.Identity.RunID, c.Active.Cursor, prefix, in.Identity.Scope.Key(), id)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO agent_message(scope_key,message_id,branch_id,parent_message_id,position,version,role,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))`, in.Identity.Scope.Key(), id, c.Branch, nullable(parent), position, c.Version, role, content, ext, in.Identity.TurnID, in.Identity.RunID, c.Active.Cursor, contentHash, prefix)
			}
			if err != nil {
				return err
			}
			c.Tip = state.MessageCursor{BranchID: c.Branch, MessageID: id, Position: position, PrefixHash: prefix}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE agent_branch SET last_position=? WHERE scope_key=? AND branch_id=?", c.Tip.Position, in.Identity.Scope.Key(), c.Branch); err != nil {
			return err
		}
		c.Usage = c.Active.Usage
		c.HistoryPolicy = c.Active.Policy
		c.Event = &state.SealedEvent{ID: state.Hash([]string{in.Identity.Scope.Key(), in.Identity.TurnID, "turn_sealed"}), Scope: in.Identity.Scope, Policy: c.Active.Policy, TurnID: in.Identity.TurnID}
		c.Active = nil
		return nil
	})
	if err == nil {
		s.mu.Lock()
		delete(s.buffers, bufferKey(in))
		s.mu.Unlock()
	}
	return err
}
