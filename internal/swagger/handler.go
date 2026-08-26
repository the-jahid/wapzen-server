package swagger

import (
	"encoding/json"
	"net/http"
	"sync"
)

// The document is static, so render it once and reuse the bytes.
var (
	docOnce  sync.Once
	docBytes []byte
	docErr   error
)

func renderDocument() ([]byte, error) {
	docOnce.Do(func() {
		docBytes, docErr = json.MarshalIndent(BuildDocument(), "", "  ")
	})
	return docBytes, docErr
}

// DocJSONHandler serves the merged OpenAPI 3 document (base endpoints + static
// Agents docs) as JSON. It is mounted at /swagger/doc.json so it overrides the
// spec that swaggo's http-swagger handler would otherwise serve.
func DocJSONHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := renderDocument()
		if err != nil {
			http.Error(w, "failed to render OpenAPI document", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}
