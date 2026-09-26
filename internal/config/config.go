package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"whatsapp-ai-caller-server/internal/embeddings"
)

// defaultPineconeIndexDimension is the native width of the default embedding
// model, and the dimension the project's index is created with. It is only a
// fallback: PINECONE_INDEX_DIMENSION must match whatever the index actually
// uses, or every write is rejected.
const defaultPineconeIndexDimension = 3072

// Config holds all environment-driven settings for the server.
type Config struct {
	Port                      string   // HTTP port the server listens on (e.g. "8080").
	AppEnv                    string   // Application environment (e.g. "development", "production").
	DatabaseURL               string   // PostgreSQL connection string.
	ClerkSecretKey            string   // Clerk backend API secret key.
	ClerkWebhookSigningSecret string   // Svix signing secret for Clerk webhooks.
	OpenAIAPIKey              string   // OpenAI API key used by voice call providers.
	ElevenLabsAPIKey          string   // ElevenLabs API key used by voice call providers.
	CORSAllowedOrigins        []string // Browser origins allowed to call the API (comma-separated CORS_ALLOWED_ORIGINS); empty means https://wapzen.io.

	// Knowledge base indexing. The embedding model and the Pinecone index must
	// agree on a vector width, so one dimension drives both: it is requested from
	// the embeddings API and checked against every vector before it is written.
	OpenAIEmbeddingModel   string // Embedding model used to index knowledge base sources.
	PineconeAPIKey         string // Pinecone API key.
	PineconeIndexName      string // Pinecone index name, for logging.
	PineconeIndexHost      string // Pinecone index data-plane host.
	PineconeIndexDimension int    // Vector width the Pinecone index was created with.
}

// Load reads configuration from a .env file (if one exists) and the
// process environment, falling back to sensible defaults.
func Load() *Config {
	// Load variables from a .env file if present. A missing file is not an
	// error, since values can also come from the system environment.
	if err := godotenv.Load(); err != nil {
		log.Println("config: no .env file found, using system environment variables")
	}

	openAIAPIKey := strings.TrimSpace(getEnv("OPENAI_API_KEY", ""))

	return &Config{
		Port:                      getEnv("PORT", "8080"),
		AppEnv:                    getEnv("APP_ENV", "development"),
		DatabaseURL:               getEnv("DATABASE_URL", ""),
		ClerkSecretKey:            getEnv("CLERK_SECRET_KEY", ""),
		ClerkWebhookSigningSecret: getEnv("CLERK_WEBHOOK_SIGNING_SECRET", ""),
		OpenAIAPIKey:              openAIAPIKey,
		ElevenLabsAPIKey:          getEnv("ELEVENLABS_API_KEY", ""),
		CORSAllowedOrigins:        getEnvList("CORS_ALLOWED_ORIGINS"),
		OpenAIEmbeddingModel:      getEnv("OPENAI_EMBEDDING_MODEL", embeddings.DefaultModel),
		PineconeAPIKey:            strings.TrimSpace(getEnv("PINECONE_API_KEY", "")),
		PineconeIndexName:         getEnv("PINECONE_INDEX_NAME", ""),
		PineconeIndexHost:         strings.TrimSpace(getEnv("PINECONE_INDEX_HOST", "")),
		PineconeIndexDimension:    getEnvInt("PINECONE_INDEX_DIMENSION", defaultPineconeIndexDimension),
	}
}

// getEnv returns the value of an environment variable, or a fallback if the
// variable is unset or empty.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

// getEnvList splits a comma-separated environment variable into its trimmed,
// non-empty entries, returning nil when the variable is unset or empty.
func getEnvList(key string) []string {
	var values []string
	for _, value := range strings.Split(os.Getenv(key), ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func getEnvInt(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return fallback
}
