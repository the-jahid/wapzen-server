package middleware

import "net/http"

// allowedOrigin is the only browser origin allowed to call the API. Requests
// without an Origin header (servers, curl, API-key integrations) are not
// affected; CORS is a browser rule, and access is still gated by API keys and
// Clerk tokens.
const allowedOrigin = "https://wapzen.io"

// CORS lets the WapZen web app at allowedOrigin call the API from a browser.
// Any other origin gets no CORS headers, so the browser blocks the response.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// The response depends on Origin, so caches must key on it.
		h.Add("Vary", "Origin")

		allowed := r.Header.Get("Origin") == allowedOrigin
		if allowed {
			h.Set("Access-Control-Allow-Origin", allowedOrigin)
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
