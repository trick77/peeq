package channels

import "testing"

// fixedNow is the single reference instant every dormancy test seeds against.
// Using one fixed literal instead of the host clock (datetime('now', ...))
// keeps these tests hermetic: the relationship between "now" and each seeded
// discovered_at is a fixed, visible offset rather than something that only
// happens to line up on the day the suite was written.
const fixedNow = "2026-07-20 12:00:00"

// TestDormantChannels_flagsChannelQuietLongerThanThreshold and its sibling
// below are a matched pair: one discovered_at just outside DormantAfter,
// one just inside. An implementation with the comparison operator flipped
// would pass one and fail the other, never both — that's the point.
func TestDormantChannels_flagsChannelQuietLongerThanThreshold(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	// 7 months before fixedNow: outside the (5-7 month) DormantAfter window.
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v1', 'UC1', 'seen', '2025-12-20 12:00:00')`)

	got, err := st.DormantChannels(fixedNow)
	if err != nil {
		t.Fatalf("dormant channels: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != "UC1" {
		t.Fatalf("dormant channels = %+v, want [UC1] flagged", got)
	}
	if got[0].Name != "One" {
		t.Fatalf("dormant channel name = %q, want %q", got[0].Name, "One")
	}
	if got[0].LastVideoAt == "" {
		t.Fatalf("dormant channel LastVideoAt is empty, want populated")
	}
}

func TestDormantChannels_ignoresChannelJustInsideThreshold(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	// 5 months before fixedNow: inside the (5-7 month) DormantAfter window.
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v1', 'UC1', 'seen', '2026-02-20 12:00:00')`)

	got, err := st.DormantChannels(fixedNow)
	if err != nil {
		t.Fatalf("dormant channels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dormant channels = %+v, want none (video is inside threshold)", got)
	}
}

// TestDormantChannels_neverFlagsChannelWithNoVideos guards the "absent data
// is not evidence" rule: a brand-new subscription must not be flagged the
// instant it is created, before any scan has ever run.
func TestDormantChannels_neverFlagsChannelWithNoVideos(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	got, err := st.DormantChannels("2026-07-20 12:00:00")
	if err != nil {
		t.Fatalf("dormant channels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dormant channels = %+v, want none (no videos ever discovered)", got)
	}
}

func TestDormantChannels_ignoresUnsubscribedChannels(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	// Tracked only, never subscribed. 7 months before fixedNow: would be
	// flagged if the channel were subscribed, so this genuinely exercises
	// the "unsubscribed channels are ignored" rule rather than passing
	// vacuously because the video looks recent.
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v1', 'UC1', 'seen', '2025-12-20 12:00:00')`)

	got, err := st.DormantChannels(fixedNow)
	if err != nil {
		t.Fatalf("dormant channels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dormant channels = %+v, want none (channel is not subscribed)", got)
	}
}

func TestDismissDormant_suppressesTheFlag(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	// 7 months before fixedNow: outside the DormantAfter window.
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v1', 'UC1', 'seen', '2025-12-20 12:00:00')`)

	got, err := st.DormantChannels(fixedNow)
	if err != nil || len(got) != 1 {
		t.Fatalf("precondition: dormant channels = %+v err=%v, want UC1 flagged", got, err)
	}

	if ok, err := st.DismissDormant("UC1", fixedNow); err != nil || !ok {
		t.Fatalf("dismiss dormant: ok=%v err=%v", ok, err)
	}

	got, err = st.DormantChannels(fixedNow)
	if err != nil {
		t.Fatalf("dormant channels after dismiss: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dormant channels after dismiss = %+v, want none (dismissal suppresses the flag)", got)
	}
}

// TestDismissDormant_reArmsAfterNewerDiscovery is the "dismissal is not
// permanent" guarantee: if the channel posts again and then goes quiet a
// second time, the flag must return. A dismissal that suppressed forever
// would hide a channel that genuinely went dormant twice.
func TestDismissDormant_reArmsAfterNewerDiscovery(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	// An older, otherwise-irrelevant discovery well before the dismissal;
	// it is superseded by v2 below and only needs a fixed, deterministic
	// value so the seeded data never drifts with the host clock.
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v1', 'UC1', 'seen', '2025-12-20 12:00:00')`)

	dismissedAt := fixedNow
	if ok, err := st.DismissDormant("UC1", dismissedAt); err != nil || !ok {
		t.Fatalf("dismiss dormant: ok=%v err=%v", ok, err)
	}

	// A newer discovery, after the dismissal, but the channel then goes
	// quiet again for longer than DormantAfter relative to "now".
	mustExec(t, db, `
INSERT INTO channel_videos (video_id, channel_id, state, discovered_at)
VALUES ('v2', 'UC1', 'seen', datetime(?, '+1 second'))`, dismissedAt)

	// 7 months after fixedNow (dismissedAt) and thus >6 months after v2's
	// discovered_at, which sits ~1 second after dismissedAt.
	now := "2027-02-20 12:00:00"
	got, err := st.DormantChannels(now)
	if err != nil {
		t.Fatalf("dormant channels: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != "UC1" {
		t.Fatalf("dormant channels = %+v, want UC1 re-flagged after newer discovery", got)
	}
}

// TestDismissDormant_unknownChannel_reportsFalse asserts DismissDormant
// reports ok=false for a channel with no subscription row (unknown, or
// tracked-but-unsubscribed), so callers can distinguish that from a real
// dismissal instead of treating a zero-row UPDATE as success.
func TestDismissDormant_unknownChannel_reportsFalse(t *testing.T) {
	st := newTestStore(t)
	ok, err := st.DismissDormant("nope", fixedNow)
	if err != nil {
		t.Fatalf("dismiss dormant: %v", err)
	}
	if ok {
		t.Fatalf("dismiss dormant unknown channel ok = true, want false")
	}
}

func TestRecordDeadScan_incrementsAndReturnsCount(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	for i, want := range []int{1, 2, 3} {
		got, err := st.RecordDeadScan("UC1")
		if err != nil {
			t.Fatalf("record dead scan #%d: %v", i+1, err)
		}
		if got != want {
			t.Fatalf("record dead scan #%d = %d, want %d", i+1, got, want)
		}
	}
}

func TestResetDeadScan_zeroesPartialCount(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.RecordDeadScan("UC1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordDeadScan("UC1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetDeadScan("UC1"); err != nil {
		t.Fatalf("reset dead scan: %v", err)
	}
	got, err := st.RecordDeadScan("UC1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("dead scan count after reset+one scan = %d, want 1 (must not creep to 3)", got)
	}
}

// TestAutoUnsubscribe_secondDeathAfterManualResubscribe_updatesRecord pins
// the ON CONFLICT DO UPDATE branch (Task 1 review): a channel that is
// manually re-subscribed via plain Subscribe (which, unlike the HTTP
// resubscribe handler, does NOT clear the prior auto-unsubscribe record)
// and then dies a second time must have its record UPDATED with the new
// reason/timestamp, not abort the transaction on the channel_id PRIMARY KEY
// conflict and leave the channel silently subscribed with a stale record.
// Removing the ON CONFLICT clause makes this test fail on the second
// AutoUnsubscribe call with a PK constraint error.
func TestAutoUnsubscribe_secondDeathAfterManualResubscribe_updatesRecord(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	const firstDeath = "2026-07-20 12:00:00"
	if err := st.AutoUnsubscribe("UC1", ReasonDeleted, firstDeath); err != nil {
		t.Fatalf("first auto unsubscribe: %v", err)
	}

	// Manual re-subscribe, deliberately NOT clearing the auto-unsubscribe
	// record — this is the case the HTTP resubscribe handler's Clear-then-
	// Subscribe ordering is designed to avoid, but Subscribe itself must
	// still tolerate it (a plain POST /api/channels/{id}/subscribe reaches
	// this same store method).
	if err := st.Subscribe("UC1", "2026-07-21 00:00:00"); err != nil {
		t.Fatalf("manual resubscribe: %v", err)
	}

	const secondDeath = "2026-08-20 12:00:00"
	if err := st.AutoUnsubscribe("UC1", ReasonDeleted, secondDeath); err != nil {
		t.Fatalf("second auto unsubscribe (must not abort on PK conflict): %v", err)
	}

	list, err := st.AutoUnsubscribedList()
	if err != nil {
		t.Fatalf("auto unsubscribed list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "UC1" {
		t.Fatalf("auto unsubscribed list = %+v, want exactly UC1", list)
	}
	if list[0].At != secondDeath {
		t.Fatalf("auto unsubscribed at = %q, want %q (the second death, not the first)", list[0].At, secondDeath)
	}
}

func TestAutoUnsubscribe_removesSubscriptionAndRecordsReason(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	const at = "2026-07-20 12:00:00"
	if err := st.AutoUnsubscribe("UC1", ReasonDeleted, at); err != nil {
		t.Fatalf("auto unsubscribe: %v", err)
	}

	items, err := st.List("all")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Subscribed {
		t.Fatalf("list after auto unsubscribe = %+v, want one tracked-but-unsubscribed item", items)
	}

	list, err := st.AutoUnsubscribedList()
	if err != nil {
		t.Fatalf("auto unsubscribed list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("auto unsubscribed list len = %d, want 1", len(list))
	}
	if list[0].ID != "UC1" {
		t.Fatalf("auto unsubscribed channel id = %q, want UC1", list[0].ID)
	}
	if list[0].Reason != ReasonDeleted {
		t.Fatalf("auto unsubscribed reason = %q, want %q", list[0].Reason, ReasonDeleted)
	}
	if list[0].At != at {
		t.Fatalf("auto unsubscribed at = %q, want %q", list[0].At, at)
	}
}

// TestAutoUnsubscribe_keepsChannelAndVideos is the "nothing is destroyed"
// promise the UI makes to the user: auto-unsubscribing removes only the
// subscriptions row, never the channel, its ledger rows, or its downloaded
// videos. Asserted against the real schema, not an assumption about how the
// FK cascades behave.
func TestAutoUnsubscribe_keepsChannelAndVideos(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO videos (id,url,channel_id,status,media_path) VALUES ('v1','u','UC1','downloaded','/m/v1.mp4')`)
	mustExec(t, db, `INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('v1','UC1','queued')`)

	if err := st.AutoUnsubscribe("UC1", ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatalf("auto unsubscribe: %v", err)
	}

	for _, q := range []string{
		`SELECT count(*) FROM channels WHERE id='UC1'`,
		`SELECT count(*) FROM channel_videos WHERE channel_id='UC1'`,
		`SELECT count(*) FROM videos WHERE channel_id='UC1'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%q -> n=%d err=%v, want 1 (nothing destroyed)", q, n, err)
		}
	}
	var subs int
	if err := db.QueryRow(`SELECT count(*) FROM subscriptions WHERE channel_id='UC1'`).Scan(&subs); err != nil || subs != 0 {
		t.Fatalf("subscriptions after auto unsubscribe = %d err=%v, want 0", subs, err)
	}
}

func TestClearAutoUnsubscribe_allowsResubscribe(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.AutoUnsubscribe("UC1", ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatal(err)
	}

	if err := st.ClearAutoUnsubscribe("UC1"); err != nil {
		t.Fatalf("clear auto unsubscribe: %v", err)
	}
	if err := st.Subscribe("UC1", "2026-07-21 00:00:00"); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}

	items, err := st.List("subscribed")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "UC1" {
		t.Fatalf("subscribed list = %+v, want UC1 subscribed again", items)
	}

	list, err := st.AutoUnsubscribedList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("auto unsubscribed list = %+v, want empty after clear", list)
	}
}

// TestAutoUnsubscribeReasons_allAcceptedByDBCheck locks the Go reason enum to
// the SQL CHECK constraint. peeq shipped a production bug where a Go enum
// silently drifted from a SQL CHECK and broke every video add — this test
// exists to prevent a repeat for auto_unsubscribes.reason.
func TestAutoUnsubscribeReasons_allAcceptedByDBCheck(t *testing.T) {
	st := newTestStore(t)
	reasons := []string{ReasonDeleted}
	for i, reason := range reasons {
		id := "UC" + string(rune('1'+i))
		if err := st.Upsert(Channel{ID: id, Name: id}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		if err := st.Subscribe(id, "2000-01-01 00:00:00"); err != nil {
			t.Fatalf("subscribe %s: %v", id, err)
		}
		if err := st.AutoUnsubscribe(id, reason, "2026-07-20 12:00:00"); err != nil {
			t.Fatalf("auto unsubscribe %s with reason %q rejected by DB CHECK: %v", id, reason, err)
		}
	}
}
