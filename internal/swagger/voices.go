package swagger

import "whatsapp-ai-caller-server/internal/swagger/oas"

// Voice discovery documentation. These endpoints proxy the ElevenLabs voice
// catalogue with the server's workspace key so the dashboard can offer the full
// ElevenLabs library when picking an agent voice.

const voicesTagName = "voices"

func voicePaths() oas.Object {
	return oas.Object{
		"/v1/voices/elevenlabs": oas.Object{
			"get": listWorkspaceVoicesOperation(),
		},
		"/v1/voices/elevenlabs/library": oas.Object{
			"get": listLibraryVoicesOperation(),
		},
		"/v1/voices/elevenlabs/library/{public_owner_id}/{voice_id}": oas.Object{
			"post": addLibraryVoiceOperation(),
		},
	}
}

func voiceComponentSchemas() oas.Object {
	return oas.Object{
		"Voice":            voiceSchema(),
		"VoiceListData":    voiceListDataSchema(),
		"ListVoicesResponse": oas.Obj().
			P("success", oas.Bool().Example(true)).
			P("message", oas.Str().Example("Voice library retrieved successfully")).
			P("data", oas.Ref("VoiceListData")).
			Req("success", "message", "data").
			Build(),
		"AddLibraryVoiceRequest": oas.Obj().
			Desc("Name to save the copied library voice under.").
			P("name", oas.Str().Desc("Display name for the voice in the workspace.").Example("Brian")).
			Req("name").
			Build(),
		"AddLibraryVoiceResponse": oas.Obj().
			P("success", oas.Bool().Example(true)).
			P("message", oas.Str().Example("Voice added successfully")).
			P("data", oas.Obj().
				P("voice_id", oas.Str().Desc("Workspace voice id to persist on the agent.").Example("nPczCjzI2devNBz1zQrb")).
				P("name", oas.Str().Example("Brian")).
				Req("voice_id", "name")).
			Req("success", "message", "data").
			Build(),
	}
}

func voiceSchema() oas.Object {
	return oas.Obj().
		Desc("One ElevenLabs voice, normalised across the public library and the workspace's own voices.").
		P("voice_id", oas.Str().Desc("ElevenLabs voice identifier.").Example("nPczCjzI2devNBz1zQrb")).
		P("public_owner_id", oas.Str().Desc("Library voice owner. Present only for library voices, which must be added to the workspace before use.").Example("03cb0be2b7c...")).
		P("name", oas.Str().Example("Brian")).
		P("description", oas.Str().Example("A deep, resonant narration voice.")).
		P("category", oas.Str().Example("professional")).
		P("gender", oas.Str().Example("male")).
		P("age", oas.Str().Example("middle_aged")).
		P("accent", oas.Str().Example("american")).
		P("language", oas.Str().Example("en")).
		P("use_case", oas.Str().Example("narrative_story")).
		P("descriptive", oas.Str().Example("confident")).
		P("preview_url", oas.Str().Desc("Audio sample used by the picker's preview button.")).
		P("image_url", oas.Str()).
		P("languages", oas.Arr(oas.Str().Build()).Desc("Languages the voice is verified for.")).
		P("cloned_by_count", oas.Int().Example(1200)).
		P("featured", oas.Bool().Example(false)).
		P("owned", oas.Bool().Desc("True when the voice is already in the workspace and usable for calls as-is.").Example(true)).
		P("added", oas.Bool().Desc("True when a library voice has already been copied into the workspace.").Example(false)).
		Req("voice_id", "name", "owned").
		Build()
}

func voiceListDataSchema() oas.Object {
	return oas.Obj().
		Desc("One page of voices. Library results paginate by page index, workspace results by opaque token.").
		P("voices", oas.Arr(oas.Ref("Voice"))).
		P("has_more", oas.Bool().Example(true)).
		P("total_count", oas.Int().Example(1404)).
		P("next_page", oas.Int().Desc("Next page index for library results.").Example(1)).
		P("next_page_token", oas.Str().Desc("Next page token for workspace results.")).
		Req("voices", "has_more").
		Build()
}

func listLibraryVoicesOperation() oas.Object {
	return oas.Object{
		"tags":        []any{voicesTagName},
		"operationId": "listElevenLabsLibraryVoices",
		"summary":     "Browse the ElevenLabs voice library",
		"description": "Lists voices from the public ElevenLabs library using the server's workspace key. Supports the same filters as the ElevenLabs library UI. Library voices must be added to the workspace before they can be used on a call.",
		"security":    oas.BearerSecurity(),
		"parameters": []any{
			voiceQueryParam("search", "Free-text search over voice names and descriptions.", "narrator"),
			voiceQueryParam("category", "Voice quality tier: professional, famous or high_quality.", "high_quality"),
			voiceQueryParam("gender", "male, female or neutral.", "female"),
			voiceQueryParam("age", "young, middle_aged or old.", "young"),
			voiceQueryParam("accent", "Accent filter, e.g. american or british.", "american"),
			voiceQueryParam("language", "Language code, e.g. en or es.", "en"),
			voiceQueryParam("use_cases", "Comma-separated use cases, e.g. conversational,narrative_story.", "conversational"),
			voiceQueryParam("sort", "trending, created_date, cloned_by_count or usage_character_count_1y.", "trending"),
			voiceIntQueryParam("page", "Zero-based page index.", 0),
			voiceIntQueryParam("page_size", "Results per page (1-100, default 30).", 24),
		},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "One page of library voices.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("ListVoicesResponse"),
						"example": libraryVoicesExample(),
					},
				},
			},
			"400": jsonResponse("Bad Request - the upstream rejected a filter value.", "APIResponse"),
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"502": jsonResponse("Bad Gateway - the ElevenLabs API could not be reached.", "APIResponse"),
			"503": jsonResponse("Service Unavailable - ElevenLabs is not configured on this server.", "APIResponse"),
		},
	}
}

func listWorkspaceVoicesOperation() oas.Object {
	return oas.Object{
		"tags":        []any{voicesTagName},
		"operationId": "listElevenLabsVoices",
		"summary":     "List workspace ElevenLabs voices",
		"description": "Lists the voices already saved in the server's ElevenLabs workspace. These are usable for calls immediately.",
		"security":    oas.BearerSecurity(),
		"parameters": []any{
			voiceQueryParam("search", "Free-text search over names, descriptions and labels.", "lauren"),
			voiceQueryParam("category", "premade, cloned, generated or professional.", "professional"),
			voiceQueryParam("voice_type", "personal, community, default, workspace or saved.", "personal"),
			voiceQueryParam("next_page_token", "Pagination token returned by a previous response.", ""),
			voiceIntQueryParam("page_size", "Results per page (1-100, default 30).", 24),
		},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "One page of workspace voices.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("ListVoicesResponse"),
						"example": workspaceVoicesExample(),
					},
				},
			},
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"502": jsonResponse("Bad Gateway - the ElevenLabs API could not be reached.", "APIResponse"),
			"503": jsonResponse("Service Unavailable - ElevenLabs is not configured on this server.", "APIResponse"),
		},
	}
}

func addLibraryVoiceOperation() oas.Object {
	return oas.Object{
		"tags":        []any{voicesTagName},
		"operationId": "addElevenLabsLibraryVoice",
		"summary":     "Add a library voice to the workspace",
		"description": "Copies a public library voice into the ElevenLabs workspace and returns the workspace voice id to persist on the agent. Library voice ids cannot be used for speech directly. Adding a voice that is already present returns the existing voice instead of failing.",
		"security":    oas.BearerSecurity(),
		"parameters": []any{
			oas.Object{
				"name":        "public_owner_id",
				"in":          "path",
				"required":    true,
				"description": "Owner of the library voice, from the library listing.",
				"schema":      oas.Str().Example("03cb0be2b7c...").Build(),
			},
			oas.Object{
				"name":        "voice_id",
				"in":          "path",
				"required":    true,
				"description": "Library voice identifier.",
				"schema":      oas.Str().Example("nPczCjzI2devNBz1zQrb").Build(),
			},
		},
		"requestBody": oas.Object{
			"required": true,
			"content": oas.Object{
				"application/json": oas.Object{
					"schema":  oas.Ref("AddLibraryVoiceRequest"),
					"example": oas.Object{"name": "Brian"},
				},
			},
		},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "The workspace voice id for the added voice.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema": oas.Ref("AddLibraryVoiceResponse"),
						"example": oas.Object{
							"success": true,
							"message": "Voice added successfully",
							"data":    oas.Object{"voice_id": "nPczCjzI2devNBz1zQrb", "name": "Brian"},
						},
					},
				},
			},
			"400": jsonResponse("Bad Request - missing name or a rejected upstream request.", "APIResponse"),
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"502": jsonResponse("Bad Gateway - the ElevenLabs API could not be reached.", "APIResponse"),
			"503": jsonResponse("Service Unavailable - ElevenLabs is not configured on this server.", "APIResponse"),
		},
	}
}

func voiceQueryParam(name, description, example string) oas.Object {
	schema := oas.Str()
	if example != "" {
		schema = schema.Example(example)
	}
	return oas.Object{
		"name":        name,
		"in":          "query",
		"required":    false,
		"description": description,
		"schema":      schema.Build(),
	}
}

func voiceIntQueryParam(name, description string, example int) oas.Object {
	return oas.Object{
		"name":        name,
		"in":          "query",
		"required":    false,
		"description": description,
		"schema":      oas.Int().Example(example).Build(),
	}
}

func libraryVoicesExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Voice library retrieved successfully",
		"data": oas.Object{
			"voices": []any{
				oas.Object{
					"voice_id":        "UrdIUsVuyr5QSUJdS5hu",
					"public_owner_id": "03cb0be2b7c...",
					"name":            "Katie - Conversational Girl Next Door",
					"category":        "professional",
					"gender":          "female",
					"age":             "young",
					"accent":          "american",
					"language":        "en",
					"use_case":        "conversational",
					"preview_url":     "https://api.elevenlabs.io/v1/voices/UrdIUsVuyr5QSUJdS5hu/previews/audio",
					"languages":       []any{"en"},
					"owned":           false,
				},
			},
			"has_more":    true,
			"total_count": 1404,
			"next_page":   1,
		},
	}
}

func workspaceVoicesExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Voices retrieved successfully",
		"data": oas.Object{
			"voices": []any{
				oas.Object{
					"voice_id":    "DODLEQrClDo8wCz460ld",
					"name":        "Lauren",
					"category":    "professional",
					"gender":      "female",
					"age":         "middle_aged",
					"accent":      "american",
					"language":    "en",
					"use_case":    "conversational",
					"preview_url": "https://api.elevenlabs.io/v1/voices/DODLEQrClDo8wCz460ld/previews/audio",
					"owned":       true,
					"added":       true,
				},
			},
			"has_more":        true,
			"next_page_token": "fER6bHcx...",
		},
	}
}
