package voicecall

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
)

func TestPCM16WAVHeader(t *testing.T) {
	wav := pcm16WAV(make([]float32, 1600), audioSampleRate)
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("invalid WAV header: %q %q %q", wav[:4], wav[8:12], wav[36:40])
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != audioSampleRate {
		t.Fatalf("sample rate = %d, want %d", got, audioSampleRate)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != 3200 {
		t.Fatalf("PCM byte length = %d, want 3200", got)
	}
}

func TestOpenAIStandardTranscriptionRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "gpt-4o-transcribe" {
			t.Errorf("model = %q", got)
		}
		if got := r.FormValue("language"); got != "bn" {
			t.Errorf("language = %q", got)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("audio file: %v", err)
		}
		defer file.Close()
		header := make([]byte, 12)
		if _, err := io.ReadFull(file, header); err != nil {
			t.Fatalf("read WAV: %v", err)
		}
		if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
			t.Errorf("uploaded file is not WAV")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"আমি সাহায্য চাই"}`)
	}))
	defer server.Close()

	b := &openAIStandardVoiceBridge{cfg: openAIStandardConfig{
		APIKey:             "test-key",
		TranscriptionURL:   server.URL,
		TranscriptionModel: "gpt-4o-transcribe",
		Language:           "bn-BD",
		HTTPClient:         server.Client(),
	}}
	text, err := b.transcribe(context.Background(), make([]float32, 1600))
	if err != nil {
		t.Fatal(err)
	}
	if text != "আমি সাহায্য চাই" {
		t.Fatalf("text = %q", text)
	}
}

func TestOpenAIStandardTTSStreamsPCM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode TTS request: %v", err)
		}
		if payload["model"] != "gpt-4o-mini-tts" || payload["voice"] != "marin" || payload["response_format"] != "pcm" {
			t.Errorf("unexpected TTS payload: %#v", payload)
		}
		if payload["instructions"] != "Speak warmly." {
			t.Errorf("instructions = %#v", payload["instructions"])
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(float32ToPCM16LE(make([]float32, openAITTSSampleRate/10)))
	}))
	defer server.Close()

	played := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	b := &openAIStandardVoiceBridge{
		cfg: openAIStandardConfig{
			APIKey:            "test-key",
			SpeechURL:         server.URL,
			TTSModel:          "gpt-4o-mini-tts",
			Voice:             "marin",
			VoiceInstructions: "Speak warmly.",
			Speed:             1,
			Volume:            1,
			HTTPClient:        server.Client(),
		},
		ctx:    ctx,
		cancel: cancel,
		playSource: func(source meowcaller.AudioSource) {
			go func() {
				defer func() { played <- struct{}{} }()
				for {
					if _, err := source.ReadFrame(); err != nil {
						return
					}
				}
			}()
		},
	}
	turn := b.beginTurn()
	if err := b.speak(turn, "Hello there"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-played:
	case <-time.After(time.Second):
		t.Fatal("streamed audio was not drained")
	}
	if !strings.EqualFold(normalizeOpenAITTSVoice("ONYX"), "onyx") {
		t.Fatal("standard TTS voice normalization rejected a supported voice")
	}
	if got := normalizeOpenAITTSVoiceForModel("marin", "tts-1"); got != defaultOpenAIVoice {
		t.Fatalf("legacy TTS model kept unsupported voice %q", got)
	}
	if got := normalizeOpenAITTSVoiceForModel("marin", "gpt-4o-mini-tts"); got != "marin" {
		t.Fatalf("GPT-4o Mini TTS voice = %q, want marin", got)
	}
}
