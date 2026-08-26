package voicecall

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Streaming a reply into speech is what keeps the caller from hearing dead air:
// text-to-speech can start on the first clause while the model is still writing
// the rest. speechChunker decides where those pieces end. The first piece is
// released as soon as any natural pause allows — it alone sets the response time
// the caller feels — while later pieces are held longer so sentences keep their
// prosody and a turn stays a handful of requests rather than dozens.
const (
	// Spoken replies often open with a short interjection — "Hi there!",
	// "Awesome!", "Sure." — and that opener is the cheapest thing to start
	// speaking. Requiring more length here made the first chunk swallow the
	// whole opening sentence, which cost hundreds of milliseconds of felt
	// latency per turn.
	firstChunkMinChars = 3
	// A leading clause ending in a comma ("Sure thing,") is a natural pause
	// once it is a few words long; release it so synthesis starts on the
	// greeting words themselves.
	firstSoftBoundaryChars = 8
	// Do not let an opening sentence with no punctuation hold the entire reply
	// hostage. Once the model has produced a useful short phrase, release it at
	// the next word boundary so TTS can begin while later tokens are generated.
	firstChunkMaxChars = 48
	laterChunkMinChars = 45
	// A comma is a weaker pause than a full stop, so only break there once
	// enough text has built up that the piece still sounds like a phrase.
	softBoundaryExtraChars = 45
	// A model that writes one long unpunctuated sentence should still stream:
	// past this length, break at the next word boundary.
	maxChunkChars = 220
)

type speechChunker struct {
	pending  []rune
	released int
}

// Push adds one streamed delta and returns the pieces that are ready to speak.
func (c *speechChunker) Push(delta string) []string {
	var chunks []string
	for _, r := range delta {
		c.pending = append(c.pending, r)
		if !c.isBoundary(r) {
			continue
		}
		if chunk := c.take(); chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

// Flush returns whatever text is left once the model stops writing.
func (c *speechChunker) Flush() string {
	return c.take()
}

func (c *speechChunker) take() string {
	chunk := strings.TrimSpace(string(c.pending))
	c.pending = c.pending[:0]
	if chunk == "" {
		return ""
	}
	c.released++
	return chunk
}

func (c *speechChunker) isBoundary(r rune) bool {
	n := len(c.pending)
	first := c.released == 0
	minChars := laterChunkMinChars
	if first {
		minChars = firstChunkMinChars
	}
	switch r {
	case '.', '!', '?', '\n', '。', '！', '？', '؟', '।':
		// A period inside "3.5" or a URL is not a pause.
		if r == '.' && n >= 2 && c.pending[n-2] >= '0' && c.pending[n-2] <= '9' {
			return false
		}
		return n >= minChars
	case ',', ';', ':', '—', '、', '，', '؛':
		if first {
			return n >= firstSoftBoundaryChars
		}
		return n >= minChars+softBoundaryExtraChars
	case ' ':
		if first && n >= firstChunkMaxChars {
			return true
		}
		return n >= maxChunkChars
	}
	return false
}

// scanSSE walks a server-sent-events body, handing every JSON data payload to
// onEvent. Lines this client does not model are skipped, so a provider adding a
// new event type cannot break a live call.
func scanSSE(body io.Reader, onEvent func(data []byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 8<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if err := onEvent([]byte(data)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

var (
	voiceTransportOnce sync.Once
	voiceTransport     *http.Transport
)

// voiceHTTPClient returns an HTTP client tuned for a live call: every voice
// endpoint shares one connection pool whose idle connections outlive a call, so
// a request made mid-conversation never pays for a fresh TCP and TLS handshake
// in front of the caller.
func voiceHTTPClient(timeout time.Duration) *http.Client {
	voiceTransportOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 32
		transport.MaxIdleConnsPerHost = 8
		transport.IdleConnTimeout = 5 * time.Minute
		transport.ForceAttemptHTTP2 = true
		voiceTransport = transport
	})
	return &http.Client{Timeout: timeout, Transport: voiceTransport}
}

// prewarmEndpoints opens a pooled connection to each endpoint's host while the
// call is still being set up, so the first transcription or completion of the
// call starts on an established connection. Failures are ignored: this only ever
// saves time, it never gates the call.
func prewarmEndpoints(ctx context.Context, client *http.Client, endpoints ...string) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	seen := make(map[string]bool, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(strings.TrimSpace(endpoint))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			continue
		}
		origin := parsed.Scheme + "://" + parsed.Host + "/"
		if seen[origin] {
			continue
		}
		seen[origin] = true

		req, err := http.NewRequestWithContext(ctx, http.MethodHead, origin, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
	}
}
