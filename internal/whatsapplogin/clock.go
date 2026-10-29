package whatsapplogin

import (
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/agenttools"
)

// sessionPhone is the number a session is logged in as, in bare digits, or ""
// before the login has paired.
func sessionPhone(session *loginSession) string {
	if session == nil || session.client == nil || session.client.Store == nil || session.client.Store.ID == nil {
		return ""
	}
	return session.client.Store.ID.User
}

// withDateContext puts today's date, in the agent's time zone, in front of its
// prompt. An agent with tools that take dates is otherwise left guessing what
// "tomorrow" is, and the guess is what gets sent to the endpoint.
func withDateContext(prompt string, loc *time.Location) string {
	line := agenttools.DateContext(time.Now(), loc)
	if strings.TrimSpace(prompt) == "" {
		return line
	}
	return line + "\n\n" + prompt
}
