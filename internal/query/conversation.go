package query

import "context"

type Conversation struct {
	ConversationID string `json:"conversation_id"`
	BranchID       string `json:"branch_id"`
}
type ConversationCreator interface {
	CreateConversation(context.Context, string, string) (Conversation, error)
}

// ConversationDeleter hides a conversation without purging its stored evidence.
type ConversationDeleter interface {
	DeleteConversation(context.Context, string, string) error
}
