// Package embeddings is a thin client for the OpenAI Embeddings API, used to
// turn knowledge base chunks into the vectors the vector store is queried by.
//
// The live call pipeline talks to OpenAI separately (internal/voicecall); this
// package deliberately covers embeddings only.
package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	defaultURL = "https://api.openai.com/v1/embeddings"

	// DefaultModel produces 3072-dimensional vectors, matching the dimension the
	// Pinecone index is created with.
	DefaultModel = "text-embedding-3-large"

	requestTimeout = 60 * time.Second

	// maxBatchInputs bounds how many chunks go into one request. The API accepts
	// far more, but a smaller batch keeps a single failure from costing the whole
	// source and keeps request bodies well inside the payload limit.
	maxBatchInputs = 64
)

// ErrNotConfigured is returned when no usable OpenAI API key is present.
var ErrNotConfigured = errors.New("embeddings: openai api key is not configured")

// APIError carries an upstream failure so callers can tell a bad request from
// an outage.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("embeddings: request failed (%d): %s", e.Status, e.Message)
}

// Config is the client's environment-driven configuration.
type Config struct {
	APIKey string
	Model  string
	// Dimensions requests a specific vector width. text-embedding-3-* models can
	// emit shorter vectors than their native size, which is how the client is
	// held to whatever dimension the index was created with. Zero leaves the
	// model's native width.
	Dimensions int
	URL        string
}

// Client calls the OpenAI Embeddings API.
type Client struct {
	apiKey     string
	model      string
	dimensions int
	url        string
	httpClient *http.Client
}

// New builds a client from cfg. A blank API key yields a client that reports
// Enabled() == false and fails every call with ErrNotConfigured, so callers can
// construct one unconditionally.
func New(cfg Config) *Client {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		url = defaultURL
	}
	return &Client{
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      model,
		dimensions: cfg.Dimensions,
		url:        url,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Enabled reports whether the client has a usable API key.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

// Model is the embedding model this client requests, for logging and for
// recording alongside the vectors it produced.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed returns one vector per input, in the same order. Inputs are sent in
// batches; a failure in any batch fails the whole call, since a partially
// embedded source is not worth indexing.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += maxBatchInputs {
		end := start + maxBatchInputs
		if end > len(inputs) {
			end = len(inputs)
		}
		batch, err := c.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model:      c.model,
		Input:      inputs,
		Dimensions: c.dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("embeddings: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embeddings: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings: request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embeddings: read response: %w", err)
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(payload, &decoded); err != nil && resp.StatusCode == http.StatusOK {
		return nil, fmt.Errorf("embeddings: decode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(payload))
		if decoded.Error != nil && decoded.Error.Message != "" {
			message = decoded.Error.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: message}
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(decoded.Data), len(inputs))
	}

	// The API documents the results as ordered by index rather than by position,
	// so they are sorted before being handed back as "one per input, in order".
	sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })

	vectors := make([][]float32, 0, len(decoded.Data))
	for i, item := range decoded.Data {
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings: empty vector for input %d", i)
		}
		vectors = append(vectors, item.Embedding)
	}
	return vectors, nil
}
