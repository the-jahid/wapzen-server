package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	clerkjwt "github.com/clerk/clerk-sdk-go/v2/jwt"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"

	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/users"
)

// userContextKey is the private context key under which the authenticated
// application user is stored. A private struct type guarantees no other
// package can collide with (or spoof) the value.
type userContextKey struct{}

// UserFromContext returns the application user resolved by Auth.RequireUser.
// The second return is false when the request did not pass through the
// middleware — protected handlers treat that as an authentication failure.
func UserFromContext(ctx context.Context) (models.User, bool) {
	u, ok := ctx.Value(userContextKey{}).(models.User)
	return u, ok
}

// Auth authenticates requests with Clerk session tokens or generated API keys
// and resolves them to application users. Attach it to any route group that
// must only be reachable by authenticated users:
//
//	authMW := middleware.NewAuth(usersRepo, apiKeysRepo)
//	r.Group(func(r chi.Router) {
//		r.Use(authMW.RequireUser)
//		r.Get("/v1/things", handler.List) // handler calls middleware.UserFromContext
//	})
type Auth struct {
	users   *users.Repository
	apiKeys *apikeys.Repository
}

// NewAuth creates the auth middleware with its dependencies. The Clerk SDK
// must already be configured via auth.Init (clerk.SetKey).
func NewAuth(usersRepo *users.Repository, apiKeysRepo *apikeys.Repository) *Auth {
	return &Auth{users: usersRepo, apiKeys: apiKeysRepo}
}

// RequireUser verifies the bearer token in the Authorization header. Clerk JWTs
// are verified against Clerk's JWKS; generated API keys are verified by hash
// lookup in Postgres. Both auth paths resolve to the application user row and
// store it in the request context for handlers to read via UserFromContext.
// Requests without a valid token get a 401 JSON envelope and never reach the
// wrapped handler.
//
// Users normally exist already (mirrored by the Clerk webhook), but the first
// request can win the race against webhook delivery, so an unknown Clerk user
// is provisioned just-in-time from the Clerk API.
func (a *Auth) RequireUser(next http.Handler) http.Handler {
	// WithHeaderAuthorization verifies the JWT signature against Clerk's JWKS
	// (fetched once and cached) and stores the session claims in the context.
	// It only invokes the failure handler for tokens that fail verification;
	// missing or undecodable tokens fall through with no claims, so the inner
	// handler below must re-check that claims are present.
	verifyToken := clerkhttp.WithHeaderAuthorization(
		clerkhttp.AuthorizationFailureHandler(http.HandlerFunc(writeUnauthorized)),
		clerkhttp.Leeway(30*time.Second),
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeUnauthorized(w, r)
			return
		}

		if apikeys.LooksLikeKey(token) {
			user, err := a.resolveAPIKeyUser(r.Context(), token)
			if err != nil {
				if errors.Is(err, apikeys.ErrNotFound) {
					writeUnauthorized(w, r)
					return
				}
				log.Printf("auth: resolving user for api key failed: %v", err)
				writeAuthJSON(w, http.StatusInternalServerError, "failed to resolve the authenticated user")
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		verifyToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := clerk.SessionClaimsFromContext(r.Context())
			if !ok || claims == nil {
				writeUnauthorized(w, r)
				return
			}

			user, err := a.resolveUser(r.Context(), claims.Subject)
			if err != nil {
				log.Printf("auth: resolving user for clerk id %q failed: %v", claims.Subject, err)
				writeAuthJSON(w, http.StatusInternalServerError, "failed to resolve the authenticated user")
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})).ServeHTTP(w, r)
	})
}

// RequireAPIKeyUser verifies that the Authorization bearer token is one of the
// application's generated API keys. Clerk session tokens are intentionally not
// accepted by this middleware; use it for live API routes that should only be
// callable with developer API keys.
func (a *Auth) RequireAPIKeyUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || !apikeys.LooksLikeKey(token) {
			writeAPIKeyUnauthorized(w, r)
			return
		}

		user, err := a.resolveAPIKeyUser(r.Context(), token)
		if err != nil {
			if errors.Is(err, apikeys.ErrNotFound) {
				writeAPIKeyUnauthorized(w, r)
				return
			}
			log.Printf("auth: resolving user for api key failed: %v", err)
			writeAuthJSON(w, http.StatusInternalServerError, "failed to resolve the authenticated user")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Auth) resolveAPIKeyUser(ctx context.Context, token string) (models.User, error) {
	if a.apiKeys == nil {
		return models.User{}, fmt.Errorf("api key repository is not configured")
	}
	return a.apiKeys.Authenticate(ctx, token)
}

// resolveUser maps a verified Clerk user id to the application user row,
// provisioning the row from the Clerk API when the webhook has not created it
// yet.
func (a *Auth) resolveUser(ctx context.Context, clerkID string) (models.User, error) {
	user, err := a.users.GetByClerkID(ctx, clerkID)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, users.ErrNotFound) {
		return models.User{}, err
	}

	clerkUser, err := clerkuser.Get(ctx, clerkID)
	if err != nil {
		return models.User{}, fmt.Errorf("fetch clerk user: %w", err)
	}
	email := primaryEmail(clerkUser)
	if email == "" {
		return models.User{}, fmt.Errorf("clerk user %s has no email address", clerkID)
	}

	return a.users.Upsert(ctx, users.UpsertParams{
		ClerkID:  clerkID,
		Email:    email,
		Username: clerkUser.Username,
	})
}

// primaryEmail picks the Clerk user's primary email address, falling back to
// the first one on record.
func primaryEmail(u *clerk.User) string {
	if u.PrimaryEmailAddressID != nil {
		for _, e := range u.EmailAddresses {
			if e != nil && e.ID == *u.PrimaryEmailAddressID {
				return e.EmailAddress
			}
		}
	}
	for _, e := range u.EmailAddresses {
		if e != nil && e.EmailAddress != "" {
			return e.EmailAddress
		}
	}
	return ""
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	logRejectedToken(r)
	writeAuthJSON(w, http.StatusUnauthorized, "Authentication required: send a valid Clerk session token or API key in the Authorization header")
}

func writeAPIKeyUnauthorized(w http.ResponseWriter, r *http.Request) {
	logRejectedAPIKey(r)
	writeAuthJSON(w, http.StatusUnauthorized, "Authentication required: send a valid API key in the Authorization header")
}

func logRejectedAPIKey(r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		log.Printf("auth: rejected api-key-only request without bearer token path=%s", r.URL.Path)
		return
	}
	if apikeys.LooksLikeKey(token) {
		log.Printf("auth: rejected api key path=%s key=%s", r.URL.Path, tokenLogHint(token))
		return
	}
	log.Printf("auth: rejected non-api-key bearer token on api-key-only route path=%s", r.URL.Path)
}

func logRejectedToken(r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		log.Printf("auth: rejected request without bearer token path=%s", r.URL.Path)
		return
	}
	if apikeys.LooksLikeKey(token) {
		log.Printf("auth: rejected api key path=%s key=%s", r.URL.Path, tokenLogHint(token))
		return
	}

	decoded, err := clerkjwt.Decode(r.Context(), &clerkjwt.DecodeParams{Token: token})
	if err != nil {
		log.Printf("auth: rejected request with undecodable bearer token path=%s err=%v", r.URL.Path, err)
		return
	}

	now := time.Now().UTC().Unix()
	var secondsUntilExp any = nil
	if decoded.Expiry != nil {
		secondsUntilExp = *decoded.Expiry - now
	}
	log.Printf(
		"auth: rejected bearer token path=%s kid=%s iss=%s sub=%s sid=%v iat=%v nbf=%v exp=%v now=%d seconds_until_exp=%v",
		r.URL.Path,
		decoded.KeyID,
		decoded.Issuer,
		decoded.Subject,
		decoded.Extra["sid"],
		decoded.IssuedAt,
		decoded.NotBefore,
		decoded.Expiry,
		now,
		secondsUntilExp,
	)
}

func bearerToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, ok := strings.Cut(raw, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func tokenLogHint(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:8] + "..." + token[len(token)-4:]
}

// writeAuthJSON emits the project's standard error envelope. Mirrors the
// handlers package's writeJSON, which is unexported there.
func writeAuthJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: message})
}
