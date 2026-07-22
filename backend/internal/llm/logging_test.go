package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// capture returns a debug-level JSON logger writing into buf, plus a reader for
// the records it collected. slog writes each record as one JSON line.
func capture() (*slog.Logger, *syncBuf) {
	buf := &syncBuf{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// syncBuf is a bytes.Buffer safe for the heartbeat goroutine to write to while
// the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// records parses the captured lines into maps.
func (s *syncBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// find returns the first record whose msg matches, or nil.
func find(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

func countMsg(recs []map[string]any, msg string) int {
	n := 0
	for _, r := range recs {
		if r["msg"] == msg {
			n++
		}
	}
	return n
}

const usageBody = `{"choices":[{"message":{"content":"ok"}}],
	"usage":{"prompt_tokens":1200,"completion_tokens":340,"total_tokens":1540,
	"prompt_tokens_details":{"cached_tokens":800},
	"completion_tokens_details":{"reasoning_tokens":250}}}`

func TestComplete_logsUsageAndCallIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())

	totals := &Totals{}
	ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Title: "A Title", Channel: "A Channel", Totals: totals})
	if _, err := c.Complete(WithStep(ctx, "summary"), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	got := totals.Snapshot()
	want := Usage{Requests: 1, PromptTokens: 1200, CachedTokens: 800, CompletionTokens: 340, ReasoningTokens: 250, TotalTokens: 1540}
	if got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}

	done := find(buf.records(t), "llm: request done")
	if done == nil {
		t.Fatal("no 'llm: request done' record")
	}
	for k, want := range map[string]any{
		"step": "summary", "video_id": "vid1", "title": "A Title", "channel": "A Channel",
		"chat_tokens_in": "1.2k", "chat_tokens_cached": "800", "chat_tokens_total": "1.5k",
	} {
		if done[k] != want {
			t.Errorf("%s = %v, want %v", k, done[k], want)
		}
	}
}

func TestComplete_accumulatesTotalsAcrossCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, _ := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())

	totals := &Totals{}
	ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Totals: totals})
	for i := 0; i < 3; i++ {
		if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := totals.Snapshot(); got.Requests != 3 || got.PromptTokens != 3600 {
		t.Fatalf("totals = %+v", got)
	}
}

func TestComplete_worksWithoutCallInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())

	// No WithCall: a caller that never attached identity (or a nil Totals)
	// must still complete, and simply log less.
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	done := find(buf.records(t), "llm: request done")
	if done == nil {
		t.Fatal("no 'llm: request done' record")
	}
	if _, ok := done["video_id"]; ok {
		t.Errorf("video_id present without CallInfo: %v", done)
	}
}

func TestComplete_heartbeatsWhileWaiting(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log, HeartbeatInterval: 10 * time.Millisecond}, srv.Client())

	ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Title: "A Title", Channel: "A Channel"})
	go func() {
		time.Sleep(60 * time.Millisecond)
		close(release)
	}()
	if _, err := c.Complete(WithStep(ctx, "keypoints"), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	recs := buf.records(t)
	if n := countMsg(recs, "llm: still waiting for response"); n < 2 {
		t.Fatalf("heartbeats = %d, want >= 2", n)
	}
	hb := find(recs, "llm: still waiting for response")
	if hb["title"] != "A Title" || hb["channel"] != "A Channel" || hb["step"] != "keypoints" {
		t.Fatalf("heartbeat missing identity: %v", hb)
	}
	if _, ok := hb["elapsed_s"]; !ok {
		t.Fatalf("heartbeat missing elapsed_s: %v", hb)
	}
}

func TestComplete_heartbeatDisabledByNegativeInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log, HeartbeatInterval: -1}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if n := countMsg(buf.records(t), "llm: still waiting for response"); n != 0 {
		t.Fatalf("heartbeats = %d, want 0", n)
	}
}

func TestComplete_logsFailuresWithDurationAndKeepsErrorText(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name:    "non-2xx",
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
			wantErr: "chat failed with status 500",
		},
		{
			name:    "undecodable body",
			handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not json") },
			wantErr: "decode chat response",
		},
		{
			name:    "no choices",
			handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"choices":[]}`) },
			wantErr: "no choices",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			log, buf := capture()
			c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())

			ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Title: "A Title"})
			_, err := c.Complete(WithStep(ctx, "summary"), []Message{{Role: "user", Content: "hi"}})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			rec := find(buf.records(t), "llm: request failed")
			if rec == nil {
				t.Fatal("no 'llm: request failed' record")
			}
			if rec["level"] != "WARN" || rec["video_id"] != "vid1" || rec["step"] != "summary" {
				t.Errorf("failure record = %v", rec)
			}
			if _, ok := rec["duration_ms"]; !ok {
				t.Errorf("failure record has no duration_ms: %v", rec)
			}
		})
	}
}

func TestComplete_logsPacingSeparatelyFromLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	// An interval well past the 1s log threshold, so the second call's wait is
	// reported as pacing rather than read as a slow endpoint.
	c := NewClient(Config{BaseURL: srv.URL, Logger: log, RequestInterval: 1200 * time.Millisecond}, srv.Client())
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}
	rec := find(buf.records(t), "llm: paced")
	if rec == nil {
		t.Fatal("no 'llm: paced' record")
	}
	if ms, ok := rec["waited_ms"].(float64); !ok || ms < 1000 {
		t.Fatalf("waited_ms = %v", rec["waited_ms"])
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1.0k", 41213: "41.2k", 1_350_000: "1.35M"}
	for in, want := range cases {
		if got := FormatTokens(in); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestUsageLogAttrs_omitsZeroFields(t *testing.T) {
	attrs := Usage{Requests: 2, PromptTokens: 500}.LogAttrs()
	joined := strings.Join(keys(attrs), ",")
	if strings.Contains(joined, "chat_tokens_reasoning") || strings.Contains(joined, "chat_tokens_cached") {
		t.Fatalf("zero fields logged: %v", attrs)
	}
	if !strings.Contains(joined, "chat_requests") || !strings.Contains(joined, "chat_tokens_in") {
		t.Fatalf("missing set fields: %v", attrs)
	}
}

func TestTotals_nilIsSafe(t *testing.T) {
	var totals *Totals
	totals.Add(Usage{Requests: 1})
	if got := totals.Snapshot(); got != (Usage{}) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestUsageSub(t *testing.T) {
	after := Usage{Requests: 5, PromptTokens: 900, CachedTokens: 100, CompletionTokens: 80, ReasoningTokens: 40, TotalTokens: 980}
	before := Usage{Requests: 2, PromptTokens: 400, CachedTokens: 50, CompletionTokens: 30, ReasoningTokens: 10, TotalTokens: 430}
	want := Usage{Requests: 3, PromptTokens: 500, CachedTokens: 50, CompletionTokens: 50, ReasoningTokens: 30, TotalTokens: 550}
	if got := after.Sub(before); got != want {
		t.Fatalf("Sub = %+v, want %+v", got, want)
	}
}

// keys returns the key half of a flat slog key/value slice.
func keys(attrs []any) []string {
	var out []string
	for i := 0; i < len(attrs); i += 2 {
		if s, ok := attrs[i].(string); ok {
			out = append(out, s)
		}
	}
	return out
}
