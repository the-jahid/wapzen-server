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
// and the transcriber/voice settings that control how the call sounds. It is
// populated only for an agent that is live and handles the call's direction.
type InboundAgent struct {
	ID               string
	ModelProvider    string
	ModelName        string
	ModelTemperature float64
	SystemPrompt     string
	Language         string

	// BeginMessageMode is one of the constants.BeginMessageModeOptions values:
	// "agent_speaks_first" (speak WelcomeMessage verbatim), "agent_waits_for_user"
	// (say nothing until the caller speaks), or
	// "agent_speaks_first_with_model_generated_message" (speak an AI-generated
	// opening line).
	BeginMessageMode string
	WelcomeMessage   string
	WelcomeDelayMs   int

	// The provider-specific model is passed to the matching live-call backend.
	TranscriberProvider        string
	TranscriberOpenAIModel     string
	TranscriberElevenLabsModel string

	// VoiceProvider selects the live backend and its voice settings.
	VoiceProvider            string
	VoiceElevenLabsVoiceID   string
	VoiceElevenLabsModel     string
	VoiceOpenAIVoiceID       string
	VoiceOpenAIVoiceModel    string
	VoiceOpenAIRealtimeModel string
	VoiceOpenAIInstructions  string

	// VoiceOpenAISpeed is the spoken rate (1 = normal) and VoiceOpenAIVolume the
	// playback gain (1 = unchanged) for the call.
	VoiceOpenAISpeed  float64
	VoiceOpenAIVolume float64
}

// LiveInboundAgentForPhoneNumber returns the active, inbound-capable agent
// assigned to phoneNumberID, or (nil, nil) when none exists — no agent is
// assigned, the assigned agent is paused (status != active), or it only handles
// outbound calls.
func (r *Repository) LiveInboundAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (*InboundAgent, error) {
	return r.liveAgentForPhoneNumber(ctx, phoneNumberID, "inbound")
}

// LiveOutboundAgentForPhoneNumber is the outbound twin of
// LiveInboundAgentForPhoneNumber. It backs the Create Call endpoint, which
// derives the answering agent from the number the call is placed from.
func (r *Repository) LiveOutboundAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (*InboundAgent, error) {
	return r.liveAgentForPhoneNumber(ctx, phoneNumberID, "outbound")
}

// liveAgentForPhoneNumber loads the live agent assigned to phoneNumberID that
// handles calls in direction ("inbound" or "outbound"). An agent set to "both"
// matches either direction. A phone number can be assigned to at most one agent,
// so the direction filter only ever excludes that one agent.
func (r *Repository) liveAgentForPhoneNumber(ctx context.Context, phoneNumberID, direction string) (*InboundAgent, error) {
	const q = `
		SELECT id, model_provider, model_name, model_temperature,
			prompt_system_prompt, language,
			prompt_begin_message_mode, prompt_begin_message, prompt_begin_message_delay_ms,
			transcriber_provider, transcriber_openai_model, transcriber_elevenlabs_model,
			voice_provider, voice_elevenlabs_voice_id, voice_elevenlabs_voice_model,
			voice_openai_voice_id, voice_openai_voice_model, voice_openai_realtime_model, voice_openai_instructions,
			voice_openai_speed, voice_openai_volume
		FROM agents
		WHERE phone_number_id = $1
			AND status = 'active'
			AND call_direction IN ($2, 'both')
		LIMIT 1`

	var (
		agent               InboundAgent
		systemPrompt        *string
		welcomeMessage      *string
		welcomeDelayMs      *int
		voiceOpenAIInstruct *string
	)
	err := r.pool.QueryRow(ctx, q, phoneNumberID, direction).Scan(
		&agent.ID, &agent.ModelProvider, &agent.ModelName, &agent.ModelTemperature,
		&systemPrompt, &agent.Language,
		&agent.BeginMessageMode, &welcomeMessage, &welcomeDelayMs,
		&agent.TranscriberProvider, &agent.TranscriberOpenAIModel, &agent.TranscriberElevenLabsModel,
		&agent.VoiceProvider, &agent.VoiceElevenLabsVoiceID, &agent.VoiceElevenLabsModel,
		&agent.VoiceOpenAIVoiceID, &agent.VoiceOpenAIVoiceModel, &agent.VoiceOpenAIRealtimeModel, &voiceOpenAIInstruct,
		&agent.VoiceOpenAISpeed, &agent.VoiceOpenAIVolume,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select live %s agent: %w", direction, err)
	}
	agent.SystemPrompt = deref(systemPrompt)
	agent.WelcomeMessage = deref(welcomeMessage)
	agent.WelcomeDelayMs = deref(welcomeDelayMs)
	agent.VoiceOpenAIInstructions = deref(voiceOpenAIInstruct)
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
