package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// embedRecords runs one Embed against handler and returns the log records it
// produced, parsed from slog's JSON output.
func embedRecords(t *testing.T, handler http.HandlerFunc, inputs []string) ([]map[string]any, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "e5", Logger: log}, srv.Client())
	_, err := c.Embed(context.Background(), inputs)

	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if jerr := json.Unmarshal([]byte(line), &m); jerr != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		recs = append(recs, m)
	}
	return recs, err
}

func TestEmbed_logsUsageOnSuccess(t *testing.T) {
	recs, err := embedRecords(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": []float32{0.1}}},
			"usage": map[string]any{"prompt_tokens": 4200, "total_tokens": 4200},
		})
	}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0]["msg"] != "embed: request done" {
		t.Fatalf("records = %v", recs)
	}
	if recs[0]["embed_tokens_in"] != "4.2k" || recs[0]["inputs"] != float64(1) {
		t.Errorf("record = %v", recs[0])
	}
	if _, ok := recs[0]["duration_ms"]; !ok {
		t.Errorf("record has no duration_ms: %v", recs[0])
	}
}

func TestEmbed_logsEveryFailurePath(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		inputs  []string
		wantErr string
	}{
		{
			name:    "non-2xx",
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusBadGateway) },
			inputs:  []string{"a"},
			wantErr: "embedding failed with status 502",
		},
		{
			name:    "undecodable body",
			handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not json") },
			inputs:  []string{"a"},
			wantErr: "decode embed response",
		},
		{
			name: "count mismatch",
			handler: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{0.1}}}})
			},
			inputs:  []string{"a", "b"},
			wantErr: "embedding count mismatch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := embedRecords(t, tc.handler, tc.inputs)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if len(recs) != 1 || recs[0]["msg"] != "embed: request failed" || recs[0]["level"] != "WARN" {
				t.Fatalf("records = %v", recs)
			}
			if _, ok := recs[0]["duration_ms"]; !ok {
				t.Errorf("record has no duration_ms: %v", recs[0])
			}
		})
	}
}

func TestEmbed_emptyInputMakesNoRequestAndNoLog(t *testing.T) {
	recs, err := embedRecords(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected for empty input")
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("records = %v", recs)
	}
}
