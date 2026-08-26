package voicecall

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// WhatsApp call audio is 16 kHz mono. OpenAI Audio Speech returns raw PCM
	// at 24 kHz, which the standard bridge resamples before playback.
	audioSampleRate     = 16000
	openAITTSSampleRate = 24000

	defaultSpeed  = 1.0
	defaultVolume = 1.0
	speedMin      = 0.25
	speedMax      = 4.0
	volumeMax     = 2.0

	defaultTranscribeModel = "gpt-4o-mini-transcribe"
	defaultInstructions    = "You are answering a live phone call. Reply in one or two short spoken sentences. Be direct, helpful, and natural."

	placeholderAPIKey           = "openai_replace_me"
	elevenLabsPlaceholderAPIKey = "elevenlabs_replace_me"
)

// agentSpeaksFirst reports whether the agent opens the call rather than waiting
// for the other party. Only these modes produce a welcome message, so only they
// have a welcome delay to honor.
func agentSpeaksFirst(beginMessageMode string) bool {
	switch strings.TrimSpace(beginMessageMode) {
	case "agent_speaks_first", "agent_speaks_first_with_model_generated_message":
		return true
	default:
		return false
	}
}

func clampSpeed(speed float64) float64 {
	if speed <= 0 {
		return defaultSpeed
	}
	if speed < speedMin {
		return speedMin
	}
	if speed > speedMax {
		return speedMax
	}
	return speed
}

func clampVolume(volume float64) float64 {
	if volume < 0 {
		return defaultVolume
	}
	if volume > volumeMax {
		return volumeMax
	}
	return volume
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return fallback
}

func envBoolDefault(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// silenceFillWindow is how long a gap in the caller's incoming audio is filled
// with silence before the fill gives up. It only has to outlast the
// transcriber's trailing-silence window; past that the caller is just not
// talking. Set VOICE_SILENCE_FILL_MS to 0 to send the audio through untouched.
func silenceFillWindow() time.Duration {
	return time.Duration(envInt("VOICE_SILENCE_FILL_MS", 1500)) * time.Millisecond
}
