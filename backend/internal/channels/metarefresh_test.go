package channels

import (
	"testing"
)

// seedSubscribed tracks and subscribes channelID, then sets its metadata
// schedule explicitly. Subscribe seeds next_meta_refresh_at a week out on its
// own, which is right for production and useless for a test that wants to say
// "this one is due".
func seedSubscribed(t *testing.T, s *Store, channelID, nextMetaRefreshAt string) {
	t.Helper()
	if err := s.Upsert(Channel{ID: channelID, Name: channelID}); err != nil {
		t.Fatalf("upsert %s: %v", channelID, err)
	}
	if err := s.Track(channelID, "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("track %s: %v", channelID, err)
	}
	if err := s.Subscribe(channelID, "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("subscribe %s: %v", channelID, err)
	}
	if err := s.MarkMetaRefreshed(channelID, nextMetaRefreshAt); err != nil {
		t.Fatalf("schedule %s: %v", channelID, err)
	}
}

// setScan writes a subscription's scan columns directly. The scan scheduler
// owns MarkScanned in production; here the point is only to place a scan at a
// chosen distance from now.
func setScan(t *testing.T, s *Store, channelID, lastScannedAt, nextScanAt string) {
	t.Helper()
	_, err := s.DB().Exec(
		`UPDATE subscriptions SET last_scanned_at = ?, next_scan_at = ? WHERE channel_id = ?`,
		lastScannedAt, nextScanAt, channelID)
	if err != nil {
		t.Fatalf("set scan %s: %v", channelID, err)
	}
}

const metaNow = "2026-07-22 12:00:00"

func TestClaimDueMetadata_picksTheMostOverdue(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCfresh", "2026-07-29 12:00:00") // not due
	seedSubscribed(t, s, "UCdue", "2026-07-22 11:00:00")   // due, 1h late
	seedSubscribed(t, s, "UCstale", "2026-07-20 12:00:00") // due, 2d late
	setScan(t, s, "UCdue", "", "2026-07-23 12:00:00")
	setScan(t, s, "UCstale", "", "2026-07-23 12:00:00")

	got, err := s.ClaimDueMetadata(metaNow)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got == nil || got.ID != "UCstale" {
		t.Fatalf("claimed = %+v; want UCstale", got)
	}
}

func TestClaimDueMetadata_nothingDue(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCfresh", "2026-07-29 12:00:00")
	setScan(t, s, "UCfresh", "", "2026-07-23 12:00:00")

	got, err := s.ClaimDueMetadata(metaNow)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got != nil {
		t.Fatalf("claimed %q from an empty due set", got.ID)
	}
}

// TestClaimDueMetadata_keepsAwayFromScans is the anti-hammering rule: a
// metadata refresh and a video scan are two yt-dlp calls against the same
// channel, and firing them minutes apart is exactly the traffic shape that
// gets an account throttled. A channel inside the window is passed over —
// note it stays DUE, so it is picked up on a later pass rather than lost.
func TestClaimDueMetadata_keepsAwayFromScans(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		lastScannedAt, nextScanAt string
		wantClaimed               bool
	}{
		{"scan minutes ago", "2026-07-22 11:50:00", "2026-07-23 11:50:00", false},
		{"scan due in minutes", "2026-07-21 11:50:00", "2026-07-22 12:10:00", false},
		{"scan hours away on both sides", "2026-07-22 09:00:00", "2026-07-23 09:00:00", true},
		// A backed-off subscription carries a next_scan_at in the PAST. Reading
		// that as "a scan is imminent" would park the metadata refresh forever
		// for exactly the channels most likely to need re-reading.
		{"overdue scan does not count as imminent", "2026-07-22 09:00:00", "2026-07-22 08:00:00", true},
		// Never scanned at all: nothing to collide with.
		{"never scanned", "", "2026-07-23 09:00:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedSubscribed(t, s, "UCa", "2026-07-20 12:00:00")
			setScan(t, s, "UCa", tc.lastScannedAt, tc.nextScanAt)

			got, err := s.ClaimDueMetadata(metaNow)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if (got != nil) != tc.wantClaimed {
				t.Fatalf("claimed = %+v; want claimed=%v", got, tc.wantClaimed)
			}
		})
	}
}

// TestClaimUnresolved_onlyTrackedAndNeverRead covers the never-read backlog:
// it is what fills in a channel the user just tracked (or one of the hundreds
// a TubeArchivist import created) without waiting for someone to open its
// page — and it must not become a retry loop for channels that already had
// their attempt.
func TestClaimUnresolved_onlyTrackedAndNeverRead(t *testing.T) {
	s := newTestStore(t)
	// Cache-only: peeq glanced at it, the user never asked for it.
	if err := s.Upsert(Channel{ID: "UCcache", Name: "Cache"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Tracked and already read.
	if err := s.Upsert(Channel{ID: "UCread", Name: "Read", ResolvedAt: "2026-07-01 00:00:00"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCread", "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	// Tracked, never read: the one we want.
	if err := s.Upsert(Channel{ID: "UCnew", Name: "New"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCnew", "2026-02-01 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := s.ClaimUnresolved(metaNow)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got == nil || got.ID != "UCnew" {
		t.Fatalf("claimed = %+v; want UCnew", got)
	}

	// A FAILED attempt still stamps resolved_at, and that must be enough to
	// take the channel out of the backlog: re-reading failures forever is the
	// loop the resolved_at rule exists to prevent.
	if err := s.MarkResolveAttempted("UCnew", "2026-07-22 12:00:00"); err != nil {
		t.Fatalf("mark attempted: %v", err)
	}
	if got, err := s.ClaimUnresolved(metaNow); err != nil || got != nil {
		t.Fatalf("backlog returned a channel after a failed attempt (got=%+v, err=%v)", got, err)
	}
}

// TestClaimUnresolved_keepsAwayFromScans asserts the backlog honours the same
// quiet window. Most backlog channels are unsubscribed and have no scan at
// all — the LEFT JOIN is what keeps those claimable.
func TestClaimUnresolved_keepsAwayFromScans(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCsub"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCsub", "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := s.Subscribe("UCsub", "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	setScan(t, s, "UCsub", "2026-07-22 11:50:00", "2026-07-23 11:50:00")

	if got, err := s.ClaimUnresolved(metaNow); err != nil || got != nil {
		t.Fatalf("claimed a channel scanned ten minutes ago (got=%+v, err=%v)", got, err)
	}

	// An unsubscribed tracked channel has no subscriptions row and so nothing
	// to collide with.
	if err := s.Upsert(Channel{ID: "UCloose"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCloose", "2026-01-02 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	got, err := s.ClaimUnresolved(metaNow)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got == nil || got.ID != "UCloose" {
		t.Fatalf("claimed = %+v; want UCloose", got)
	}
}

func TestMarkMetaRefreshed_advancesTheSchedule(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCa", "2026-07-20 12:00:00")
	setScan(t, s, "UCa", "", "2026-07-23 12:00:00")

	if err := s.MarkMetaRefreshed("UCa", "2026-07-29 12:00:00"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got, err := s.ClaimDueMetadata(metaNow); err != nil || got != nil {
		t.Fatalf("channel still due after rescheduling (got=%+v, err=%v)", got, err)
	}
}

// TestMarkMetaRefreshed_unsubscribedIsANoOp: a backlog channel that is merely
// tracked has no rotation to be scheduled into, and saying so must not be an
// error the worker logs on every pass.
func TestMarkMetaRefreshed_unsubscribedIsANoOp(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCloose"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.MarkMetaRefreshed("UCloose", "2026-07-29 12:00:00"); err != nil {
		t.Fatalf("mark: %v", err)
	}
}

// TestSubscribe_seedsTheMetadataSchedule asserts a newly subscribed channel
// joins the rotation instead of sitting at NULL forever — a NULL would never
// match ClaimDueMetadata's predicate and the channel would never refresh.
func TestSubscribe_seedsTheMetadataSchedule(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCa"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Subscribe("UCa", "2026-07-22 12:00:00"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var next string
	err := s.DB().QueryRow(
		`SELECT COALESCE(next_meta_refresh_at, '') FROM subscriptions WHERE channel_id = ?`, "UCa").Scan(&next)
	if err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if next == "" {
		t.Fatal("subscribing left next_meta_refresh_at NULL; the channel would never refresh")
	}
	// Roughly a week out: not due now, and not so far out it never comes.
	if got, _ := s.ClaimDueMetadata(metaNow); got != nil {
		t.Fatal("a freshly subscribed channel is immediately due for a metadata refresh")
	}
}

// TestMarkResolveAttemptedIfUnset covers the worker's loop-breaker backstop:
// it must stamp a never-read channel (taking it out of the backlog) and must
// NOT touch one that already carries an outcome.
func TestMarkResolveAttemptedIfUnset(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCnever"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Upsert(Channel{ID: "UCdone", Name: "Read", ResolvedAt: "2026-07-01 00:00:00"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// SaveResolved is what a successful read writes; resolve_ok must survive.
	if err := s.SaveResolved(Channel{ID: "UCdone", Name: "Read", ResolvedAt: "2026-07-01 00:00:00"}); err != nil {
		t.Fatalf("save resolved: %v", err)
	}

	for _, id := range []string{"UCnever", "UCdone"} {
		if err := s.MarkResolveAttemptedIfUnset(id, "2026-07-22 12:00:00"); err != nil {
			t.Fatalf("mark %s: %v", id, err)
		}
	}

	never, err := s.Get("UCnever")
	if err != nil || never == nil {
		t.Fatalf("get: %v, %v", never, err)
	}
	if never.ResolvedAt != "2026-07-22 12:00:00" {
		t.Fatalf("never-read channel was not stamped: %q", never.ResolvedAt)
	}

	done, err := s.Get("UCdone")
	if err != nil || done == nil {
		t.Fatalf("get: %v, %v", done, err)
	}
	if done.ResolvedAt != "2026-07-01 00:00:00" {
		t.Fatalf("an existing outcome was overwritten: %q", done.ResolvedAt)
	}
	if !done.ResolveOk {
		t.Fatal("a successful resolve lost resolve_ok")
	}
}
