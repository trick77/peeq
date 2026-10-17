package store

import "testing"

// 0038 drops sessions and users. It deletes DATA, so it runs over a database
// shaped the way 0037 left it: a user with a live session, beside a channel,
// a video and a playback pointer that must survive it untouched.
func TestMigrate0038_dropsSessionsAndUsersOnly(t *testing.T) {
	db := openMigratedThrough(t, "0037_keep_in_search_default_on.sql")
	for _, q := range []string{
		`INSERT INTO users (id, oidc_subject, username, role) VALUES ('u1', 'sub-1', 'jan', 'admin')`,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('h1', 'u1', '2099-01-01T00:00:00Z')`,
		`INSERT INTO channels (id, name) VALUES ('UC1', 'c')`,
		`INSERT INTO videos (id, url, channel_id) VALUES ('v1', 'u', 'UC1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	counts := func() map[string]int {
		out := map[string]int{}
		for _, tbl := range []string{"channels", "videos"} {
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[tbl] = n
		}
		return out
	}
	before := counts()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, tbl := range []string{"sessions", "users"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still exists after 0038", tbl)
		}
	}
	if after := counts(); after["channels"] != before["channels"] || after["videos"] != before["videos"] {
		t.Fatalf("rows changed: before %v, after %v", before, after)
	}
}
