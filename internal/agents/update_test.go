package agents

import (
	"errors"
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func ptr[T any](v T) *T { return &v }

// TestParseAgentUpdateScalars pins the column/value mapping for the documented
// partial-update example plus the nullable and nested cases the mapping must get
// right (explicit null clears a column; nested sections descend correctly).
func TestParseAgentUpdateScalars(t *testing.T) {
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
			name:     "null section is a no-op",
			body:     `{"model":null}`,
			wantCols: nil,
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, dynVars, postCall, knowledgeBases, toolIDs, err := parseAgentUpdate([]byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dynVars != nil || postCall != nil || knowledgeBases != nil || toolIDs != nil {
				t.Fatalf("expected no child collections, got dynVars=%v postCall=%v knowledgeBases=%v toolIDs=%v",
					dynVars, postCall, knowledgeBases, toolIDs)
			}
			if !reflect.DeepEqual(set.cols, tt.wantCols) {
				t.Errorf("cols = %#v, want %#v", set.cols, tt.wantCols)
			}
			if !reflect.DeepEqual(set.args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", set.args, tt.wantArgs)
			}
		})
	}
}

// TestParseAgentUpdateChildPresence checks the "present means replace, absent
// means leave unchanged" rule for the three child collections, including the
// empty-value (clear) case.
func TestParseAgentUpdateChildPresence(t *testing.T) {
	t.Run("dynamic_variables present replaces", func(t *testing.T) {
		_, dynVars, _, _, _, err := parseAgentUpdate([]byte(`{"prompt":{"dynamic_variables":{"k":"v"}}}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dynVars == nil {
			t.Fatal("dynVars = nil, want non-nil pointer")
		}
		if !reflect.DeepEqual(*dynVars, map[string]string{"k": "v"}) {
			t.Errorf("dynVars = %#v, want {k:v}", *dynVars)
		}
	})

	t.Run("empty dynamic_variables clears", func(t *testing.T) {
		_, dynVars, _, _, _, err := parseAgentUpdate([]byte(`{"prompt":{"dynamic_variables":{}}}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dynVars == nil {
			t.Fatal("dynVars = nil, want non-nil pointer signalling replace-with-empty")
		}
		if len(*dynVars) != 0 {
			t.Errorf("dynVars = %#v, want empty", *dynVars)
		}
	})

	t.Run("absent dynamic_variables leaves unchanged", func(t *testing.T) {
		_, dynVars, _, _, _, err := parseAgentUpdate([]byte(`{"prompt":{"system_prompt":"x"}}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dynVars != nil {
			t.Errorf("dynVars = %#v, want nil (unchanged)", *dynVars)
		}
	})

	t.Run("post_call_analysis_data present replaces", func(t *testing.T) {
		body := `{"post_call":{"post_call_analysis_data":[{"type":"string","name":"summary"}]}}`
		_, _, postCall, _, _, err := parseAgentUpdate([]byte(body))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if postCall == nil {
			t.Fatal("postCall = nil, want non-nil pointer")
		}
		want := []types.PostCallField{{Type: constants.PostCallFieldType("string"), Name: "summary"}}
		if !reflect.DeepEqual(*postCall, want) {
			t.Errorf("postCall = %#v, want %#v", *postCall, want)
		}
	})

	t.Run("knowledge_base_ids present replaces", func(t *testing.T) {
		body := `{"knowledge_base":{"knowledge_base_ids":["kb_1","kb_2"]}}`
		_, _, _, knowledgeBases, _, err := parseAgentUpdate([]byte(body))
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
		_, _, _, knowledgeBases, _, err := parseAgentUpdate([]byte(`{"knowledge_base":{"knowledge_base_ids":[]}}`))
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
		_, _, _, knowledgeBases, _, err := parseAgentUpdate([]byte(`{"agent":{"name":"x"}}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if knowledgeBases != nil {
			t.Errorf("knowledgeBases = %#v, want nil (unchanged)", *knowledgeBases)
		}
	})
}

// TestNormalizeKnowledgeBaseIDs pins what the attachment writer accepts: blanks
// disappear, surrounding whitespace is not part of an id, and a repeated id
// attaches once — attaching twice is the same state as attaching once.
func TestNormalizeKnowledgeBaseIDs(t *testing.T) {
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
			if got := normalizeKnowledgeBaseIDs(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("normalizeKnowledgeBaseIDs(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

// TestParseAgentUpdateInvalid confirms malformed bodies and wrong-typed fields
// surface as *InvalidRequestError so the handler can answer 400.
func TestParseAgentUpdateInvalid(t *testing.T) {
	bodies := map[string]string{
		"not an object":      `"just a string"`,
		"wrong scalar type":  `{"model":{"temperature":"hot"}}`,
		"wrong section type": `{"agent":"nope"}`,
		"wrong child type":   `{"prompt":{"dynamic_variables":[1,2,3]}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			_, _, _, _, _, err := parseAgentUpdate([]byte(body))
			var invalid *InvalidRequestError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want *InvalidRequestError", err)
			}
		})
	}
}

// TestGoLiveInvariantHelpers pins the two predicates behind the "active needs a
// phone number" guard: which columns arm the check, and what counts as a real
// phone assignment (NULL and blank strings do not).
func TestGoLiveInvariantHelpers(t *testing.T) {
	t.Run("columnsInclude", func(t *testing.T) {
		cols := []string{"agent_name", "status", "phone_number_id"}
		if !columnsInclude(cols, "status") {
			t.Error("columnsInclude(status) = false, want true")
		}
		if !columnsInclude(cols, "phone_number_id") {
			t.Error("columnsInclude(phone_number_id) = false, want true")
		}
		if columnsInclude(cols, "language") {
			t.Error("columnsInclude(language) = true, want false")
		}
		if columnsInclude(nil, "status") {
			t.Error("columnsInclude(nil, status) = true, want false")
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
