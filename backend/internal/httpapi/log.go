package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/ship/kit/httpx"
)

// logAnswerFailed reports an Ask answer stream that ended in an error, and
// separates the one whose reader went away from the one that failed: the
// cancelled stream's error names the stream, not the closed tab behind it.
// took is how long the call ran — ninety seconds before a failure is the
// finding.
func logAnswerFailed(ctx context.Context, err error, took time.Duration) {
	d := took.Round(time.Millisecond).String()
	if ctx.Err() != nil {
		slog.Warn("answer cancelled", "cause", context.Cause(ctx).Error(), "err", err, "step", "answer", "took", d)
		return
	}
	slog.Warn("answer: chat failed", "err", err, "took", d)
}

// upstreamError answers a 502 for a failure of something peeq called on the
// client's behalf (yt-dlp, today). The client keeps getting the upstream's
// own text after clientPrefix — the UI surfaces it, and it is the only thing
// that tells the user WHY a channel would not resolve — and the log gets the
// same cause, so the failure is no longer visible only as an access line.
// Both copies are redacted, so the body can never say more than the log.
//
// ERROR, not WARN, although the upstream is the one that failed: the access
// line for a 502 is at ERROR, and an operator running at that level must
// still see the reason next to it.
//
// A client that went away before the upstream answered gets nothing and
// logs nothing above DEBUG: the cancellation is the client's, not a failure.
func upstreamError(w http.ResponseWriter, r *http.Request, err error, clientPrefix string) {
	if r.Context().Err() != nil {
		slog.Debug("upstream request abandoned by client", "method", r.Method, "path", r.URL.Path)
		return
	}
	redacted := logx.RedactErr(err)
	slog.Error("upstream request failed",
		"method", r.Method,
		"path", r.URL.Path,
		"client_message", clientPrefix,
		"err", redacted,
	)
	httpx.WriteError(w, http.StatusBadGateway, clientPrefix+": "+redacted.Error())
}
