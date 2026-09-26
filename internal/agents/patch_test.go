package agents

import (
	"errors"
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// TestParsePatchScalars pins the column/value mapping for the documented
// partial-update example plus the nullable and nested cases the mapping must get
// right (explicit null clears a column; nested sections descend correctly).
func TestParsePatchScalars(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCols []string
		wantArgs []any
	}{
		{
			name:     "empty object changes nothing",
			body:     `{}`,
			wantCols: nil,
			wantArgs: nil,
		},
		{
			name:     "documented example: name and temperature",
			body:     `{"agent":{"name":"Jarvis (Updated)"},"model":{"temperature":0.4}}`,
			wantCols: []string{"agent_name", "model_temperature"},
			wantArgs: []any{"Jarvis (Updated)", ptr(0.4)},
		},
		{
			name:     "explicit null clears a nullable column",
			body:     `{"agent":{"timezone":null}}`,
			wantCols: []string{"timezone"},
			wantArgs: []any{(*string)(nil)},
		},
		{
			name:     "phone number id maps to nullable assignment column",
			body:     `{"agent":{"phone_number_id":" phone_123 "}}`,
			wantCols: []string{"phone_number_id"},
			wantArgs: []any{ptr("phone_123")},
		},
		{
			name:     "null phone number id clears assignment",
			body:     `{"agent":{"phone_number_id":null}}`,
			wantCols: []string{"phone_number_id"},
			wantArgs: []any{(*string)(nil)},
		},
		{
			name:     "nested transcriber provider model",
			body:     `{"transcriber":{"openai":{"model":"gpt-4o-transcribe"}}}`,
			wantCols: []string{"transcriber_openai_model"},
			wantArgs: []any{"gpt-4o-transcribe"},
		},
		{
			name:     "nested elevenlabs realtime transcriber model",
			body:     `{"transcriber":{"provider":"11labs","elevenlabs":{"model":"scribe_v2_realtime"}}}`,
			wantCols: []string{"transcriber_provider", "transcriber_elevenlabs_model"},
			wantArgs: []any{"11labs", "scribe_v2_realtime"},
		},
		{
			name:     "nested openai realtime voice model",
			body:     `{"voice":{"openai":{"realtime_model":"gpt-realtime-2.1"}}}`,
			wantCols: []string{"voice_openai_realtime_model"},
			wantArgs: []any{"gpt-realtime-2.1"},
		},
		{
			name:     "post call scalar settings",
			body:     `{"post_call":{"analysis_provider":"anthropic","analysis_model":null}}`,
			wantCols: []string{"post_call_analysis_provider", "post_call_analysis_model"},
			wantArgs: []any{"anthropic", (*string)(nil)},
		},
		{
			name:     "null section is a no-op",
			body:     `{"model":null}`,
			wantCols: nil,
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch, err := parsePatch([]byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if patch.changesAttachments() {
				t.Fatalf("expected no attachment sets, got knowledgeBases=%v toolIDs=%v",
					patch.knowledgeBaseIDs, patch.toolIDs)
			}
			if !reflect.DeepEqual(patch.columns.cols, tt.wantCols) {
				t.Errorf("cols = %#v, want %#v", patch.columns.cols, tt.wantCols)
			}
			if !reflect.DeepEqual(patch.columns.args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", patch.columns.args, tt.wantArgs)
			}
		})
	}
}

// TestParsePatchChildPresence checks the "present means replace, absent
// means leave unchanged" rule for the attachment sets, including the
// empty-value (clear) case.
func TestParsePatchChildPresence(t *testing.T) {
	t.Run("knowledge_base_ids present replaces", func(t *testing.T) {
		body := `{"knowledge_base":{"knowledge_base_ids":["kb_1","kb_2"]}}`
		patch, err := parsePatch([]byte(body))
		knowledgeBases := patch.knowledgeBaseIDs
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if knowledgeBases == nil {
			t.Fatal("knowledgeBases = nil, want non-nil pointer")
		}
		if !reflect.DeepEqual(*knowledgeBases, []string{"kb_1", "kb_2"}) {
			t.Errorf("knowledgeBases = %#v, want [kb_1 kb_2]", *knowledgeBases)
		}
	})

	// An empty list is how the UI detaches every knowledge base, so it has to
	// survive parsing as "replace with nothing" rather than "unchanged".
	t.Run("empty knowledge_base_ids detaches all", func(t *testing.T) {
		patch, err := parsePatch([]byte(`{"knowledge_base":{"knowledge_base_ids":[]}}`))
		knowledgeBases := patch.knowledgeBaseIDs
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if knowledgeBases == nil {
			t.Fatal("knowledgeBases = nil, want non-nil pointer signalling replace-with-empty")
		}
		if len(*knowledgeBases) != 0 {
			t.Errorf("knowledgeBases = %#v, want empty", *knowledgeBases)
		}
	})

	t.Run("absent knowledge_base leaves attachments unchanged", func(t *testing.T) {
		patch, err := parsePatch([]byte(`{"agent":{"name":"x"}}`))
		knowledgeBases := patch.knowledgeBaseIDs
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if knowledgeBases != nil {
			t.Errorf("knowledgeBases = %#v, want nil (unchanged)", *knowledgeBases)
		}
	})
}

// TestNormalizeIDs pins what the attachment writers accept: blanks disappear,
// surrounding whitespace is not part of an id, and a repeated id attaches once
// — attaching twice is the same state as attaching once.
func TestNormalizeIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays empty", nil, []string{}},
		{"trims ids", []string{" kb_1 "}, []string{"kb_1"}},
		{"drops blanks", []string{"kb_1", "", "   "}, []string{"kb_1"}},
		{"drops duplicates keeping first order", []string{"kb_2", "kb_1", "kb_2"}, []string{"kb_2", "kb_1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeIDs(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("normalizeIDs(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

// TestParsePatchInvalid confirms malformed bodies and wrong-typed fields
// surface as *ValidationError so the handler can answer 400.
func TestParsePatchInvalid(t *testing.T) {
	bodies := map[string]string{
		"not an object":      `"just a string"`,
		"wrong scalar type":  `{"model":{"temperature":"hot"}}`,
		"wrong section type": `{"agent":"nope"}`,
		"wrong child type":   `{"knowledge_base":{"knowledge_base_ids":"kb_1"}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			_, err := parsePatch([]byte(body))
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
		})
	}
}

// TestGoLiveInvariantHelpers pins the two predicates behind the "active needs a
// phone number" guard: which columns arm the check, and what counts as a real
// phone assignment (NULL and blank strings do not).
func TestGoLiveInvariantHelpers(t *testing.T) {
	t.Run("columnSet.has", func(t *testing.T) {
		set := columnSet{cols: []string{"agent_name", "status", "phone_number_id"}}
		if !set.has("status") {
			t.Error("has(status) = false, want true")
		}
		if !set.has("phone_number_id") {
			t.Error("has(phone_number_id) = false, want true")
		}
		if set.has("language") {
			t.Error("has(language) = true, want false")
		}
		if (&columnSet{}).has("status") {
			t.Error("empty set has(status) = true, want false")
		}
	})

	t.Run("hasPhoneNumberAssigned", func(t *testing.T) {
		cases := []struct {
			name string
			id   *string
			want bool
		}{
			{"nil is unassigned", nil, false},
			{"empty string is unassigned", ptr(""), false},
			{"blank string is unassigned", ptr("   "), false},
			{"real id is assigned", ptr("phone_123"), true},
		}
		for _, c := range cases {
			if got := hasPhoneNumberAssigned(c.id); got != c.want {
				t.Errorf("%s: hasPhoneNumberAssigned = %v, want %v", c.name, got, c.want)
			}
		}
	})
}
