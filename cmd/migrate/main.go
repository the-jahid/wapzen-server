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
	var chatAgents, knowledgeOwner, toolOwner bool
	if err := pool.QueryRow(ctx, `SELECT
		to_regclass('chat_agents') IS NOT NULL,
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='knowledge_bases' AND column_name='chat_agent_id'),
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='tools' AND column_name='chat_agent_id')`).
		Scan(&chatAgents, &knowledgeOwner, &toolOwner); err != nil {
		log.Fatalf("database schema verification failed: %v", err)
	}
	if !chatAgents || !knowledgeOwner || !toolOwner {
		log.Fatalf("database schema verification failed: chat_agents=%t knowledge_owner=%t tool_owner=%t", chatAgents, knowledgeOwner, toolOwner)
	}
	log.Println("database migrations applied and chat-agent schema verified successfully")
}
