package summarize

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/llm"
)

const (
	// subcategoryIdleHold is how long the sweeps rest after finding nothing to
	// do. Their candidate queries scan every video's index entry; at the
	// worker's idle poll that would be a scan every two seconds for nothing.
	// New work is rare and nobody waits on it.
	subcategoryIdleHold = 5 * time.Minute
	// channelTitlesForSubcategories is how many recent titles the channel
	// classifier reads beside the description.
	channelTitlesForSubcategories = 25
)

// subcategorizeOne spends an idle turn on the subcategory backlog: one video,
// else one channel. Like classifyOne it reports did=true only when it made an
// LLM call, logs its failures rather than returning them (there is no job to
// fail), and parks a failed id for the life of the process.
func (w *Worker) subcategorizeOne(ctx context.Context) (bool, error) {
	if w.d.Now().Before(w.subcatIdleUntil) {
		return false, nil
	}
	if did, err := w.subcategorizeVideoOne(ctx); did || err != nil {
		return did, err
	}
	if did, err := w.subcategorizeChannelOne(ctx); did || err != nil {
		return did, err
	}
	w.subcatIdleUntil = w.d.Now().Add(subcategoryIdleHold)
	return false, nil
}

func (w *Worker) subcategorizeVideoOne(ctx context.Context) (bool, error) {
	video, err := w.d.Videos.NextUnsubcategorized(ctx, slices.Collect(maps.Keys(w.subcatFailed)))
	if err != nil || video == nil {
		return false, err
	}
	totals := &llm.Totals{}
	cctx := llm.WithTotals(llm.WithCall(ctx, llm.CallInfo{
		VideoID: video.ID, Title: video.Title, Channel: video.ChannelName, Step: "subcategories",
	}), totals)
	ident := []any{"video_id", video.ID, "title", video.Title, "category", video.Category}
	started := time.Now()
	subs, err := w.d.Summarizer.ClassifySubcategories(cctx, video.Title, video.Summary, video.Category)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		w.subcatFailed[video.ID] = true
		w.d.Logger.Warn("summarize worker: subcategories failed", append(ident, "duration_ms", elapsed, "err", err)...)
		return true, nil
	}
	applied, err := w.d.Videos.SetSubcategoriesIfCategory(ctx, video.ID, video.Category, subs)
	if err != nil {
		w.subcatFailed[video.ID] = true
		w.d.Logger.Error("summarize worker: set subcategories failed", append(ident, "err", err)...)
		return true, nil
	}
	attrs := append(ident, "subcategories", strings.Join(subs, ","), "applied", applied, "duration_ms", elapsed)
	w.d.Logger.Info("summarize worker: subcategorized video", append(attrs, totals.Snapshot().LogAttrs()...)...)
	return true, nil
}

func (w *Worker) subcategorizeChannelOne(ctx context.Context) (bool, error) {
	if w.d.Channels == nil {
		return false, nil
	}
	c, err := w.d.Channels.NextToSubcategorize(ctx, slices.Collect(maps.Keys(w.channelSubcatFailed)))
	if err != nil || c == nil {
		return false, err
	}
	ident := []any{"channel_id", c.ID, "channel", c.Name, "videos", c.Videos}
	titles, err := w.d.Videos.ChannelTitles(ctx, c.ID, channelTitlesForSubcategories)
	if err != nil {
		w.channelSubcatFailed[c.ID] = true
		w.d.Logger.Error("summarize worker: channel titles failed", append(ident, "err", err)...)
		return false, nil
	}
	totals := &llm.Totals{}
	cctx := llm.WithTotals(llm.WithCall(ctx, llm.CallInfo{Channel: c.Name, Step: "channel-subcategories"}), totals)
	started := time.Now()
	subs, err := w.d.Summarizer.ClassifyChannelSubcategories(cctx, cmp.Or(c.Name, c.Handle, c.ID), c.Description, titles)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		w.channelSubcatFailed[c.ID] = true
		w.d.Logger.Warn("summarize worker: channel subcategories failed", append(ident, "duration_ms", elapsed, "err", err)...)
		return true, nil
	}
	applied, err := w.d.Channels.SetSubcategoriesIfAuto(ctx, c.ID, subs, c.Videos)
	if err != nil {
		w.channelSubcatFailed[c.ID] = true
		w.d.Logger.Error("summarize worker: set channel subcategories failed", append(ident, "err", err)...)
		return true, nil
	}
	attrs := append(ident, "subcategories", strings.Join(subs, ","), "applied", applied, "duration_ms", elapsed)
	w.d.Logger.Info("summarize worker: subcategorized channel", append(attrs, totals.Snapshot().LogAttrs()...)...)
	return true, nil
}
