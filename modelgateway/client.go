// Package modelgateway is the shared transport for all internal calls to the company's model
// gateway (api.model.selleraim.com, OpenAI-Chat-Completions-compatible). It exists so that
// sa-go-platform's translation services and sa-go-product's similarity-matching code share one
// request/auth/response-parsing implementation instead of each hand-rolling an http.Client
// (TECH_CHARTER 统一传输层 / CLAUDE.md 黄金法则 6). See specs/023-catalog-reverse-sync/research.md
// D3-D5/D9 for the design rationale.
package modelgateway

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

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/zh391878487-commits/sa-go-common/logging"
	"github.com/zh391878487-commits/sa-go-common/tracing"
)

// defaultBaseURL is the model gateway's public endpoint. Both ChatCompletion and Embeddings
// currently share the same host (research.md D9 — the gateway has no dedicated /v1/embeddings
// route confirmed working as of 2026-09-18; see Embeddings doc comment for the unresolved 502).
const defaultBaseURL = "https://api.model.selleraim.com"

// Message is one entry in a ChatCompletion request's messages array.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RerankResult is one scored document returned by Rerank.
type RerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// ErrNotImplemented is returned by Rerank — Phase 1 of 023 does not do rerank (research.md D7);
// the method exists on the interface now so adding a real implementation later does not break
// callers that already depend on this interface.
var ErrNotImplemented = errors.New("modelgateway: not implemented")

// Client is the shared model gateway transport. All calls to api.model.selleraim.com from
// sa-go-platform/sa-go-product must go through an implementation of this interface — no bare
// http.Client, no per-call-site request building (CLAUDE.md 规范 14 精神，扩展到内部模型网关).
type Client interface {
	// ChatCompletion sends a single chat completion request and returns the assistant message
	// content. opts is currently only used by WithMaxTokens.
	ChatCompletion(ctx context.Context, model string, messages []Message, opts ...ChatOption) (string, error)

	// Embeddings returns one embedding vector per input text, in the same order as texts.
	Embeddings(ctx context.Context, model string, texts []string) ([][]float32, error)

	// Rerank is a reserved signature — Phase 1 does not implement it (research.md D7); calling
	// it returns ErrNotImplemented.
	Rerank(ctx context.Context, model, query string, docs []string) ([]RerankResult, error)
}

type client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	rdb        *redis.Client
	rateLimit  *rateLimitConfig
}

// Option configures a Client at construction time.
type Option func(*client)

// WithRateLimit enables token-bucket rate limiting for every call made through this Client
// instance, under the given dimension. Different call sites must use separately-constructed
// Client instances if they need independent limits (research.md D5) — e.g. sa-go-platform's two
// translate services should either omit this option or pass a generous "translate" dimension,
// while sa-go-product's new similarity-matching call site passes its own tuned "embedding"
// dimension. Without this option, the Client never rate-limits (D5 boundary).
func WithRateLimit(dimension string, capacity, refillRate float64) Option {
	return func(c *client) {
		c.rateLimit = &rateLimitConfig{dimension: dimension, capacity: capacity, refillRate: refillRate}
	}
}

// WithBaseURL overrides the default gateway host — primarily for tests.
func WithBaseURL(baseURL string) Option {
	return func(c *client) {
		c.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithHTTPClient overrides the underlying http.Client — primarily for tests. The caller is
// responsible for wrapping Transport with tracing.RoundTripper if they need traceId propagation.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *client) {
		c.httpClient = hc
	}
}

// NewClient builds a Client. rdb may be nil if no call site on this instance ever uses
// WithRateLimit (acquire() no-ops without a Redis client).
func NewClient(baseURL, apiKey string, rdb *redis.Client, opts ...Option) Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	c := &client{
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: tracing.RoundTripper(nil),
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		rdb:     rdb,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// chatCompletionConfig holds per-call ChatCompletion overrides.
type chatCompletionConfig struct {
	maxTokens int
}

// ChatOption configures a single ChatCompletion call.
type ChatOption func(*chatCompletionConfig)

// WithMaxTokens caps the response length. The two pre-existing translate services each computed
// this dynamically as len(names)*30 — migrated call sites must pass the equivalent explicit
// value via this option to preserve their existing behavior exactly (research.md D4).
func WithMaxTokens(n int) ChatOption {
	return func(c *chatCompletionConfig) { c.maxTokens = n }
}

const defaultMaxTokens = 2000

type chatCompletionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// ChatCompletion posts to /v1/chat/completions and returns choices[0].message.content. This is
// the request/response shape migrated verbatim from sa-go-platform's
// browse_tree_translate_service.go / shopify_taxonomy_translate_service.go (research.md D3/D4) —
// the only endpoint on this gateway confirmed working via real calls.
func (c *client) ChatCompletion(ctx context.Context, model string, messages []Message, opts ...ChatOption) (string, error) {
	if err := acquire(ctx, c.rdb, c.rateLimit); err != nil {
		return "", err
	}

	cfg := chatCompletionConfig{maxTokens: defaultMaxTokens}
	for _, opt := range opts {
		opt(&cfg)
	}

	reqBody := chatCompletionRequest{
		Model:     model,
		Messages:  messages,
		Stream:    false,
		MaxTokens: cfg.maxTokens,
	}
	respBytes, err := c.post(ctx, "/v1/chat/completions", reqBody)
	if err != nil {
		return "", fmt.Errorf("chat completion: %w", err)
	}

	var apiResp chatCompletionResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return "", fmt.Errorf("parse chat completion response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("chat completion: empty choices in response")
	}
	return apiResp.Choices[0].Message.Content, nil
}

// embeddingsRequest/embeddingsResponse follow the OpenAI-standard /v1/embeddings contract.
//
// UNVERIFIED (research.md D9/T006, checked 2026-09-18): real calls to both
// POST /v1/chat/completions {"model":"text-embedding-3-small","messages":[...]} and
// POST /v1/embeddings {"model":"text-embedding-3-small","input":[...]} against the live gateway
// (from inside the dev-bill cluster, using the real SA_TRANSLATE_API_KEY) returned
// "502 Bad Gateway" for both shapes, while a control call to the known-working
// /v1/chat/completions + model=sa-translate succeeded — so auth and connectivity are confirmed
// good, and GET /v1/models does list "text-embedding-3-small" as a registered model, but the
// backend integration for that model appears broken/unwired server-side. This implementation
// uses the industry-standard shape (matches what any OpenAI-SDK-compatible client would send for
// a model named "text-embedding-3-small"); it has NOT been validated end-to-end against a
// successful response and must be re-checked once the gateway team fixes the 502. Per FR-004,
// callers must already treat an Embeddings error as "no candidate" (safe degrade), so shipping
// this unverified is not a functional regression — matching just won't auto-recall candidates
// until the gateway side is fixed.
type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (c *client) Embeddings(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if err := acquire(ctx, c.rdb, c.rateLimit); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}

	respBytes, err := c.post(ctx, "/v1/embeddings", embeddingsRequest{Model: model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}

	var apiResp embeddingsResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("parse embeddings response: %w", err)
	}
	if len(apiResp.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: expected %d vectors, got %d", len(texts), len(apiResp.Data))
	}

	out := make([][]float32, len(texts))
	for _, d := range apiResp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embeddings: response index %d out of range", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// Rerank is not implemented in Phase 1 (research.md D7).
func (c *client) Rerank(ctx context.Context, model, query string, docs []string) ([]RerankResult, error) {
	return nil, ErrNotImplemented
}

// post issues an authenticated POST and returns the raw response body, mirroring the
// error-handling shape of the pre-migration translate services (non-200 → error with body).
func (c *client) post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		logging.FromContext(ctx).Warn("model gateway non-200 response",
			zap.String("path", path), zap.Int("status", resp.StatusCode), zap.String("body", truncate(string(respBytes), 500)))
		return nil, fmt.Errorf("model gateway %s returned %d: %s", path, resp.StatusCode, truncate(string(respBytes), 500))
	}
	return respBytes, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
