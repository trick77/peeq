package store

import (
	"path/filepath"
	"testing"
)

func TestMigrate_createsCoreTables(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"settings", "videos", "download_jobs", "schema_migrations"} {
		var n int
		if err := db.QueryRow("select count(*) from sqlite_master where type='table' and name=?", tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}

	var preset string
	var retentionDays, throttleBaseSeconds, minFreeGB int
	var cookieStatus string
	if err := db.QueryRow(
		"select format_preset, retention_days, throttle_base_seconds, min_free_gb, cookie_status from settings where id = 1",
	).Scan(&preset, &retentionDays, &throttleBaseSeconds, &minFreeGB, &cookieStatus); err != nil {
		t.Fatalf("settings seed row missing: %v", err)
	}
	if preset != "apple-1080p" || retentionDays != 14 || throttleBaseSeconds != 10 || minFreeGB != 5 || cookieStatus != "absent" {
		t.Fatalf("unexpected settings defaults: preset=%s retentionDays=%d throttleBaseSeconds=%d minFreeGB=%d cookieStatus=%s",
			preset, retentionDays, throttleBaseSeconds, minFreeGB, cookieStatus)
	}
}

func TestMigrate_createsPhase2Tables(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"channels", "subscriptions", "channel_videos", "users", "sessions"} {
		var n int
		if err := db.QueryRow(
			"select count(*) from sqlite_master where type='table' and name=?", tbl,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
}

func TestMigrate_idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db1); err != nil {
		t.Fatal(err)
	}
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := Migrate(db2); err != nil {
		t.Fatalf("second migrate should be a no-op, got error: %v", err)
	}
}
