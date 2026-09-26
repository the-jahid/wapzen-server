package agents

import (
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// returningColumns is the full, ordered column list read back after every write
// and on every read, so a response reflects the values Postgres actually
// persisted (including every column that fell back to its schema default). It
// is the column order agentRow and scanAgentRow follow.
const returningColumns = `
	id, created_at, updated_at,
	agent_name, language, timezone, phone_number_id, call_direction, status,
	model_provider, model_name, model_temperature,
	prompt_begin_message_mode, prompt_begin_message, prompt_begin_message_delay_ms,
	prompt_system_prompt,
	voice_provider,
	voice_elevenlabs_voice_id, voice_elevenlabs_voice_name, voice_elevenlabs_voice_model,
	voice_openai_voice_id, voice_openai_voice_name, voice_openai_voice_model, voice_openai_realtime_model, voice_openai_instructions,
	voice_openai_speed, voice_openai_volume,
	transcriber_provider, transcriber_language, transcriber_openai_model, transcriber_elevenlabs_model,
	post_call_analysis_provider, post_call_analysis_model
`

// agentRow mirrors returningColumns. Columns that are nullable in the schema use
// pointer types so a SQL NULL scans cleanly.
type agentRow struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time

	AgentName     string
	Language      string
	Timezone      *string
	PhoneNumberID *string
	CallDirection string
	Status        string

	ModelProvider    string
	ModelName        string
	ModelTemperature *float64

	PromptBeginMessageMode    string
	PromptBeginMessage        *string
	PromptBeginMessageDelayMs *int
	PromptSystemPrompt        *string

	VoiceProvider             *string
	VoiceElevenlabsVoiceID    string
	VoiceElevenlabsVoiceName  string
	VoiceElevenlabsVoiceModel string
	VoiceOpenAIVoiceID        string
	VoiceOpenAIVoiceName      string
	VoiceOpenAIVoiceModel     string
	VoiceOpenAIRealtimeModel  string
	VoiceOpenAIInstructions   *string
	VoiceOpenAISpeed          float64
	VoiceOpenAIVolume         float64

	TranscriberProvider        string
	TranscriberLanguage        string
	TranscriberOpenAIModel     string
	TranscriberElevenlabsModel string

	PostCallAnalysisProvider string
	PostCallAnalysisModel    *string
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgentRow(row rowScanner, dst *agentRow) error {
	return row.Scan(
		&dst.ID, &dst.CreatedAt, &dst.UpdatedAt,
		&dst.AgentName, &dst.Language, &dst.Timezone, &dst.PhoneNumberID, &dst.CallDirection, &dst.Status,
		&dst.ModelProvider, &dst.ModelName, &dst.ModelTemperature,
		&dst.PromptBeginMessageMode, &dst.PromptBeginMessage, &dst.PromptBeginMessageDelayMs,
		&dst.PromptSystemPrompt,
		&dst.VoiceProvider,
		&dst.VoiceElevenlabsVoiceID, &dst.VoiceElevenlabsVoiceName, &dst.VoiceElevenlabsVoiceModel,
		&dst.VoiceOpenAIVoiceID, &dst.VoiceOpenAIVoiceName, &dst.VoiceOpenAIVoiceModel, &dst.VoiceOpenAIRealtimeModel, &dst.VoiceOpenAIInstructions,
		&dst.VoiceOpenAISpeed, &dst.VoiceOpenAIVolume,
		&dst.TranscriberProvider, &dst.TranscriberLanguage, &dst.TranscriberOpenAIModel, &dst.TranscriberElevenlabsModel,
		&dst.PostCallAnalysisProvider, &dst.PostCallAnalysisModel,
	)
}

// buildResource assembles the documented AgentResource from the persisted
// scalar row plus its attachments (knowledge bases and tools).
//
// knowledgeBaseIDs and toolIDs are normalized to empty slices so an agent with
// nothing attached renders [] rather than null — the difference would otherwise
// read as "unknown" to a client, and there is no such state.
func buildResource(row agentRow, knowledgeBaseIDs, toolIDs []string) types.AgentResource {
	if knowledgeBaseIDs == nil {
		knowledgeBaseIDs = []string{}
	}
	if toolIDs == nil {
		toolIDs = []string{}
	}
	return types.AgentResource{
		ID:        row.ID,
		CreatedAt: row.CreatedAt.Format(time.RFC3339),
		UpdatedAt: row.UpdatedAt.Format(time.RFC3339),
		CreateAgentRequest: types.CreateAgentRequest{
			Agent: &types.AgentSection{
				Name:          row.AgentName,
				Language:      row.Language,
				Timezone:      row.Timezone,
				PhoneNumberID: row.PhoneNumberID,
				CallDirection: row.CallDirection,
				Status:        row.Status,
			},
			Model: &types.ModelSection{
				Provider:    row.ModelProvider,
				Name:        row.ModelName,
				Temperature: deref(row.ModelTemperature),
			},
			Prompt: &types.PromptSection{
				BeginMessageMode:    row.PromptBeginMessageMode,
				BeginMessage:        deref(row.PromptBeginMessage),
				BeginMessageDelayMs: deref(row.PromptBeginMessageDelayMs),
				SystemPrompt:        deref(row.PromptSystemPrompt),
			},
			Voice: &types.VoiceSection{
				Provider: deref(row.VoiceProvider),
				ElevenLabs: &types.ElevenLabsVoice{
					VoiceID:    row.VoiceElevenlabsVoiceID,
					VoiceName:  row.VoiceElevenlabsVoiceName,
					VoiceModel: row.VoiceElevenlabsVoiceModel,
				},
				OpenAI: &types.OpenAIVoice{
					VoiceID:       row.VoiceOpenAIVoiceID,
					VoiceName:     row.VoiceOpenAIVoiceName,
					VoiceModel:    row.VoiceOpenAIVoiceModel,
					RealtimeModel: row.VoiceOpenAIRealtimeModel,
					Instructions:  row.VoiceOpenAIInstructions,
					Speed:         row.VoiceOpenAISpeed,
					Volume:        row.VoiceOpenAIVolume,
				},
			},
			Transcriber: &types.TranscriberSection{
				Provider:   row.TranscriberProvider,
				Language:   row.TranscriberLanguage,
				OpenAI:     &types.TranscriberOpenAI{Model: row.TranscriberOpenAIModel},
				ElevenLabs: &types.TranscriberElevenLabs{Model: row.TranscriberElevenlabsModel},
			},
			PostCall: &types.PostCallSection{
				AnalysisProvider: row.PostCallAnalysisProvider,
				AnalysisModel:    row.PostCallAnalysisModel,
			},
			KnowledgeBase: &types.KnowledgeBaseSection{KnowledgeBaseIDs: knowledgeBaseIDs},
			Tools:         &types.ToolsSection{ToolIDs: toolIDs},
		},
	}
}

// columnSet accumulates the column/value pairs of a dynamic INSERT or UPDATE, so
// only the columns a request actually carries are written and everything else
// keeps its schema default (on insert) or stored value (on update).
type columnSet struct {
	cols []string
	args []any
}

func (s *columnSet) add(col string, val any) {
	s.cols = append(s.cols, col)
	s.args = append(s.args, val)
}

// has reports whether the set writes the given column.
func (s *columnSet) has(col string) bool {
	for _, c := range s.cols {
		if c == col {
			return true
		}
	}
	return false
}

// addNonZero writes col only when v is not its type's zero value — how a create
// request says "use the default" for a plain field.
func addNonZero[T comparable](s *columnSet, col string, v T) {
	var zero T
	if v != zero {
		s.add(col, v)
	}
}

// addNonNil writes col only when v is set — how a create request says "use the
// default" for a nullable field.
func addNonNil[T any](s *columnSet, col string, v *T) {
	if v != nil {
		s.add(col, v)
	}
}

// normalizePhoneNumberID trims an optional phone-number assignment, treating a
// blank string as no assignment.
func normalizePhoneNumberID(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// hasPhoneNumberAssigned reports whether a phone_number_id holds a real
// assignment rather than NULL or a blank string.
func hasPhoneNumberAssigned(id *string) bool {
	return normalizePhoneNumberID(id) != nil
}

// samePhoneNumber reports whether two optional phone-number assignments are
// equal after trimming (both unset counts as equal).
func samePhoneNumber(a, b *string) bool {
	return deref(normalizePhoneNumberID(a)) == deref(normalizePhoneNumberID(b))
}

// deref returns the pointed-to value or the zero value when the pointer is nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
