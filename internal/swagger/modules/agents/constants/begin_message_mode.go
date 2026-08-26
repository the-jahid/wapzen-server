package constants

// BeginMessageModeOptions is the set of allowed begin_message_mode values.
var BeginMessageModeOptions = []string{
	"agent_speaks_first",
	"agent_waits_for_user",
	"agent_speaks_first_with_model_generated_message",
}

// BeginMessageMode is the union of BeginMessageModeOptions.
type BeginMessageMode = string
