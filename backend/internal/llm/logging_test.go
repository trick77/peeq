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
	// Timings vary per run, so compare the accounting and check them separately.
	tokensOnly := got
	tokensOnly.InferenceNanos, tokensOnly.PacedNanos = 0, 0
	want := Usage{Requests: 1, Reported: true, PromptTokens: 1200, CachedTokens: 800, CompletionTokens: 340, ReasoningTokens: 250, TotalTokens: 1540}
	if tokensOnly != want {
		t.Fatalf("totals = %+v, want %+v", tokensOnly, want)
	}
	if got.InferenceNanos <= 0 {
		t.Errorf("inference time not recorded: %+v", got)
	}
	if got.PacedNanos != 0 {
		t.Errorf("paced time recorded without a RequestInterval: %+v", got)
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

func TestComplete_logsAReportedZeroRatherThanDroppingIt(t *testing.T) {
	// MiMo-shaped reply: the details objects are there, the numbers in them are
	// zero. That zero is the answer and must reach the log.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],
			"usage":{"prompt_tokens":900,"completion_tokens":100,"total_tokens":1000,
			"prompt_tokens_details":{"cached_tokens":0},
			"completion_tokens_details":{"reasoning_tokens":0}}}`)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	done := find(buf.records(t), "llm: request done")
	if done["chat_tokens_reasoning"] != "0" || done["chat_tokens_cached"] != "0" {
		t.Errorf("reported zeros missing from the line: %v", done)
	}
}

func TestComplete_logsRawUsageAndTheAbsenceOfIt(t *testing.T) {
	t.Run("reported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, usageBody)
		}))
		defer srv.Close()
		log, buf := capture()
		c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())
		if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
		raw := find(buf.records(t), "llm: usage raw")
		if raw == nil {
			t.Fatal("no raw usage record")
		}
		// Verbatim, so a field name we do not parse is still visible.
		if s, _ := raw["usage"].(string); !strings.Contains(s, "reasoning_tokens") {
			t.Errorf("raw usage = %v", raw["usage"])
		}
	})
	t.Run("absent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}))
		defer srv.Close()
		log, buf := capture()
		c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())
		totals := &Totals{}
		ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Totals: totals})
		if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
		recs := buf.records(t)
		if find(recs, "llm: no usage reported") == nil {
			t.Fatal("missing the 'no usage reported' record")
		}
		if got := totals.Snapshot(); got.Reported {
			t.Errorf("totals claim a report that never came: %+v", got)
		}
		if done := find(recs, "llm: request done"); done["chat_tokens_total"] != nil {
			t.Errorf("invented token fields: %v", done)
		}
	})
}

func TestComplete_rawUsageIsCapped(t *testing.T) {
	// A hostile or chatty endpoint must not be able to push an unbounded blob
	// into the log through the raw-usage line.
	padding := strings.Repeat("x", maxRawUsage*2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"note":"`+padding+`"}}`)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := find(buf.records(t), "llm: usage raw")["usage"].(string)
	if len(raw) > maxRawUsage+len("…(truncated)") {
		t.Fatalf("raw usage not capped: %d chars", len(raw))
	}
	if !strings.HasSuffix(raw, "(truncated)") {
		t.Errorf("truncation not marked: %q", raw[max(0, len(raw)-40):])
	}
}

func TestComplete_inferenceTimeExcludesPacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, _ := capture()
	const interval = 300 * time.Millisecond
	c := NewClient(Config{BaseURL: srv.URL, Logger: log, RequestInterval: interval}, srv.Client())

	totals := &Totals{}
	ctx := WithCall(context.Background(), CallInfo{VideoID: "vid1", Totals: totals})
	wall := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(wall)
	got := totals.Snapshot()
	if elapsed < interval {
		t.Fatalf("pacing did not happen: wall %s", elapsed)
	}
	// The whole point: the deliberate gap is booked as pacing, not as the
	// model being slow. A local httptest server answers in microseconds.
	if got.InferenceNanos >= int64(interval) {
		t.Errorf("inference %s swallowed the %s gap", time.Duration(got.InferenceNanos), interval)
	}
	// Slightly under the interval: pace() waits until the slot, and the first
	// call's own duration has already eaten part of the gap.
	if got.PacedNanos < int64(interval)/2 {
		t.Errorf("paced %s, want roughly %s", time.Duration(got.PacedNanos), interval)
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

func TestWithStage_ridesAlongToTheHeartbeatAndTheLogLines(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, usageBody)
	}))
	defer srv.Close()
	log, buf := capture()
	c := NewClient(Config{BaseURL: srv.URL, Logger: log, HeartbeatInterval: 10 * time.Millisecond}, srv.Client())

	// The worker sets step and stage together; a stall must say which stage of
	// which video is stuck, not just that something is slow.
	ctx := WithStage(WithStep(WithCall(context.Background(), CallInfo{VideoID: "vid1", Title: "A Title"}), "keypoints"), "4/4")
	go func() {
		time.Sleep(40 * time.Millisecond)
		close(release)
	}()
	if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	recs := buf.records(t)
	hb := find(recs, "llm: still waiting for response")
	if hb == nil {
		t.Fatal("no heartbeat record")
	}
	if hb["stage"] != "4/4" || hb["step"] != "keypoints" {
		t.Errorf("heartbeat = %v, want stage 4/4 keypoints", hb)
	}
	if done := find(recs, "llm: request done"); done["stage"] != "4/4" {
		t.Errorf("request done = %v, want stage 4/4", done)
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

func TestUsageLogAttrs_reportedUsageKeepsItsZeros(t *testing.T) {
	// The regression this exists for: an endpoint that reports
	// reasoning_tokens: 0 must SAY zero. Dropping the field made a reported
	// zero look exactly like an endpoint that reports nothing at all.
	attrs := Usage{Requests: 2, Reported: true, PromptTokens: 500}.LogAttrs()
	joined := strings.Join(keys(attrs), ",")
	for _, want := range []string{"chat_requests", "chat_tokens_in", "chat_tokens_cached", "chat_tokens_out", "chat_tokens_reasoning", "chat_tokens_total"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s: %v", want, attrs)
		}
	}
	if got := attrValue(attrs, "chat_tokens_reasoning"); got != "0" {
		t.Errorf("chat_tokens_reasoning = %v, want \"0\"", got)
	}
}

func TestUsageLogAttrs_unreportedUsageLogsNoTokenFields(t *testing.T) {
	// Nothing came back: printing zeros here would invent an answer the
	// endpoint never gave.
	attrs := Usage{Requests: 2, InferenceNanos: int64(3 * time.Second)}.LogAttrs()
	joined := strings.Join(keys(attrs), ",")
	if strings.Contains(joined, "chat_tokens") {
		t.Fatalf("token fields logged without a usage report: %v", attrs)
	}
	if !strings.Contains(joined, "chat_requests") || !strings.Contains(joined, "chat_inference_ms") {
		t.Fatalf("missing requests/inference: %v", attrs)
	}
}

func TestUsageLogAttrs_timings(t *testing.T) {
	attrs := Usage{Requests: 1, InferenceNanos: int64(2500 * time.Millisecond), PacedNanos: int64(10 * time.Second)}.LogAttrs()
	if got := attrValue(attrs, "chat_inference_ms"); got != int64(2500) {
		t.Errorf("chat_inference_ms = %v, want 2500", got)
	}
	if got := attrValue(attrs, "chat_paced_ms"); got != int64(10000) {
		t.Errorf("chat_paced_ms = %v, want 10000", got)
	}
}

// attrValue picks one value out of a flat slog key/value slice.
func attrValue(attrs []any, key string) any {
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == key {
			return attrs[i+1]
		}
	}
	return nil
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
