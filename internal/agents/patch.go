package agents

import "encoding/json"

// agentPatch is a parsed partial-update body: the scalar columns to write, plus
// the two attachment sets. A nil attachment set means "leave it unchanged"; a
// non-nil one — even empty — means "replace it with exactly these ids".
type agentPatch struct {
	columns          columnSet
	knowledgeBaseIDs *[]string
	toolIDs          *[]string
}

// changesAttachments reports whether the patch replaces either attachment set.
func (p agentPatch) changesAttachments() bool {
	return p.knowledgeBaseIDs != nil || p.toolIDs != nil
}

// rawFields is one JSON object level left undecoded, so the presence of a key
// can be told apart from a key set to its type's zero value — the crux of PATCH
// semantics that a typed struct cannot express.
type rawFields map[string]json.RawMessage

// patchParser walks a PATCH body section by section. The first decoding error is
// kept and every later step becomes a no-op, so parsePatch reads as a straight
// list of fields with a single error check at the end.
type patchParser struct {
	patch agentPatch
	err   error
}

// field decodes fields[key] as T. ok is false when the key is absent (or an
// earlier step failed); a present key with the wrong JSON type records a
// ValidationError. For pointer types an explicit JSON null decodes to nil, which
// is how a nullable column is cleared.
func field[T any](p *patchParser, fields rawFields, key string) (value T, ok bool) {
	if p.err != nil || fields == nil {
		return value, false
	}
	raw, present := fields[key]
	if !present {
		return value, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		p.err = invalid("invalid value for %q", key)
		return value, false
	}
	return value, true
}

// section descends into a nested object. A missing key or explicit null yields
// nil, which every later field call treats as "nothing to set".
func (p *patchParser) section(fields rawFields, key string) rawFields {
	sub, _ := field[rawFields](p, fields, key)
	return sub
}

// set records "col = value" when key is present. T is chosen per column: a plain
// type for NOT NULL columns, a pointer type for nullable ones.
func set[T any](p *patchParser, fields rawFields, key, col string) {
	if v, ok := field[T](p, fields, key); ok {
		p.patch.columns.add(col, v)
	}
}

// parsePatch maps a partial-update JSON body onto column assignments and
// attachment sets. It returns a ValidationError when the body is not a JSON
// object or a field has the wrong type for its column.
func parsePatch(body []byte) (agentPatch, error) {
	var top rawFields
	if err := json.Unmarshal(body, &top); err != nil {
		return agentPatch{}, invalid("request body must be a JSON object")
	}

	p := &patchParser{}

	agent := p.section(top, "agent")
	set[string](p, agent, "name", "agent_name")
	set[string](p, agent, "language", "language")
	set[*string](p, agent, "timezone", "timezone")
	if v, ok := field[*string](p, agent, "phone_number_id"); ok {
		p.patch.columns.add("phone_number_id", normalizePhoneNumberID(v))
	}
	set[string](p, agent, "call_direction", "call_direction")
	set[string](p, agent, "status", "status")

	model := p.section(top, "model")
	set[string](p, model, "provider", "model_provider")
	set[string](p, model, "name", "model_name")
	set[*float64](p, model, "temperature", "model_temperature")

	prompt := p.section(top, "prompt")
	set[string](p, prompt, "begin_message_mode", "prompt_begin_message_mode")
	set[*string](p, prompt, "begin_message", "prompt_begin_message")
	set[*int](p, prompt, "begin_message_delay_ms", "prompt_begin_message_delay_ms")
	set[*string](p, prompt, "system_prompt", "prompt_system_prompt")

	voice := p.section(top, "voice")
	set[*string](p, voice, "provider", "voice_provider")
	elevenLabs := p.section(voice, "elevenlabs")
	set[string](p, elevenLabs, "voice_id", "voice_elevenlabs_voice_id")
	set[string](p, elevenLabs, "voice_name", "voice_elevenlabs_voice_name")
	set[string](p, elevenLabs, "voice_model", "voice_elevenlabs_voice_model")
	openAI := p.section(voice, "openai")
	set[string](p, openAI, "voice_id", "voice_openai_voice_id")
	set[string](p, openAI, "voice_name", "voice_openai_voice_name")
	set[string](p, openAI, "voice_model", "voice_openai_voice_model")
	set[string](p, openAI, "realtime_model", "voice_openai_realtime_model")
	set[*string](p, openAI, "instructions", "voice_openai_instructions")
	set[float64](p, openAI, "speed", "voice_openai_speed")
	set[float64](p, openAI, "volume", "voice_openai_volume")

	transcriber := p.section(top, "transcriber")
	set[string](p, transcriber, "provider", "transcriber_provider")
	set[string](p, transcriber, "language", "transcriber_language")
	set[string](p, p.section(transcriber, "openai"), "model", "transcriber_openai_model")
	set[string](p, p.section(transcriber, "elevenlabs"), "model", "transcriber_elevenlabs_model")

	postCall := p.section(top, "post_call")
	set[string](p, postCall, "analysis_provider", "post_call_analysis_provider")
	set[*string](p, postCall, "analysis_model", "post_call_analysis_model")

	// Attachment sets: a present key (even null) means "replace"; absent means
	// "leave unchanged".
	if ids, ok := field[[]string](p, p.section(top, "knowledge_base"), "knowledge_base_ids"); ok {
		p.patch.knowledgeBaseIDs = &ids
	}
	if ids, ok := field[[]string](p, p.section(top, "tools"), "tool_ids"); ok {
		p.patch.toolIDs = &ids
	}

	if p.err != nil {
		return agentPatch{}, p.err
	}
	return p.patch, nil
}
