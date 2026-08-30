package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/chatagents"
	"whatsapp-ai-caller-server/internal/chatconversations"
	"whatsapp-ai-caller-server/internal/handlers"
	"whatsapp-ai-caller-server/internal/knowledgebases"
	appmiddleware "whatsapp-ai-caller-server/internal/middleware"
	"whatsapp-ai-caller-server/internal/outboundcampaigns"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/swagger"
	"whatsapp-ai-caller-server/internal/tools"
	"whatsapp-ai-caller-server/internal/users"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

// Dependencies are the route-level dependencies provided by main.
type Dependencies struct {
	UsersRepo                 *users.Repository
	AgentsRepo                *agents.Repository
	ChatAgentsRepo            *chatagents.Repository
	ChatConversationsRepo     *chatconversations.Repository
	APIKeysRepo               *apikeys.Repository
	PhoneNumbersRepo          *phonenumbers.Repository
	CallsRepo                 *calls.Repository
	KnowledgeBasesRepo        *knowledgebases.Repository
	KnowledgeBaseIndexer      *knowledgebases.Indexer
	OutboundCampaignsRepo     *outboundcampaigns.Repository
	ToolsRepo                 *tools.Repository
	WhatsAppLoginManager      *whatsapplogin.Manager
	ClerkWebhookSigningSecret string
	ElevenLabsAPIKey          string
}

// NewRouter builds the chi router with all middleware and routes registered.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()
	webhookHandler := handlers.NewWebhookHandler(deps.UsersRepo, deps.ClerkWebhookSigningSecret)
	agentHandler := handlers.NewAgentHandler(deps.AgentsRepo, deps.WhatsAppLoginManager, deps.KnowledgeBaseIndexer)
	chatAgentHandler := handlers.NewChatAgentHandler(deps.ChatAgentsRepo, deps.KnowledgeBaseIndexer)
	chatConversationHandler := handlers.NewChatConversationHandler(deps.ChatConversationsRepo, deps.WhatsAppLoginManager)
	userHandler := handlers.NewUserHandler()
	apiKeyHandler := handlers.NewAPIKeyHandler(deps.APIKeysRepo)
	phoneNumberHandler := handlers.NewPhoneNumberHandler(
		deps.PhoneNumbersRepo,
		deps.WhatsAppLoginManager,
	)
	voiceHandler := handlers.NewVoiceHandler(deps.ElevenLabsAPIKey)
	callHandler := handlers.NewCallHandler(
		deps.CallsRepo,
		deps.PhoneNumbersRepo,
		deps.WhatsAppLoginManager,
	)
	knowledgeBaseHandler := handlers.NewKnowledgeBaseHandler(deps.KnowledgeBasesRepo, deps.KnowledgeBaseIndexer)
	outboundCampaignHandler := handlers.NewOutboundCampaignHandler(deps.OutboundCampaignsRepo, deps.AgentsRepo)
	campaignLeadHandler := handlers.NewCampaignLeadHandler(
		deps.OutboundCampaignsRepo,
		deps.CallsRepo,
		deps.WhatsAppLoginManager,
	)
	toolHandler := handlers.NewToolHandler(deps.ToolsRepo)
	authMW := appmiddleware.NewAuth(deps.UsersRepo, deps.APIKeysRepo)

	// Built-in chi middleware.
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)

	// Custom CORS middleware.
	r.Use(appmiddleware.CORS)

	// Health check.
	r.Get("/health", handlers.HealthCheck)

	// Clerk webhooks are public but verified by Svix signatures in the handler.
	r.Post("/api/webhooks/clerk", webhookHandler.Clerk)

	// Account API. RequireUser accepts Clerk bearer tokens or generated API keys
	// and stores the resolved application user in the request context; handlers
	// read it via middleware.UserFromContext instead of trusting request
	// parameters.
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireUser)

		// Users.
		r.Get("/v1/users/me", userHandler.Me)

		// API keys. Keys are scoped to the authenticated user.
		r.Get("/v1/api-keys", apiKeyHandler.List)
		r.Post("/v1/api-keys", apiKeyHandler.Create)
		r.Patch("/v1/api-keys/{api_key_id}/default", apiKeyHandler.SetDefault)
		r.Delete("/v1/api-keys/{api_key_id}", apiKeyHandler.Revoke)

		// WhatsApp phone number QR login. Login sessions remain scoped to the
		// authenticated user; paired clients are adopted by the call runtime.
		r.Get("/v1/phone-number", phoneNumberHandler.List)
		r.Post("/v1/phone-number/login", phoneNumberHandler.Login)
		r.Get("/v1/phone-number/{phone_number_id}", phoneNumberHandler.Get)
		r.Post("/v1/phone-number/logout", phoneNumberHandler.Logout)

		// ElevenLabs voice catalogue for the agent voice picker. The workspace
		// API key stays server-side; these endpoints only return voice metadata.
		r.Get("/v1/voices/elevenlabs", voiceHandler.Workspace)
		r.Get("/v1/voices/elevenlabs/library", voiceHandler.Library)
		r.Post("/v1/voices/elevenlabs/library/{public_owner_id}/{voice_id}", voiceHandler.AddLibraryVoice)

		// Dashboard agent access uses the logged-in user. Public live API
		// routes below remain API-key-only for external callers.
		r.Post("/v1/dashboard/agents", agentHandler.Create)
		r.Get("/v1/dashboard/agents", agentHandler.List)
		r.Get("/v1/dashboard/agents/{agent_id}", agentHandler.Get)
		r.Patch("/v1/dashboard/agents/{agent_id}", agentHandler.Update)
		r.Delete("/v1/dashboard/agents/{agent_id}", agentHandler.Delete)

		// Dashboard chat agents use the Clerk-authenticated account and control
		// the behaviour of incoming WhatsApp text messages in real time.
		r.Post("/v1/dashboard/chat-agents", chatAgentHandler.Create)
		r.Get("/v1/dashboard/chat-agents", chatAgentHandler.List)
		r.Get("/v1/dashboard/chat-agents/{chat_agent_id}", chatAgentHandler.Get)
		r.Patch("/v1/dashboard/chat-agents/{chat_agent_id}", chatAgentHandler.Update)
		r.Delete("/v1/dashboard/chat-agents/{chat_agent_id}", chatAgentHandler.Delete)

		// The saved WhatsApp threads those agents answered. They are written by
		// the message runtime as it replies, so the API only reads a thread,
		// closes it, or deletes it. The per-agent route is what the dashboard's
		// Conversation tab lists.
		r.Get("/v1/dashboard/chat-agents/{chat_agent_id}/conversations", chatConversationHandler.ListByChatAgent)
		r.Get("/v1/dashboard/chat-conversations", chatConversationHandler.List)
		r.Get("/v1/dashboard/chat-conversations/{conversation_id}", chatConversationHandler.Get)
		r.Patch("/v1/dashboard/chat-conversations/{conversation_id}", chatConversationHandler.Update)
		r.Post("/v1/dashboard/chat-conversations/{conversation_id}/messages", chatConversationHandler.SendMessage)
		r.Delete("/v1/dashboard/chat-conversations/{conversation_id}", chatConversationHandler.Delete)

		// Dashboard call history uses the logged-in user. These share the same
		// handlers as the API-key /v1/calls routes below; the auth middleware
		// supplies the resolved user either way. Create places an outbound call;
		// inbound calls arrive through the voice-call runtime instead.
		r.Post("/v1/dashboard/calls", callHandler.Create)
		r.Get("/v1/dashboard/calls", callHandler.List)
		r.Get("/v1/dashboard/calls/{call_id}", callHandler.Get)
		r.Patch("/v1/dashboard/calls/{call_id}", callHandler.Update)
		r.Delete("/v1/dashboard/calls/{call_id}", callHandler.Delete)

		// Dashboard knowledge bases, sharing the handlers with the API-key
		// /v1/knowledge-base routes below.
		r.Post("/v1/dashboard/knowledge-base", knowledgeBaseHandler.Create)
		r.Get("/v1/dashboard/knowledge-base", knowledgeBaseHandler.List)
		r.Get("/v1/dashboard/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Get)
		r.Patch("/v1/dashboard/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Update)
		r.Delete("/v1/dashboard/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Delete)
		r.Post("/v1/dashboard/knowledge-base/{knowledge_base_id}/sources", knowledgeBaseHandler.AddSources)
		r.Delete("/v1/dashboard/knowledge-base/{knowledge_base_id}/sources/{source_id}", knowledgeBaseHandler.DeleteSource)

		// Dashboard tools, sharing the handlers with the API-key /v1/tools routes
		// below. A tool defines an action; it is offered on a call only once an
		// agent attaches it through tools.tool_ids.
		r.Post("/v1/dashboard/tools", toolHandler.Create)
		r.Get("/v1/dashboard/tools", toolHandler.List)
		r.Get("/v1/dashboard/tools/{tool_id}", toolHandler.Get)
		r.Patch("/v1/dashboard/tools/{tool_id}", toolHandler.Update)
		r.Delete("/v1/dashboard/tools/{tool_id}", toolHandler.Delete)

		// Dashboard outbound campaigns, sharing the handlers with the API-key
		// /v1/outbound-campaigns routes below.
		r.Post("/v1/dashboard/outbound-campaigns", outboundCampaignHandler.Create)
		r.Get("/v1/dashboard/outbound-campaigns", outboundCampaignHandler.List)
		r.Get("/v1/dashboard/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Get)
		r.Patch("/v1/dashboard/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Update)
		r.Delete("/v1/dashboard/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Delete)
		// A campaign's own call history. It shares the calls handler with the
		// routes above; only the campaign that placed the calls narrows it.
		r.Get("/v1/dashboard/outbound-campaigns/{campaign_id}/calls", callHandler.ListByCampaign)
		r.Get("/v1/dashboard/outbound-campaigns/{campaign_id}/analytics", outboundCampaignHandler.Analytics)
		// Adding a lead also dials it with the campaign's agent, so this is the
		// route that puts a campaign's calls on the wire.
		r.Post("/v1/dashboard/outbound-campaigns/{campaign_id}/leads", campaignLeadHandler.Create)
		r.Get("/v1/dashboard/outbound-campaigns/{campaign_id}/leads", campaignLeadHandler.List)
		r.Get("/v1/dashboard/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Get)
		r.Patch("/v1/dashboard/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Update)
		r.Delete("/v1/dashboard/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Delete)
	})

	// Agent API. These live API routes only accept generated API keys; Clerk
	// session tokens are not valid here.
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAPIKeyUser)

		// Agents. Every operation is scoped to the authenticated API key owner.
		r.Post("/v1/agents", agentHandler.Create)
		r.Get("/v1/agents", agentHandler.List)
		r.Get("/v1/agents/{agent_id}", agentHandler.Get)
		r.Patch("/v1/agents/{agent_id}", agentHandler.Update)
		r.Delete("/v1/agents/{agent_id}", agentHandler.Delete)

		// API-key chat-agent CRUD mirrors the dashboard routes above.
		r.Post("/v1/chat-agents", chatAgentHandler.Create)
		r.Get("/v1/chat-agents", chatAgentHandler.List)
		r.Get("/v1/chat-agents/{chat_agent_id}", chatAgentHandler.Get)
		r.Patch("/v1/chat-agents/{chat_agent_id}", chatAgentHandler.Update)
		r.Delete("/v1/chat-agents/{chat_agent_id}", chatAgentHandler.Delete)

		// API-key chat-conversation reads mirror the dashboard routes above.
		r.Get("/v1/chat-agents/{chat_agent_id}/conversations", chatConversationHandler.ListByChatAgent)
		r.Get("/v1/chat-conversations", chatConversationHandler.List)
		r.Get("/v1/chat-conversations/{conversation_id}", chatConversationHandler.Get)
		r.Patch("/v1/chat-conversations/{conversation_id}", chatConversationHandler.Update)
		r.Post("/v1/chat-conversations/{conversation_id}/messages", chatConversationHandler.SendMessage)
		r.Delete("/v1/chat-conversations/{conversation_id}", chatConversationHandler.Delete)

		// Calls. Placing and managing the calls handled for the API key owner.
		// POST places an outbound call; inbound calls are recorded by the
		// voice-call runtime as they arrive.
		r.Post("/v1/calls", callHandler.Create)
		r.Get("/v1/calls", callHandler.List)
		r.Get("/v1/calls/{call_id}", callHandler.Get)
		r.Patch("/v1/calls/{call_id}", callHandler.Update)
		r.Delete("/v1/calls/{call_id}", callHandler.Delete)

		// Knowledge bases. Add Sources indexes raw texts into the knowledge base's
		// vector namespace and Delete Source removes one again, vectors included;
		// url and file sources arrive with their own fetchers.
		r.Post("/v1/knowledge-base", knowledgeBaseHandler.Create)
		r.Get("/v1/knowledge-base", knowledgeBaseHandler.List)
		r.Get("/v1/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Get)
		r.Patch("/v1/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Update)
		r.Delete("/v1/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Delete)
		r.Post("/v1/knowledge-base/{knowledge_base_id}/sources", knowledgeBaseHandler.AddSources)
		r.Delete("/v1/knowledge-base/{knowledge_base_id}/sources/{source_id}", knowledgeBaseHandler.DeleteSource)

		// Tools. The actions an agent can take mid-call: hit an HTTP endpoint,
		// hand the caller over, send a WhatsApp message, hang up. Every operation
		// is scoped to the authenticated API key owner, and a tool stays inert
		// until an agent attaches it through tools.tool_ids.
		r.Post("/v1/tools", toolHandler.Create)
		r.Get("/v1/tools", toolHandler.List)
		r.Get("/v1/tools/{tool_id}", toolHandler.Get)
		r.Patch("/v1/tools/{tool_id}", toolHandler.Update)
		r.Delete("/v1/tools/{tool_id}", toolHandler.Delete)

		// Outbound campaigns. Every operation is scoped to the authenticated API
		// key owner. A campaign records the batch and its counters, and adding a
		// lead to one dials that lead with the campaign's agent.
		r.Post("/v1/outbound-campaigns", outboundCampaignHandler.Create)
		r.Get("/v1/outbound-campaigns", outboundCampaignHandler.List)
		r.Get("/v1/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Get)
		r.Patch("/v1/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Update)
		r.Delete("/v1/outbound-campaigns/{campaign_id}", outboundCampaignHandler.Delete)
		r.Get("/v1/outbound-campaigns/{campaign_id}/calls", callHandler.ListByCampaign)
		r.Get("/v1/outbound-campaigns/{campaign_id}/analytics", outboundCampaignHandler.Analytics)
		r.Post("/v1/outbound-campaigns/{campaign_id}/leads", campaignLeadHandler.Create)
		r.Get("/v1/outbound-campaigns/{campaign_id}/leads", campaignLeadHandler.List)
		r.Get("/v1/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Get)
		r.Patch("/v1/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Update)
		r.Delete("/v1/outbound-campaigns/{campaign_id}/leads/{lead_id}", campaignLeadHandler.Delete)
	})

	// Serve our merged OpenAPI 3 document (base endpoints + static Agents docs).
	// Registered explicitly so this static route takes precedence over the
	// /swagger/* wildcard below, overriding the spec http-swagger would serve.
	r.Get("/swagger/doc.json", swagger.DocJSONHandler())

	// Swagger UI: http://localhost:8080/swagger/index.html
	r.Get("/swagger/*", swagger.UIHandler())

	return r
}
