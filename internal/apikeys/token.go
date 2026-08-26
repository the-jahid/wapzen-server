package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// KeyPrefix makes application API keys distinguishable from Clerk session
	// JWTs before the auth middleware chooses a verification path.
	KeyPrefix = "wcai_"

	// DefaultName is used for the automatic key created with each new user.
	DefaultName = "Default"

	// MaxNameLength is mirrored by the api_keys.name database check.
	MaxNameLength = 80

	randomBytes = 32
)

// Generate returns a new bearer API key in a stable application-specific
// format. The random body is URL-safe and carries 256 bits of entropy.
func Generate() (string, error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the deterministic storage hash for a bearer API key.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// LooksLikeKey reports whether a bearer token should be verified as an
// application API key instead of as a Clerk JWT.
func LooksLikeKey(token string) bool {
	return strings.HasPrefix(token, KeyPrefix)
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultName
	}
	return name
}

func publicPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

func last4(key string) string {
	if len(key) <= 4 {
		return key
	}
	return key[len(key)-4:]
}
