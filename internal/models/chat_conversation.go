package models

import "time"

// Chat message roles, mirroring the chat_messages.role CHECK constraint.
const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"
)

// Chat conversation statuses, mirroring the chat_conversations.status CHECK
// constraint. A thread is open until somebody closes it; nothing closes one
// automatically, because a WhatsApp thread has no hangup.
const (
	ChatConversationStatusOpen   = "open"
	ChatConversationStatusClosed = "closed"
)

// ChatConversation is one saved WhatsApp text thread between a chat agent and a
// contact, persisted in the chat_conversations table.
//
// PhoneNumberID and ChatAgentID are nullable: the number may have been unpaired
// or the agent deleted since, and neither is a reason to lose what was said.
// MessageCount, LastMessageRole and LastMessageAt are maintained by a database
// trigger as messages are appended. Messages holds the transcript; it is
// populated by the Get endpoint and omitted from list responses.
type ChatConversation struct {
	ID              string        `json:"id" example:"c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"`
	UserID          string        `json:"-"`
	PhoneNumberID   *string       `json:"phone_number_id" example:"phone_number_12345"`
	ChatAgentID     *string       `json:"chat_agent_id" example:"chat_agent_12345"`
	PeerJID         string        `json:"peer_jid" example:"15557654321@s.whatsapp.net"`
	PeerPhone       *string       `json:"peer_phone" example:"+15557654321"`
	PeerName        *string       `json:"peer_name" example:"Alex Doe"`
	Status          string        `json:"status" example:"open"`
	MessageCount    int           `json:"message_count" example:"6"`
	LastMessageRole *string       `json:"last_message_role" example:"assistant"`
	LastMessageAt   *time.Time    `json:"last_message_at" example:"2026-08-30T10:04:11Z"`
	CreatedAt       time.Time     `json:"created_at" example:"2026-08-30T10:03:52Z"`
	UpdatedAt       time.Time     `json:"updated_at" example:"2026-08-30T10:04:11Z"`
	Messages        []ChatMessage `json:"messages,omitempty"`
}

// ChatMessage is one turn of a saved chat thread. Role is ChatRoleUser (the
// contact) or ChatRoleAssistant (the agent). Seq is the turn's position within
// the thread, which is what the transcript is ordered by — two rapid turns can
// share a CreatedAt.
type ChatMessage struct {
	Seq       int       `json:"seq" example:"0"`
	Role      string    `json:"role" example:"user"`
	Content   string    `json:"content" example:"Do you deliver on Sundays?"`
	CreatedAt time.Time `json:"created_at" example:"2026-08-30T10:03:52Z"`
}

// NewChatTurn is one exchange to append to a thread: the message that arrived
// and the reply that was sent. It carries everything needed to find the thread
// or open it, so the runtime records a turn in one call.
//
// UserID, PhoneNumberID, PeerJID and UserMessage are always set. ChatAgentID,
// PeerPhone, PeerName and WAMessageID are optional. AssistantMessage is empty
// when the agent did not answer, in which case only the inbound turn is saved.
type NewChatTurn struct {
	UserID           string
	PhoneNumberID    string
	ChatAgentID      string
	PeerJID          string
	PeerPhone        string
	PeerName         string
	WAMessageID      string
	UserMessage      string
	AssistantMessage string
}
