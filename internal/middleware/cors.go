package middleware

import "net/http"

// DefaultAllowedOrigin is the browser origin allowed to call the API when no
// other origins are configured. Requests without an Origin header (servers,
// curl, API-key integrations) are not affected; CORS is a browser rule, and
// access is still gated by API keys and Clerk tokens.
const DefaultAllowedOrigin = "https://wapzen.io"

// CORS lets the WapZen web app call the API from a browser, from any of
// allowedOrigins (matched exactly; an empty list means DefaultAllowedOrigin).
// Any other origin gets no CORS headers, so the browser blocks the response.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{DefaultAllowedOrigin}
	}
	allowedSet := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowedSet[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// The response depends on Origin, so caches must key on it.
			h.Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			_, allowed := allowedSet[origin]
			if allowed {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
				h.Set("Access-Control-Expose-Headers", "*")
				h.Set("Access-Control-Max-Age", "86400")

				// Echo whatever headers the preflight asks for instead of a fixed list.
				if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
					h.Set("Access-Control-Allow-Headers", requested)
				} else {
					h.Set("Access-Control-Allow-Headers", "*")
				}
			}

			// Short-circuit pre-flight requests.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if allowed {
					w.WriteHeader(http.StatusNoContent)
				} else {
					w.WriteHeader(http.StatusForbidden)
				}
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
