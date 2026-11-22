package agents

import (
	"strings"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
)

// geminiVoiceIDs are the voices Gemini Live speaks (stored lowercase).
var geminiVoiceIDs = map[string]bool{
	"puck": true, "charon": true, "kore": true, "fenrir": true,
	"aoede": true, "leda": true, "orus": true, "zephyr": true,
}

// defaultVoiceChoice fills in what a create request leaves out, so every new
// agent starts on Gemini 3.8 Live with Zephyr. When the request names only a
// model or only a voice, the missing half comes from the same engine, so an
// OpenAI model never gets a Gemini voice or the other way round.
func defaultVoiceChoice(voiceID, voiceName, model string) (string, string, string) {
	voiceID = strings.ToLower(strings.TrimSpace(voiceID))
	voiceName = strings.TrimSpace(voiceName)
	model = strings.TrimSpace(model)

	switch {
	case model == "" && voiceID == "":
		return constants.DefaultVoiceID, constants.DefaultVoiceName, constants.DefaultRealtimeModel
	case model == "":
		if geminiVoiceIDs[voiceID] {
			model = constants.DefaultRealtimeModel
		} else {
			model = constants.DefaultOpenAIRealtimeModel
		}
	case voiceID == "":
		if strings.HasPrefix(strings.ToLower(model), "gemini") {
			voiceID, voiceName = constants.DefaultVoiceID, constants.DefaultVoiceName
		} else {
			voiceID, voiceName = constants.DefaultOpenAIVoiceID, constants.DefaultOpenAIVoiceName
		}
	}
	if voiceName == "" {
		voiceName = strings.ToUpper(voiceID[:1]) + voiceID[1:]
	}
	return voiceID, voiceName, model
}
