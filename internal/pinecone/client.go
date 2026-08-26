// Package pinecone is a thin client for the Pinecone data plane used by the
// knowledge base indexer: writing a namespace's chunk vectors, querying them
// back, and deleting them again.
//
// Only the data plane is covered. The index itself is created out of band and
// addressed by its host, which is what PINECONE_INDEX_HOST carries.
package pinecone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout = 30 * time.Second

	// apiVersion pins the data-plane contract this client is written against, so
	// a server-side default moving on cannot change these calls underneath us.
	apiVersion = "2025-04"

	// maxUpsertBatch bounds the vectors per upsert request. Pinecone caps a
	// request at 2MB, and a 3072-dimension vector serializes to roughly 40KB of
	// JSON, so this leaves comfortable headroom.
	maxUpsertBatch = 32

	// maxDeleteBatch bounds the ids per delete request, matching Pinecone's
	// documented limit of 1000 ids.
	maxDeleteBatch = 1000

	// defaultTopK is how many matches a Query returns when the caller does not
	// say. Retrieval feeds a spoken answer, so a handful of passages is the
	// useful amount — more only costs the model tokens it will not read out.
	defaultTopK = 5

	// maxResponseBytes bounds a response body. A write's body is an
	// acknowledgement, but a query carries the matched chunks' text back, which is
	// why this is far larger than the few hundred bytes a write needs.
	maxResponseBytes = 4 << 20
)

// ErrNotConfigured is returned when the API key or index host is missing.
var ErrNotConfigured = errors.New("pinecone: api key or index host is not configured")

// APIError carries an upstream failure so callers can tell a rejected request
// from an outage.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("pinecone: request failed (%d): %s", e.Status, e.Message)
}

// Vector is one record written to the index: an id unique within the namespace,
// the embedding itself, and the metadata returned alongside a query match.
type Vector struct {
	ID       string         `json:"id"`
	Values   []float32      `json:"values"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Match is one result of a Query: the record's id, its similarity to the query
// vector (higher is closer, for the cosine metric the index is created with),
// and whatever metadata was stored with it.
type Match struct {
	ID       string         `json:"id"`
	Score    float32        `json:"score"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Config is the client's environment-driven configuration.
type Config struct {
	APIKey string
	// IndexHost is the index's data-plane host, with or without a scheme.
	IndexHost string
	// IndexName is not used to address the index — the host already does — but is
	// carried for logging, so a misrouted write is traceable to a config value.
	IndexName string
	// Dimension is the width the index was created with. Vectors of any other
	// width are rejected before the request is sent, since the API's own error
	// for it is far less specific.
	Dimension int
}

// Client calls the Pinecone data plane for a single index.
type Client struct {
	apiKey     string
	baseURL    string
	indexName  string
	dimension  int
	httpClient *http.Client
}

// New builds a client from cfg. A blank key or host yields a client that
// reports Enabled() == false and fails every call with ErrNotConfigured, so
// callers can construct one unconditionally.
func New(cfg Config) *Client {
	return &Client{
		apiKey:     strings.TrimSpace(cfg.APIKey),
		baseURL:    normalizeHost(cfg.IndexHost),
		indexName:  strings.TrimSpace(cfg.IndexName),
		dimension:  cfg.Dimension,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// normalizeHost accepts the host with or without a scheme and with or without a
// trailing slash, because both forms are copied out of the Pinecone console.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return strings.TrimRight(host, "/")
}

// Enabled reports whether the client has both an API key and an index host.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != "" && c.baseURL != ""
}

// IndexName is the configured index, for logging.
func (c *Client) IndexName() string {
	if c == nil {
		return ""
	}
	return c.indexName
}

// Dimension is the vector width the index was created with, or 0 when it is not
// configured and vectors are therefore not checked.
func (c *Client) Dimension() int {
	if c == nil {
		return 0
	}
	return c.dimension
}

type upsertRequest struct {
	Namespace string   `json:"namespace"`
	Vectors   []Vector `json:"vectors"`
}

type deleteRequest struct {
	Namespace string   `json:"namespace"`
	IDs       []string `json:"ids"`
}

type deleteAllRequest struct {
	Namespace string `json:"namespace"`
	DeleteAll bool   `json:"deleteAll"`
}

type queryRequest struct {
	Namespace       string    `json:"namespace"`
	Vector          []float32 `json:"vector"`
	TopK            int       `json:"topK"`
	IncludeMetadata bool      `json:"includeMetadata"`
}

type queryResponse struct {
	Matches []Match `json:"matches"`
}

// Upsert writes vectors into a namespace, replacing any existing record with
// the same id. It sends them in batches; a failed batch fails the call, leaving
// the batches before it written, so callers that need all-or-nothing delete the
// ids they asked for on error.
func (c *Client) Upsert(ctx context.Context, namespace string, vectors []Vector) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	if strings.TrimSpace(namespace) == "" {
		return errors.New("pinecone: namespace is required")
	}
	if len(vectors) == 0 {
		return nil
	}
	if c.dimension > 0 {
		for _, v := range vectors {
			if len(v.Values) != c.dimension {
				return fmt.Errorf("pinecone: vector %s has %d dimensions, index expects %d", v.ID, len(v.Values), c.dimension)
			}
		}
	}

	for start := 0; start < len(vectors); start += maxUpsertBatch {
		end := start + maxUpsertBatch
		if end > len(vectors) {
			end = len(vectors)
		}
		if _, err := c.post(ctx, "/vectors/upsert", upsertRequest{
			Namespace: namespace,
			Vectors:   vectors[start:end],
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteByIDs removes records from a namespace by id. Ids that are not present
// are ignored by the API, so this is safe to call as a rollback for an upsert
// that only partly ran.
func (c *Client) DeleteByIDs(ctx context.Context, namespace string, ids []string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	if strings.TrimSpace(namespace) == "" {
		return errors.New("pinecone: namespace is required")
	}
	if len(ids) == 0 {
		return nil
	}

	for start := 0; start < len(ids); start += maxDeleteBatch {
		end := start + maxDeleteBatch
		if end > len(ids) {
			end = len(ids)
		}
		if _, err := c.post(ctx, "/vectors/delete", deleteRequest{
			Namespace: namespace,
			IDs:       ids[start:end],
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteNamespace removes every record in a namespace. Pinecone answers 404 for
// a namespace that holds nothing, which is the state the caller wanted, so that
// one status is not treated as a failure.
func (c *Client) DeleteNamespace(ctx context.Context, namespace string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	if strings.TrimSpace(namespace) == "" {
		return errors.New("pinecone: namespace is required")
	}

	_, err := c.post(ctx, "/vectors/delete", deleteAllRequest{Namespace: namespace, DeleteAll: true})
	if isNotFound(err) {
		return nil
	}
	return err
}

// Query returns the topK records of a namespace closest to vector, each with
// the metadata it was written with, so a caller can turn a match straight into
// text without a second lookup.
//
// A namespace that holds nothing is not an error: the index answers either with
// no matches or with a 404, and both mean "nothing to retrieve here", which is a
// normal state for a knowledge base whose sources have not been indexed yet.
func (c *Client) Query(ctx context.Context, namespace string, vector []float32, topK int) ([]Match, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("pinecone: namespace is required")
	}
	if len(vector) == 0 {
		return nil, errors.New("pinecone: query vector is required")
	}
	if c.dimension > 0 && len(vector) != c.dimension {
		return nil, fmt.Errorf("pinecone: query vector has %d dimensions, index expects %d", len(vector), c.dimension)
	}
	if topK <= 0 {
		topK = defaultTopK
	}

	body, err := c.post(ctx, "/query", queryRequest{
		Namespace:       namespace,
		Vector:          vector,
		TopK:            topK,
		IncludeMetadata: true,
	})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var decoded queryResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("pinecone: decode query response: %w", err)
	}
	return decoded.Matches, nil
}

// isNotFound reports whether err is the API's 404, which several calls treat as
// the empty case rather than as a failure.
func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// post sends payload to path and returns the response body. Writes ignore it;
// Query decodes it.
func (c *Client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("pinecone: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("pinecone: build request: %w", err)
	}
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pinecone-API-Version", apiVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pinecone: request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("pinecone: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(responseBody))}
	}
	return responseBody, nil
}
