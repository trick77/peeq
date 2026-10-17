package videos

import (
	"context"
	"testing"
)

// The *Ctx readers are what the MCP tools call under their deadline: a done
// context must stop them rather than be ignored.
func TestCtxReadersHonourCancellation(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "t", ChannelID: "UC1", ChannelName: "C"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetCtx(ctx, "v1"); err == nil {
		t.Error("GetCtx ignored a cancelled context")
	}
	if _, err := s.GetManyCtx(ctx, []string{"v1"}); err == nil {
		t.Error("GetManyCtx ignored a cancelled context")
	}
	if _, err := s.InDepthCtx(ctx, "v1"); err == nil {
		t.Error("InDepthCtx ignored a cancelled context")
	}
	if _, err := s.GetTranscriptCtx(ctx, "v1"); err == nil {
		t.Error("GetTranscriptCtx ignored a cancelled context")
	}
	if _, err := s.ChannelDirectoryCtx(ctx); err == nil {
		t.Error("ChannelDirectoryCtx ignored a cancelled context")
	}
	if _, err := s.ChannelCategoryStats(ctx); err == nil {
		t.Error("ChannelCategoryStats ignored a cancelled context")
	}
	// And the live context still reads.
	if v, err := s.GetCtx(context.Background(), "v1"); err != nil || v == nil {
		t.Fatalf("GetCtx = %v, %v", v, err)
	}
}
