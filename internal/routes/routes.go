package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/handlers"
	"whatsapp-ai-caller-server/internal/knowledgebases"
	appmiddleware "whatsapp-ai-caller-server/internal/middleware"
	"whatsapp-ai-caller-server/internal/outboundcampaigns"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/swagger"
	"whatsapp-ai-caller-server/internal/tools"
	"whatsapp-ai-caller-server/internal/users"
	"whatsapp-ai-caller-server/internal/voiceusage"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

// Dependencies are the route-level dependencies provided by main.
type Dependencies struct {
	UsersRepo                 *users.Repository
	AgentsRepo                *agents.Repository
	PhoneNumbersRepo          *phonenumbers.Repository
	CallsRepo                 *calls.Repository
	KnowledgeBasesRepo        *knowledgebases.Repository
	KnowledgeBaseIndexer      *knowledgebases.Indexer
	OutboundCampaignsRepo     *outboundcampaigns.Repository
	ToolsRepo                 *tools.Repository
	VoiceUsageRepo            *voiceusage.Repository
	WhatsAppLoginManager      *whatsapplogin.Manager
	ClerkWebhookSigningSecret string
	CORSAllowedOrigins        []string
	// VerboseLogs adds a log line for every request. Off, the log carries only
	// user activity (see the activity package).
	VerboseLogs bool
}

// NewRouter builds the chi router with all middleware and routes registered.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()
	webhookHandler := handlers.NewWebhookHandler(deps.UsersRepo, deps.ClerkWebhookSigningSecret)
	// A nil manager must stay a nil interface, not a typed nil the service calls.
	var inboundRefresher agents.InboundCallRefresher
	if deps.WhatsAppLoginManager != nil {
		inboundRefresher = deps.WhatsAppLoginManager
	}
	agentController := agents.NewController(agents.NewService(deps.AgentsRepo, inboundRefresher, deps.KnowledgeBaseIndexer))
	userHandler := handlers.NewUserHandler()
	phoneNumberHandler := handlers.NewPhoneNumberHandler(
		deps.PhoneNumbersRepo,
		deps.WhatsAppLoginManager,
	)
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
	voiceUsageController := voiceusage.NewController(deps.VoiceUsageRepo)
	authMW := appmiddleware.NewAuth(deps.UsersRepo)

	// Built-in chi middleware.
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	if deps.VerboseLogs {
		r.Use(chimiddleware.Logger)
	}
	r.Use(chimiddleware.Recoverer)

	// Custom CORS middleware.
	r.Use(appmiddleware.CORS(deps.CORSAllowedOrigins))

	// Compress JSON (and the Swagger UI's text assets) for clients that accept
	// it. List responses repeat the same keys row after row, so they shrink
	// several times over, which is most of their transfer time on a slow link.
	r.Use(chimiddleware.Compress(5))

	// Health check.
	r.Get("/health", handlers.HealthCheck)

	// Clerk webhooks are public but verified by Svix signatures in the handler.
	r.Post("/api/webhooks/clerk", webhookHandler.Clerk)

	// Account API. RequireUser accepts Clerk session tokens and stores the resolved application user in the request context; handlers
	// read it via middleware.UserFromContext instead of trusting request
	// parameters.
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireUser)

		// Users.
		r.Get("/v1/users/me", userHandler.Me)

		// Voice minutes used and left on the account (10 free, more bought).
		r.Get("/v1/voice/usage", voiceUsageController.Get)

		// Voice WhatsApp numbers: QR / pairing-code login through whatsmeow,
		// stored in voice_phone_numbers. Everything voice lives under /voice/ so
		// it never collides with whiskey's chat routes (/v1/phone-number,
		// /v1/tools, /v1/knowledge-base), which serve chat's own tables. Login
		// sessions remain scoped to the authenticated user; paired clients are
		// adopted by the call runtime.
		r.Get("/v1/voice/phone-number", phoneNumberHandler.List)
		r.Post("/v1/voice/phone-number/login", phoneNumberHandler.Login)
		r.Get("/v1/voice/phone-number/{phone_number_id}", phoneNumberHandler.Get)
		r.Post("/v1/voice/phone-number/logout", phoneNumberHandler.Logout)

		// Dashboard agent access uses the logged-in user.
		r.Post("/v1/dashboard/agents", agentController.Create)
		r.Get("/v1/dashboard/agents", agentController.List)
		r.Get("/v1/dashboard/agents/{agent_id}", agentController.Get)
		r.Patch("/v1/dashboard/agents/{agent_id}", agentController.Update)
		r.Delete("/v1/dashboard/agents/{agent_id}", agentController.Delete)

		// Dashboard call history uses the logged-in user. These share the same
		// handlers as the /v1/calls routes below. Create places an outbound call;
		// inbound calls arrive through the voice-call runtime instead.
		r.Post("/v1/dashboard/calls", callHandler.Create)
		r.Get("/v1/dashboard/calls", callHandler.List)
		r.Get("/v1/dashboard/calls/{call_id}", callHandler.Get)
		r.Patch("/v1/dashboard/calls/{call_id}", callHandler.Update)
		r.Post("/v1/dashboard/calls/{call_id}/hangup", callHandler.Hangup)
		r.Delete("/v1/dashboard/calls/{call_id}", callHandler.Delete)

		// Dashboard knowledge bases, sharing the handlers with the
		// /v1/voice/knowledge-base routes below.
		r.Post("/v1/dashboard/voice/knowledge-base", knowledgeBaseHandler.Create)
		r.Get("/v1/dashboard/voice/knowledge-base", knowledgeBaseHandler.List)
		r.Get("/v1/dashboard/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Get)
		r.Patch("/v1/dashboard/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Update)
		r.Delete("/v1/dashboard/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Delete)
		r.Post("/v1/dashboard/voice/knowledge-base/{knowledge_base_id}/sources", knowledgeBaseHandler.AddSources)
		r.Delete("/v1/dashboard/voice/knowledge-base/{knowledge_base_id}/sources/{source_id}", knowledgeBaseHandler.DeleteSource)

		// Dashboard tools, sharing the handlers with the /v1/voice/tools routes
		// below. A tool defines an action; it is offered on a call only once an
		// agent attaches it through tools.tool_ids.
		r.Post("/v1/dashboard/voice/tools", toolHandler.Create)
		r.Get("/v1/dashboard/voice/tools", toolHandler.List)
		r.Get("/v1/dashboard/voice/tools/{tool_id}", toolHandler.Get)
		r.Patch("/v1/dashboard/voice/tools/{tool_id}", toolHandler.Update)
		r.Delete("/v1/dashboard/voice/tools/{tool_id}", toolHandler.Delete)
		r.Post("/v1/dashboard/voice/tools/{tool_id}/test", toolHandler.Test)

		// Dashboard outbound campaigns, sharing the handlers with the
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

	// Agent API: the same handlers under their plain /v1 paths. API keys were
	// removed, so these take the signed-in Clerk session like the dashboard routes.
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireUser)

		// Agents. Every operation is scoped to the authenticated user.
		r.Post("/v1/agents", agentController.Create)
		r.Get("/v1/agents", agentController.List)
		r.Get("/v1/agents/{agent_id}", agentController.Get)
		r.Patch("/v1/agents/{agent_id}", agentController.Update)
		r.Delete("/v1/agents/{agent_id}", agentController.Delete)

		// Calls. Placing and managing the calls handled for the authenticated user.
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
		r.Post("/v1/voice/knowledge-base", knowledgeBaseHandler.Create)
		r.Get("/v1/voice/knowledge-base", knowledgeBaseHandler.List)
		r.Get("/v1/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Get)
		r.Patch("/v1/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Update)
		r.Delete("/v1/voice/knowledge-base/{knowledge_base_id}", knowledgeBaseHandler.Delete)
		r.Post("/v1/voice/knowledge-base/{knowledge_base_id}/sources", knowledgeBaseHandler.AddSources)
		r.Delete("/v1/voice/knowledge-base/{knowledge_base_id}/sources/{source_id}", knowledgeBaseHandler.DeleteSource)

		// Tools. The actions an agent can take mid-call: hit an HTTP endpoint,
		// hand the caller over, send a WhatsApp message, hang up. Every operation
		// is scoped to the authenticated user, and a tool stays inert
		// until an agent attaches it through tools.tool_ids.
		r.Post("/v1/voice/tools", toolHandler.Create)
		r.Get("/v1/voice/tools", toolHandler.List)
		r.Get("/v1/voice/tools/{tool_id}", toolHandler.Get)
		r.Patch("/v1/voice/tools/{tool_id}", toolHandler.Update)
		r.Delete("/v1/voice/tools/{tool_id}", toolHandler.Delete)
		r.Post("/v1/voice/tools/{tool_id}/test", toolHandler.Test)

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
