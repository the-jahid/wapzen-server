package auth

import (
	"fmt"

	"github.com/clerk/clerk-sdk-go/v2"
)

// Init configures the Clerk SDK with the backend secret key. It must run
// before any request passes through middleware.Auth, which verifies tokens
// and calls the Clerk API using this key.
func Init(secretKey string) error {
	if secretKey == "" {
		return fmt.Errorf("CLERK_SECRET_KEY is required")
	}
	clerk.SetKey(secretKey)
	return nil
}
