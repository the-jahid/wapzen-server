package voicecall

import (
	"strings"
	"sync"
)

// A voice turn runs on whatever model the agent picked, and the API rejects a
// parameter its model does not accept rather than ignoring it. Every rejection
// costs a full round trip that the caller sits through in silence, so what a
// model refused is remembered here: the first turn of the process pays for the
// discovery and no turn after it does. Support is per provider and model, which
// is how the same model name reached through two providers stays independent.
var (
	rejectedTemperature sync.Map // provider/model -> struct{}
	learnedTuning       sync.Map // provider/model -> voiceTuning
)

func modelKey(provider, model string) string {
	return normalizeLLMProvider(provider) + "/" + strings.TrimSpace(model)
}

// voiceTuning is the pair of parameters that decide how long a reasoning model
// thinks before it says anything. On a phone call that thinking is dead air —
// the caller hears silence until the first token — so effort is turned off and
// the answer kept terse, which is what a spoken reply wants anyway.
type voiceTuning struct {
	Effort    string
	Verbosity string
}

// configuredTuning is what a reasoning model is asked for before any rejection
// has taught us otherwise. Set VOICE_REASONING_EFFORT to off to leave the
// model's own default alone.
func configuredTuning() voiceTuning {
	tuning := voiceTuning{
		Effort:    strings.ToLower(envString("VOICE_REASONING_EFFORT", "none")),
		Verbosity: strings.ToLower(envString("VOICE_TEXT_VERBOSITY", "low")),
	}
	switch tuning.Effort {
	case "off", "default", "model":
		tuning.Effort = ""
	}
	return tuning
}

// reasoningModel reports whether a model belongs to a family that reasons
// before answering — the ones this tuning exists to hurry along. Guessing wrong
// is not fatal: the first rejection corrects it for the rest of the process.
func reasoningModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(model, "chat") {
		// gpt-5-chat-latest carries the family name but answers without reasoning.
		return false
	}
	return strings.HasPrefix(model, "gpt-5") ||
		strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4")
}

func tuningFor(provider, model string) voiceTuning {
	if learned, ok := learnedTuning.Load(modelKey(provider, model)); ok {
		return learned.(voiceTuning)
	}
	if normalizeLLMProvider(provider) != "openai" || !reasoningModel(model) {
		return voiceTuning{}
	}
	return configuredTuning()
}

// applyVoiceTuning writes tuning onto an OpenAI Responses payload, replacing
// whatever a previous attempt put there.
func applyVoiceTuning(payload map[string]any, tuning voiceTuning) {
	delete(payload, "reasoning")
	delete(payload, "text")
	if tuning.Effort != "" {
		payload["reasoning"] = map[string]any{"effort": tuning.Effort}
	}
	if tuning.Verbosity != "" {
		payload["text"] = map[string]any{"verbosity": tuning.Verbosity}
	}
}

// sendsTemperature reports whether temperature is worth putting on the request.
// Reasoning models reject it outright, and the agent's configured value is
// dropped for them either way — this only decides whether the turn discovers
// that the slow way.
func sendsTemperature(provider, model string) bool {
	if _, rejected := rejectedTemperature.Load(modelKey(provider, model)); rejected {
		return false
	}
	return normalizeLLMProvider(provider) != "openai" || !reasoningModel(model)
}

// retryAfterRejection records what the model refused, takes that parameter off
// the payload, and reports whether the request is now worth sending again. It
// returns false when nothing about the payload would change, so a caller can
// loop on it without spinning.
func retryAfterRejection(provider string, payload map[string]any, err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unsupported") &&
		!strings.Contains(message, "not supported") &&
		!strings.Contains(message, "deprecated") {
		return false
	}
	model, _ := payload["model"].(string)

	if _, sent := payload["temperature"]; sent && strings.Contains(message, "temperature") {
		rejectedTemperature.Store(modelKey(provider, model), struct{}{})
		delete(payload, "temperature")
		return true
	}

	current := tuningFor(provider, model)
	next := current
	switch {
	case strings.Contains(message, "reasoning") || strings.Contains(message, "effort"):
		// The API names the efforts it will take when it turns one down, so the
		// replacement is the best of those rather than another guess.
		next.Effort = bestSupportedEffort(message)
	case strings.Contains(message, "verbosity"):
		next.Verbosity = ""
	default:
		return false
	}
	if next == current {
		return false
	}
	learnedTuning.Store(modelKey(provider, model), next)
	applyVoiceTuning(payload, next)
	return true
}

// effortPreference is ordered fastest first: these are the efforts that keep a
// reasoning model from thinking through the caller's silence.
var effortPreference = []string{"none", "minimal", "low"}

// bestSupportedEffort picks the quickest effort out of a "supported values are"
// rejection. It returns "" when the message lists nothing usable, which drops
// the parameter and lets the model use its own default.
func bestSupportedEffort(message string) string {
	index := strings.Index(message, "supported values are")
	if index < 0 {
		return ""
	}
	listed := message[index:]
	for _, effort := range effortPreference {
		if strings.Contains(listed, "'"+effort+"'") {
			return effort
		}
	}
	return ""
}

// maxPayloadRetries bounds how many times one request may be reshaped and sent
// again. Each rejection removes or downgrades a parameter, so the loop is
// already finite; this only keeps an unfamiliar error message from spinning it.
const maxPayloadRetries = 3
