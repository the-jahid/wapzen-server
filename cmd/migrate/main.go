// Command migrate applies the embedded PostgreSQL migrations without starting
// the API server or reconnecting WhatsApp sessions.
package main

import (
	"context"
	"log"
	"time"

	"whatsapp-ai-caller-server/internal/config"
	"whatsapp-ai-caller-server/internal/db"
)

func main() {
	cfg := config.Load()
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database verification connection failed: %v", err)
	}
	defer pool.Close()
	// A knowledge base still carries its chat agent on its own row; a tool is
	// shared, so its attachments live in the two join tables instead.
	var chatAgents, knowledgeOwner, agentTools, chatAgentTools bool
	if err := pool.QueryRow(ctx, `SELECT
		to_regclass('chat_agents') IS NOT NULL,
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='knowledge_bases' AND column_name='chat_agent_id'),
		to_regclass('agent_tools') IS NOT NULL,
		to_regclass('chat_agent_tools') IS NOT NULL`).
		Scan(&chatAgents, &knowledgeOwner, &agentTools, &chatAgentTools); err != nil {
		log.Fatalf("database schema verification failed: %v", err)
	}
	if !chatAgents || !knowledgeOwner || !agentTools || !chatAgentTools {
		log.Fatalf("database schema verification failed: chat_agents=%t knowledge_owner=%t agent_tools=%t chat_agent_tools=%t",
			chatAgents, knowledgeOwner, agentTools, chatAgentTools)
	}
	log.Println("database migrations applied and chat-agent schema verified successfully")
}
