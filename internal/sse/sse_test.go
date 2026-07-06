package sse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractUsageTokens(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{`data: {"choices":[{"delta":{"content":"hi"}}]}`, 0},
		{`data: {"choices":[],"usage":{"completion_tokens":42}}`, 42},
		{`data: [DONE]`, 0},
		{``, 0},
		{`not an sse line`, 0},
		{`data: not json`, 0},
	}
	for _, c := range cases {
		if got := ExtractUsageTokens(c.line); got != c.want {
			t.Errorf("ExtractUsageTokens(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

func TestRelay_HappyPath(t *testing.T) {
	src := strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"completion_tokens\":7}}\n\n" +
			"data: [DONE]\n\n",
	)
	rec := httptest.NewRecorder()
	started, tokens, err := Relay(rec, src)
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if !started {
		t.Error("expected started=true when lines were relayed")
	}
	if tokens != 7 {
		t.Errorf("tokens = %d, want 7", tokens)
	}
	if !strings.Contains(rec.Body.String(), "completion_tokens") {
		t.Errorf("relayed body missing expected content: %q", rec.Body.String())
	}
}

func TestRelay_EmptySourceNeverStarts(t *testing.T) {
	rec := httptest.NewRecorder()
	started, tokens, err := Relay(rec, strings.NewReader(""))
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if started {
		t.Error("expected started=false for an empty source (nothing relayed)")
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0", tokens)
	}
}

// nonFlusherWriter is a bare http.ResponseWriter that deliberately does NOT
// implement http.Flusher (unlike httptest.ResponseRecorder, which does) — used
// to exercise Relay's "streaming not supported" rejection path.
type nonFlusherWriter struct{ header http.Header }

func (w *nonFlusherWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *nonFlusherWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nonFlusherWriter) WriteHeader(int)             {}

func TestRelay_RejectsNonFlusherWriter(t *testing.T) {
	var w nonFlusherWriter
	if _, _, err := Relay(&w, strings.NewReader("data: hi\n\n")); err == nil {
		t.Error("expected an error when the writer doesn't support http.Flusher")
	}
}

func TestSetHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SetHeaders(rec)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}
