package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/auth"
	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/chatagents"
	"whatsapp-ai-caller-server/internal/chatconversations"
	"whatsapp-ai-caller-server/internal/config"
	"whatsapp-ai-caller-server/internal/db"
	"whatsapp-ai-caller-server/internal/embeddings"
	"whatsapp-ai-caller-server/internal/knowledgebases"
	"whatsapp-ai-caller-server/internal/outboundcampaigns"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/pinecone"
	"whatsapp-ai-caller-server/internal/routes"
	"whatsapp-ai-caller-server/internal/tools"
	"whatsapp-ai-caller-server/internal/users"
	"whatsapp-ai-caller-server/internal/whatsapplogin"

	// Blank import of the generated Swagger docs. The package is created by
	// running: swag init -g cmd/api/main.go
	_ "whatsapp-ai-caller-server/docs"
)

// @title           WhatsApp AI Caller API
// @version         1.0
// @description     Backend API foundation for the WhatsApp AI Voice Caller application.

// @host            localhost:8080
// @BasePath        /
// @schemes         http
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	// Match high-resolution timestamps so logs can be correlated precisely
	// during live verification.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Tee logs to a file for offline debugging.
	// Must run before any component logger is built.
	if logFile, err := os.OpenFile("tmp/server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		defer logFile.Close()
	} else {
		log.Printf("could not open tmp/server.log for log tee: %v", err)
	}

	// Load configuration from .env / environment variables.
	cfg := config.Load()
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}

	if err := auth.Init(cfg.ClerkSecretKey); err != nil {
		log.Fatalf("clerk configuration failed: %v", err)
	}

	usersRepo := users.NewRepository(pool)
	agentsRepo := agents.NewRepository(pool)
	chatAgentsRepo := chatagents.NewRepository(pool)
	chatConversationsRepo := chatconversations.NewRepository(pool)
	apiKeysRepo := apikeys.NewRepository(pool)
	phoneNumbersRepo := phonenumbers.NewRepository(pool)
	callsRepo := calls.NewRepository(pool)
	knowledgeBasesRepo := knowledgebases.NewRepository(pool)
	outboundCampaignsRepo := outboundcampaigns.NewRepository(pool)
	toolsRepo := tools.NewRepository(pool)

	// Knowledge base indexing and the retrieval that reads it back. Both upstreams
	// are constructed unconditionally; missing credentials leave the indexer
	// disabled, which only the endpoints that index anything refuse over.
	//
	// The indexer and the retriever deliberately share one embedding client: a
	// query is only comparable to the chunks it searches when both were embedded
	// by the same model at the same width.
	knowledgeEmbedder := embeddings.New(embeddings.Config{
		APIKey:     cfg.OpenAIAPIKey,
		Model:      cfg.OpenAIEmbeddingModel,
		Dimensions: cfg.PineconeIndexDimension,
	})
	knowledgeVectors := pinecone.New(pinecone.Config{
		APIKey:    cfg.PineconeAPIKey,
		IndexHost: cfg.PineconeIndexHost,
		IndexName: cfg.PineconeIndexName,
		Dimension: cfg.PineconeIndexDimension,
	})
	knowledgeBaseIndexer := knowledgebases.NewIndexer(knowledgeBasesRepo, knowledgeEmbedder, knowledgeVectors)
	knowledgeBaseRetriever := knowledgebases.NewRetriever(knowledgeEmbedder, knowledgeVectors)
	if knowledgeBaseIndexer.Enabled() {
		log.Printf("knowledge base indexing enabled: %s -> pinecone index %q (%d dimensions)",
			cfg.OpenAIEmbeddingModel, cfg.PineconeIndexName, cfg.PineconeIndexDimension)
	} else {
		log.Printf("knowledge base indexing disabled: set OPENAI_API_KEY, PINECONE_API_KEY and PINECONE_INDEX_HOST to enable it")
	}

	whatsAppLoginManager, err := whatsapplogin.NewManager(ctx, cfg.DatabaseURL, phoneNumbersRepo, agentsRepo, chatAgentsRepo, callsRepo, knowledgeBaseRetriever)
	if err != nil {
		log.Fatalf("WhatsApp login manager failed: %v", err)
	}
	defer whatsAppLoginManager.Close()

	// Save the text threads the chat agents answer, so a conversation outlives
	// the process that held it in memory and can be read back in the dashboard.
	whatsAppLoginManager.UseChatStore(chatConversationsRepo)

	// Reconnect already-paired numbers so they keep receiving messages after a
	// restart, instead of sitting "connected" in the DB with no live client.
	whatsAppLoginManager.ResumeSessions(ctx)

	// Build the router with all middleware and routes.
	router := routes.NewRouter(routes.Dependencies{
		UsersRepo:                 usersRepo,
		AgentsRepo:                agentsRepo,
		ChatAgentsRepo:            chatAgentsRepo,
		ChatConversationsRepo:     chatConversationsRepo,
		APIKeysRepo:               apiKeysRepo,
		PhoneNumbersRepo:          phoneNumbersRepo,
		CallsRepo:                 callsRepo,
		KnowledgeBasesRepo:        knowledgeBasesRepo,
		KnowledgeBaseIndexer:      knowledgeBaseIndexer,
		OutboundCampaignsRepo:     outboundCampaignsRepo,
		ToolsRepo:                 toolsRepo,
		WhatsAppLoginManager:      whatsAppLoginManager,
		ClerkWebhookSigningSecret: cfg.ClerkWebhookSigningSecret,
		ElevenLabsAPIKey:          cfg.ElevenLabsAPIKey,
		CORSAllowedOrigins:        cfg.CORSAllowedOrigins,
	})

	addr := ":" + cfg.Port

	log.Printf("WhatsApp AI Caller server starting in %q mode", cfg.AppEnv)
	log.Printf("Listening on   http://localhost%s", addr)
	log.Printf("Swagger UI at  http://localhost%s/swagger/index.html", addr)

	server := &http.Server{Addr: addr, Handler: router}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown failed: %v", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server failed to start: %v", err)
	}
}
