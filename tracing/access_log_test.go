package tracing

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestRouter(t *testing.T) (*gin.Engine, *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	r := gin.New()
	r.Use(AccessLogMiddleware(logger))
	return r, logs
}

func TestAccessLogMiddleware_PanicKeepsTraceID(t *testing.T) {
	r, _ := newTestRouter(t)
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response body not valid JSON (traceId likely lost): %v, body=%s", err, w.Body.String())
	}
	if tid, _ := m["traceId"].(string); tid == "" {
		t.Fatal("traceId missing from panic response")
	}
}

func TestAccessLogMiddleware_InjectsDefaults(t *testing.T) {
	r, _ := newTestRouter(t)
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": gin.H{}}) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))

	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["code"] != float64(0) {
		t.Errorf("expected code=0, got %v", m["code"])
	}
	if m["message"] != "ok" {
		t.Errorf("expected message=ok, got %v", m["message"])
	}
	if tid, _ := m["traceId"].(string); tid == "" {
		t.Error("traceId missing")
	}
}

func TestAccessLogMiddleware_NotFoundKeepsTraceID(t *testing.T) {
	r, _ := newTestRouter(t)
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"code": 40401, "message": "not found", "data": gin.H{}})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if tid, _ := m["traceId"].(string); tid == "" {
		t.Error("traceId missing from 404 response")
	}
	if w.Header().Get("X-Trace-Id") == "" {
		t.Error("X-Trace-Id header missing from 404 response")
	}
}

func TestAccessLogMiddleware_SkipsMultipartBodyRead(t *testing.T) {
	r, _ := newTestRouter(t)
	var gotFormValue string
	r.POST("/upload", func(c *gin.Context) {
		gotFormValue, _ = c.GetPostForm("field")
		c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
	})

	body := &strings.Builder{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("field", "value")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if gotFormValue != "value" {
		t.Fatalf("multipart form value not readable by handler (body consumed by middleware?): got %q", gotFormValue)
	}
}

func TestAccessLogMiddleware_RedactsSensitiveRequestBodyOn5xx(t *testing.T) {
	r, logs := newTestRouter(t)
	r.POST("/fail", func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": "boom", "data": gin.H{}})
	})

	req := httptest.NewRequest(http.MethodPost, "/fail", strings.NewReader(`{"password":"hunter2","x":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	entries := logs.All()
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry")
	}
	last := entries[len(entries)-1]
	ctxMap := last.ContextMap()
	reqBody, _ := ctxMap["req_body"].(string)
	if strings.Contains(reqBody, "hunter2") {
		t.Fatalf("sensitive field leaked into log: %s", reqBody)
	}
	if !strings.Contains(reqBody, "***") {
		t.Fatalf("expected redacted marker in req_body: %s", reqBody)
	}
}

func TestAccessLogMiddleware_SkipsHealthCheckLogging(t *testing.T) {
	r, logs := newTestRouter(t)
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	for _, e := range logs.All() {
		if e.Message == "request" || e.Message == "request_error" {
			t.Fatalf("expected /health to skip access logging, got log entry: %+v", e)
		}
	}
}
