package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// TestServiceCreateRequiresName checks the name rule is enforced before the
// repository is ever reached (the service here has none).
func TestServiceCreateRequiresName(t *testing.T) {
	svc := NewService(nil, nil, nil)
	for name, req := range map[string]types.CreateAgentRequest{
		"no agent section": {},
		"blank name":       {Agent: &types.AgentSection{Name: "   "}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), "user_1", req)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
		})
	}
}

// TestInsertColumnsWritesOnlySuppliedFields pins the create mapping: user_id and
// agent_name always, everything else only when the request carries a value, so
// the schema defaults apply to the rest — except the voice, which is always
// written (Gemini 3.8 Live + Zephyr unless the request picks one).
func TestInsertColumnsWritesOnlySuppliedFields(t *testing.T) {
	req := types.CreateAgentRequest{
		Agent: &types.AgentSection{Name: "Jarvis", PhoneNumberID: ptr(" phone_1 "), Status: "inactive"},
		Voice: &types.VoiceSection{OpenAI: &types.OpenAIVoice{Speed: 1.2}},
	}
	got := insertColumns("user_1", req)

	wantCols := []string{"user_id", "agent_name", "phone_number_id", "status", "voice_openai_speed",
		"voice_openai_voice_id", "voice_openai_voice_name", "voice_openai_realtime_model"}
	wantArgs := []any{"user_1", "Jarvis", ptr("phone_1"), "inactive", 1.2, "zephyr", "Zephyr", "gemini-3.8-live"}
	if !reflect.DeepEqual(got.cols, wantCols) {
		t.Errorf("cols = %#v, want %#v", got.cols, wantCols)
	}
	if !reflect.DeepEqual(got.args, wantArgs) {
		t.Errorf("args = %#v, want %#v", got.args, wantArgs)
	}
}

// TestDefaultVoiceChoice: nothing picked = Gemini 3.8 Live + Zephyr; half a
// choice is completed from the same engine.
func TestDefaultVoiceChoice(t *testing.T) {
	cases := []struct {
		inID, inName, inModel string
		id, name, model       string
	}{
		{"", "", "", "zephyr", "Zephyr", "gemini-3.8-live"},
		{"kore", "", "", "kore", "Kore", "gemini-3.8-live"},
		{"alloy", "Alloy", "", "alloy", "Alloy", "gpt-realtime-2.1-mini"},
		{"", "", "gpt-realtime-2.1", "echo", "Echo", "gpt-realtime-2.1"},
		{"", "", "gemini-3.1-flash-live-preview", "zephyr", "Zephyr", "gemini-3.1-flash-live-preview"},
		{"marin", "Marin", "gpt-realtime-2", "marin", "Marin", "gpt-realtime-2"},
	}
	for _, c := range cases {
		id, name, model := defaultVoiceChoice(c.inID, c.inName, c.inModel)
		if id != c.id || name != c.name || model != c.model {
			t.Errorf("defaultVoiceChoice(%q, %q, %q) = %q, %q, %q; want %q, %q, %q",
				c.inID, c.inName, c.inModel, id, name, model, c.id, c.name, c.model)
		}
	}
}

// TestTranslateWriteError pins which Postgres errors become which domain errors.
func TestTranslateWriteError(t *testing.T) {
	wrap := func(pgErr *pgconn.PgError) error { return fmt.Errorf("insert agent: %w", pgErr) }
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"phone fk", wrap(&pgconn.PgError{Code: "23503", ConstraintName: "agents_phone_number_id_fkey"}), ErrPhoneNumberNotFound},
		{"user fk", wrap(&pgconn.PgError{Code: "23503", ConstraintName: "agents_user_id_fkey"}), ErrUserNotFound},
		{"duplicate name", wrap(&pgconn.PgError{Code: "23505"}), ErrAgentNameTaken},
		{"domain error passes through", ErrAgentNotFound, ErrAgentNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translateWriteError(c.in); !errors.Is(got, c.want) {
				t.Errorf("translateWriteError = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("check violation is a validation error", func(t *testing.T) {
		err := translateWriteError(wrap(&pgconn.PgError{Code: "23514", Message: "bad value"}))
		var validation *ValidationError
		if !errors.As(err, &validation) || err.Error() != "bad value" {
			t.Errorf("err = %v, want ValidationError(bad value)", err)
		}
	})

	if translateWriteError(nil) != nil {
		t.Error("translateWriteError(nil) != nil")
	}
}

// TestWriteErrorStatus pins the HTTP status each domain error answers with.
func TestWriteErrorStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{invalid("agent.name is required"), http.StatusBadRequest},
		{ErrAgentNotFound, http.StatusNotFound},
		{fmt.Errorf("%w: kb_9", ErrKnowledgeBaseNotFound), http.StatusNotFound},
		{ErrAgentNameTaken, http.StatusConflict},
		{fmt.Errorf("%w: kb_9", ErrKnowledgeBaseAttachmentConflict), http.StatusConflict},
		{errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, c.err, "failed")
		if rec.Code != c.want {
			t.Errorf("writeError(%v) status = %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

func TestSamePhoneNumber(t *testing.T) {
	cases := []struct {
		a, b *string
		want bool
	}{
		{nil, nil, true},
		{nil, ptr("  "), true},
		{ptr("p1"), ptr(" p1 "), true},
		{ptr("p1"), nil, false},
		{ptr("p1"), ptr("p2"), false},
	}
	for _, c := range cases {
		if got := samePhoneNumber(c.a, c.b); got != c.want {
			t.Errorf("samePhoneNumber(%v, %v) = %v, want %v", deref(c.a), deref(c.b), got, c.want)
		}
	}
}
