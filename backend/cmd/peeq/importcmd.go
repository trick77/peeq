package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/peeq/internal/download"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/taimport"
	"github.com/trick77/peeq/internal/videos"
)

// runImportChannels implements `peeq import-ta-channels`: a one-shot migration
// that copies TubeArchivist's subscriptions into peeq.
//
// It opens the database directly, so the peeq server must be stopped while it
// runs. The DB path comes from config (BACKEND_DB_PATH), not a flag, so it
// always targets the same database the server uses.
func runImportChannels(args []string) error {
	fs := flag.NewFlagSet("import-ta-channels", flag.ContinueOnError)
	var (
		taURL   = fs.String("ta-url", "", "TubeArchivist base URL, e.g. http://tubearchivist:8000")
		taToken = fs.String("ta-token", "", "TubeArchivist API token (TA settings UI, or GET /api/appsettings/token/)")
		dryRun  = fs.Bool("dry-run", false, "report what would be imported without writing anything")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// flag already printed usage; --help is not a failure.
			return nil
		}
		return err
	}
	if *taURL == "" {
		return fmt.Errorf("--ta-url is required")
	}
	if *taToken == "" {
		return fmt.Errorf("--ta-token is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		return err
	}

	client := taimport.NewClient(*taURL, *taToken, nil)
	chStore := channels.New(db)

	res, err := taimport.ImportChannels(context.Background(), client, chStore, *dryRun, time.Now())
	if err != nil {
		return err
	}

	fmt.Println(formatChannelResult(res, *dryRun))
	return nil
}

// formatChannelResult renders an import summary for the terminal.
func formatChannelResult(res taimport.ChannelResult, dryRun bool) string {
	var b strings.Builder

	if dryRun {
		b.WriteString("DRY RUN — nothing was written.\n\n")
	}
	fmt.Fprintf(&b, "Subscriptions:  %d\n", res.Subscribed)
	fmt.Fprintf(&b, "  active:       %d\n", res.Active)
	fmt.Fprintf(&b, "  inactive:     %d\n", res.Inactive)
	fmt.Fprintf(&b, "Skipped:        %d  (channels TubeArchivist knows but was never subscribed to)\n", res.Skipped)

	if len(res.InactiveNames) > 0 {
		b.WriteString("\nTubeArchivist marked these inactive — its last refresh could not\n")
		b.WriteString("fetch the channel, which usually means a transient failure rather\n")
		b.WriteString("than a deleted channel. peeq imported them normally and re-checks\n")
		b.WriteString("each against YouTube on its own scan; only genuinely dead ones are\n")
		b.WriteString("unsubscribed:\n")
		for _, n := range res.InactiveNames {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}

	if !dryRun {
		b.WriteString("\nAll subscriptions have autodownload OFF. peeq's first scan of each\n")
		b.WriteString("channel baselines it, so the pending queue will not fill with back\n")
		b.WriteString("catalogue. Turn autodownload on per channel when you want it.\n")
	}

	return b.String()
}

// runImportVideos implements `peeq import-ta`: a one-shot migration of the
// unwatched video queue and its media files from TubeArchivist into peeq. Like
// import-ta-channels it opens the database directly, so the peeq server must be
// stopped while it runs. It reads files from TubeArchivist's two read-only
// volumes (media + cache) mounted via --ta-media and --ta-cache.
func runImportVideos(args []string) error {
	fs := flag.NewFlagSet("import-ta", flag.ContinueOnError)
	var (
		taURL    = fs.String("ta-url", "", "TubeArchivist base URL, e.g. http://tubearchivist:8000")
		taToken  = fs.String("ta-token", "", "TubeArchivist API token (must be the user whose queue is migrating)")
		taMedia  = fs.String("ta-media", "", "read-only mount of TubeArchivist's media volume")
		taCache  = fs.String("ta-cache", "", "read-only mount of TubeArchivist's cache volume (thumbnails)")
		dryRun   = fs.Bool("dry-run", false, "report what would be imported and its size without writing or copying anything")
		maxChans = fs.Int("channels", 0, "import only the first N channels (0 = all)")
		types    = fs.String("types", "", "comma-separated vid_type allowlist (videos,shorts,streams); empty = all")
	)
	var onlyChannels []string
	fs.Func("channel", "import only this channel id; repeatable", func(v string) error {
		onlyChannels = append(onlyChannels, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch {
	case *taURL == "":
		return fmt.Errorf("--ta-url is required")
	case *taToken == "":
		return fmt.Errorf("--ta-token is required")
	case *taMedia == "":
		return fmt.Errorf("--ta-media is required (read-only mount of TubeArchivist's media volume)")
	case *taCache == "":
		return fmt.Errorf("--ta-cache is required (read-only mount of TubeArchivist's cache volume)")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		return err
	}

	ctx := context.Background()
	client := taimport.NewClient(*taURL, *taToken, nil)

	// Shard the video crawl over the subscribed channels.
	chans, err := client.AllChannels(ctx)
	if err != nil {
		return err
	}
	ids := selectChannelIDs(chans, onlyChannels, *maxChans)

	writer := taimport.NewStoreWriter(videos.New(db), summaryjobs.New(db))
	opts := taimport.ImportOptions{
		Paths: taimport.PathMapper{TAMediaRoot: *taMedia, TACacheRoot: *taCache, PeeqMediaDir: cfg.MediaDir},
		Types: parseTypes(*types),
		CheckSpace: func(needed int64) error {
			free, err := download.FreeBytes(cfg.MediaDir)
			if err != nil {
				return fmt.Errorf("free-space check on %s: %w", cfg.MediaDir, err)
			}
			const headroom = int64(1) << 30 // 1 GiB
			if int64(free) < needed+headroom {
				return fmt.Errorf("not enough free space in %s: need ~%d bytes plus headroom, have %d — refusing to fill the disk", cfg.MediaDir, needed, free)
			}
			return nil
		},
	}

	res, err := taimport.ImportVideos(ctx, client, writer, ids, opts, *dryRun)
	if err != nil {
		return err
	}
	fmt.Println(formatVideoResult(res, *dryRun))
	return nil
}

// selectChannelIDs applies the --channel (allowlist) and --channels (first N)
// flags to the resolved channel list and returns the ids to crawl.
func selectChannelIDs(chans []taimport.Channel, only []string, maxN int) []string {
	if len(only) > 0 {
		set := make(map[string]bool, len(only))
		for _, c := range only {
			set[c] = true
		}
		kept := chans[:0:0]
		for _, c := range chans {
			if set[c.ID] {
				kept = append(kept, c)
			}
		}
		chans = kept
	}
	if maxN > 0 && len(chans) > maxN {
		chans = chans[:maxN]
	}
	ids := make([]string, len(chans))
	for i, c := range chans {
		ids[i] = c.ID
	}
	return ids
}

// parseTypes splits the comma-separated --types flag; empty yields nil (all).
func parseTypes(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// formatVideoResult renders an import-ta summary for the terminal.
func formatVideoResult(res taimport.VideoResult, dryRun bool) string {
	var b strings.Builder
	if dryRun {
		b.WriteString("DRY RUN — nothing was written or copied.\n\n")
		fmt.Fprintf(&b, "Would import:   %d videos (%s)\n", res.Planned, humanBytes(res.BytesMedia))
	} else {
		fmt.Fprintf(&b, "Imported:       %d videos (%s copied)\n", res.Imported, humanBytes(res.BytesMedia))
	}
	fmt.Fprintf(&b, "Skipped:        %d already imported\n", res.SkippedDownloaded)
	if res.SkippedType > 0 {
		fmt.Fprintf(&b, "                %d excluded by --types\n", res.SkippedType)
	}
	if res.MissingFile > 0 {
		fmt.Fprintf(&b, "Missing files:  %d videos have metadata but no .mp4 on the TA mount (skipped)\n", res.MissingFile)
	}
	if res.ResumeUnavailable > 0 {
		fmt.Fprintf(&b, "\nHeads-up: %d partially-watched videos were imported WITHOUT a resume\n", res.ResumeUnavailable)
		b.WriteString("position (TubeArchivist did not report one). Check them while TA is still around.\n")
	}
	return b.String()
}

// humanBytes renders a byte count as B/KiB/MiB/GiB… for the summary.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// importSubcommands maps a subcommand name to its entry point. Anything not
// listed here falls through to the normal server boot.
var importSubcommands = map[string]func([]string) error{
	"import-ta-channels": runImportChannels,
	"import-ta":          runImportVideos,
}

// dispatchSubcommand runs a subcommand if args names one, reporting whether it
// handled the invocation and what the subcommand returned. args is the full
// argv including the program name at index 0.
//
// Taking args as a parameter rather than reading os.Args, and returning the
// error rather than exiting, is what makes the fall-through behaviour testable
// — and that behaviour is load-bearing: the container starts the server with no
// arguments, so an unrecognised or absent argument MUST fall through to the
// normal server boot.
func dispatchSubcommand(args []string) (handled bool, err error) {
	if len(args) < 2 {
		return false, nil
	}
	fn, ok := importSubcommands[args[1]]
	if !ok {
		return false, nil
	}
	return true, fn(args[2:])
}
