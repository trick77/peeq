package channelvideos

import "testing"

// seedPending inserts one pending ledger row for a freshly seeded channel.
func seedPending(t *testing.T, st *Store, videoID string) {
	t.Helper()
	seedChannel(t, st, "UC1")
	if err := st.Insert(Entry{VideoID: videoID, ChannelID: "UC1", Title: "T", State: "pending"}); err != nil {
		t.Fatalf("insert %s: %v", videoID, err)
	}
}

func TestLedger_setUnavailableRecordsReasonAndStamp(t *testing.T) {
	st := newTestStore(t)
	seedPending(t, st, "v1")

	if err := st.SetUnavailable("v1", "members"); err != nil {
		t.Fatalf("set unavailable: %v", err)
	}

	got, err := st.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateUnavailable {
		t.Fatalf("state = %q, want %q", got.State, StateUnavailable)
	}
	if got.UnavailableReason != "members" {
		t.Fatalf("reason = %q, want members", got.UnavailableReason)
	}
	if got.UnavailableAt == "" {
		t.Fatal("unavailable_at must be stamped")
	}
	// An unavailable row is not a decision the user made, so it must not
	// appear as one — and it must not be offered in the inbox.
	if got.DecidedAt != "" {
		t.Fatalf("decided_at = %q, want empty (peeq decided this, not the user)", got.DecidedAt)
	}
	pending, _ := st.ListPending()
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}

// Leaving a stale reason behind would make the UI label an ordinary download
// "members only" long after the gate lifted.
func TestLedger_setStateClearsTheUnavailableFields(t *testing.T) {
	st := newTestStore(t)
	seedPending(t, st, "v1")
	if err := st.SetUnavailable("v1", "members"); err != nil {
		t.Fatal(err)
	}

	if err := st.SetState("v1", "pending"); err != nil {
		t.Fatal(err)
	}

	got, _ := st.Get("v1")
	if got.UnavailableReason != "" || got.UnavailableAt != "" {
		t.Fatalf("revived row still carries %q/%q", got.UnavailableReason, got.UnavailableAt)
	}
	if got.State != "pending" {
		t.Fatalf("state = %q, want pending", got.State)
	}
}

// Restamping on every park is what keeps the timed re-offer self-limiting: a
// video that came back, was clicked, and failed again waits a fresh window
// instead of qualifying on the next pass.
func TestLedger_setUnavailableRestampsAnAlreadyParkedRow(t *testing.T) {
	st := newTestStore(t)
	seedPending(t, st, "v1")
	if err := st.SetUnavailable("v1", "members"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`UPDATE channel_videos SET unavailable_at = '2000-01-01 00:00:00' WHERE video_id = 'v1'`); err != nil {
		t.Fatal(err)
	}

	if err := st.SetUnavailable("v1", "members"); err != nil {
		t.Fatal(err)
	}

	got, _ := st.Get("v1")
	if got.UnavailableAt == "2000-01-01 00:00:00" {
		t.Fatal("unavailable_at must be restamped on a repeat park")
	}
}

// A row born unavailable (the scan saw the gate badge before the video ever
// reached the inbox) starts its clock the same way one parked later does.
func TestLedger_insertUnavailableStampsOnTheWayIn(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UC1")

	if err := st.Insert(Entry{
		VideoID: "v1", ChannelID: "UC1", Title: "T",
		State: StateUnavailable, UnavailableReason: "members",
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := st.Get("v1")
	if got.UnavailableAt == "" {
		t.Fatal("unavailable_at must be stamped by Insert")
	}
	if got.UnavailableReason != "members" {
		t.Fatalf("reason = %q, want members", got.UnavailableReason)
	}
}

// Every other state leaves the stamp null, so "is this parked?" has exactly
// one answer.
func TestLedger_insertOrdinaryStateLeavesNoStamp(t *testing.T) {
	st := newTestStore(t)
	seedPending(t, st, "v1")

	got, _ := st.Get("v1")
	if got.UnavailableAt != "" || got.UnavailableReason != "" {
		t.Fatalf("pending row carries %q/%q", got.UnavailableReason, got.UnavailableAt)
	}
}
