package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The lookups in this file back live call handling (whatsapplogin.Manager), not
// the HTTP API. They are unscoped by user on purpose: call handling only knows
// the phone number a call arrived on, and every agent referencing a phone number
// is owned by that number's owner anyway (enforced on assignment).

// InboundAgent is the minimal agent configuration needed to answer a call: the
// prompt fields that shape the AI's behaviour — the General Prompt
// (SystemPrompt) — the welcome-message settings that control how the call opens,
// and the Realtime voice settings that control how the call sounds. It is
// populated only for an agent that is live and handles the call's direction.
type InboundAgent struct {
	ID           string
	SystemPrompt string
	Language     string
	// Timezone is the IANA zone the agent's conversations are dated in, or ""
	// to take it from the country of the number it answers on.
	Timezone string

	// BeginMessageMode is one of the constants.BeginMessageModeOptions values:
	// "agent_speaks_first" (speak WelcomeMessage verbatim), "agent_waits_for_user"
	// (say nothing until the caller speaks), or
	// "agent_speaks_first_with_model_generated_message" (speak an AI-generated
	// opening line).
	BeginMessageMode string
	WelcomeMessage   string
	WelcomeDelayMs   int

	// The OpenAI Realtime voice and model the call runs on.
	VoiceOpenAIVoiceID       string
	VoiceOpenAIRealtimeModel string

	// VoiceOpenAISpeed is the spoken rate (1 = normal) and VoiceOpenAIVolume the
	// playback gain (1 = unchanged) for the call.
	VoiceOpenAISpeed  float64
	VoiceOpenAIVolume float64
}

// LiveAgentForPhoneNumber returns the active agent assigned to phoneNumberID,
// or (nil, nil) when none exists — no agent is assigned, or the assigned agent
// is paused (status != active). Every agent both answers calls to its number
// and places calls from it, so the same agent serves either direction.
func (r *Repository) LiveAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (*InboundAgent, error) {
	const q = `
		SELECT id,
			prompt_system_prompt, language, timezone,
			prompt_begin_message_mode, prompt_begin_message, prompt_begin_message_delay_ms,
			voice_openai_voice_id, voice_openai_realtime_model,
			voice_openai_speed, voice_openai_volume
		FROM agents
		WHERE phone_number_id = $1
			AND status = 'active'
			AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = agents.user_id AND u.blocked_at IS NOT NULL)
		LIMIT 1`

	var (
		agent          InboundAgent
		systemPrompt   *string
		welcomeMessage *string
		welcomeDelayMs *int
		timezone       *string
	)
	err := r.pool.QueryRow(ctx, q, phoneNumberID).Scan(
		&agent.ID,
		&systemPrompt, &agent.Language, &timezone,
		&agent.BeginMessageMode, &welcomeMessage, &welcomeDelayMs,
		&agent.VoiceOpenAIVoiceID, &agent.VoiceOpenAIRealtimeModel,
		&agent.VoiceOpenAISpeed, &agent.VoiceOpenAIVolume,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select live agent: %w", err)
	}
	agent.SystemPrompt = deref(systemPrompt)
	agent.WelcomeMessage = deref(welcomeMessage)
	agent.WelcomeDelayMs = deref(welcomeDelayMs)
	agent.Timezone = deref(timezone)
	return &agent, nil
}

// HasAgentForPhoneNumber reports whether any agent is assigned to phoneNumberID.
// It is the coarse gate for inbound-call handling: a number with no agent runs no
// call handler at all (see whatsapplogin.Manager.shouldAttachInbound), while the
// finer active/inbound-direction check happens per call.
func (r *Repository) HasAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM agents WHERE phone_number_id = $1)`
	var exists bool
	if err := r.pool.QueryRow(ctx, q, phoneNumberID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check agent assignment: %w", err)
	}
	return exists, nil
}
