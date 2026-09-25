package middleware

import "net/http"

// CORS allows every origin, method and request header, so any site can call
// the API from a browser. Access is gated by API keys and Clerk tokens, not by
// origin.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
		h.Set("Access-Control-Expose-Headers", "*")
		h.Set("Access-Control-Max-Age", "86400")

		// Echo whatever headers the preflight asks for instead of a fixed list.
		if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
			h.Set("Access-Control-Allow-Headers", requested)
		} else {
			h.Set("Access-Control-Allow-Headers", "*")
		}

		// Short-circuit pre-flight requests.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
