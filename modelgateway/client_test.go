package modelgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletion_ParsesContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected Authorization header: %s", got)
		}
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "sa-translate" || req.Stream {
			t.Fatalf("unexpected request body: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[\"你好\"]"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", nil)
	content, err := c.ChatCompletion(context.Background(), "sa-translate", []Message{{Role: "user", Content: "hi"}}, WithMaxTokens(30))
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if content != `["你好"]` {
		t.Fatalf("unexpected content: %s", content)
	}
}

func TestChatCompletion_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream error`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", nil)
	if _, err := c.ChatCompletion(context.Background(), "sa-translate", []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
}

func TestEmbeddings_OrdersByResponseIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		// Deliberately return out of order to verify the client re-sorts by index.
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.2],"index":1},{"embedding":[0.1],"index":0}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", nil)
	vecs, err := c.Embeddings(context.Background(), "text-embedding-3-small", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embeddings returned error: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Fatalf("unexpected vectors: %+v", vecs)
	}
}

func TestEmbeddings_EmptyInputReturnsNilWithoutCallingServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called for empty input")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key", nil)
	vecs, err := c.Embeddings(context.Background(), "text-embedding-3-small", nil)
	if err != nil || vecs != nil {
		t.Fatalf("expected (nil, nil) for empty input, got (%v, %v)", vecs, err)
	}
}

func TestRerank_ReturnsNotImplemented(t *testing.T) {
	c := NewClient("http://example.invalid", "test-key", nil)
	if _, err := c.Rerank(context.Background(), "any", "query", []string{"doc"}); err != ErrNotImplemented {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}
