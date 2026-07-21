package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/taimport"
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
		b.WriteString("\nInactive channels imported (gone from YouTube).\n")
		b.WriteString("peeq's auto-unsubscribe will retire these on its own over the next few days:\n")
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

// importSubcommands maps a subcommand name to its entry point. Anything not
// listed here falls through to the normal server boot.
var importSubcommands = map[string]func([]string) error{
	"import-ta-channels": runImportChannels,
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
