package summarize

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/videos"
)

// Category and subcategories run on every read, an inbox read included, or
// the Inbox, Search and MCP could not tell what an undownloaded video is
// about (a category filter dropped it). Both read only the title and the
// summary, and both are best-effort: a failure must NOT fail the job, the
// summary is already saved, and the idle sweeps (classifyOne,
// subcategorizeOne) pick the video up later.

// classifyStep is step 2: the category, if the row has none. It returns the
// category this run wrote, "" when it wrote none (skipped, failed, or the
// user picked one by hand meanwhile).
//
// `video` was read before the summary call, so the test below is against a
// stale row; SetCategoryIfUnset re-checks at write time, which is what
// protects a category the user picked on the Player meanwhile.
//
// live says whether to surface the step as the Player's "classifying" phase.
// Only while the summary is still running: a row already 'done' (a resumed
// job, an inbox read's post-download pass, the inbox branch itself) must not
// be moved back to running by the emit.
func (w *Worker) classifyStep(video *videos.Video, run *analysisRun, summary string, live bool) string {
	if video.Category != "" && video.Category != videos.UncategorizedCategory {
		run.skipped("classify", "already categorized")
		return ""
	}
	if live {
		w.emit(video.ID, videos.SummaryRunning, PhaseClassifying)
	}
	cctx, done := run.step("classify")
	raw, err := w.d.Summarizer.Classify(cctx, video.Title, summary, videos.ClassifiableCategories())
	if err != nil {
		w.d.Logger.Warn("summarize worker: classify failed", append(run.ident(), "duration_ms", run.stepElapsedMs(), "err", err)...)
		return ""
	}
	category := videos.NormalizeCategory(raw)
	applied, err := w.d.Videos.SetCategoryIfUnset(video.ID, category)
	if err != nil {
		w.d.Logger.Error("summarize worker: set category failed", append(run.ident(), "err", err)...)
		return ""
	}
	// applied=false means the user picked a category by hand while this call
	// was in flight, and theirs won.
	done("category", category, "applied", applied)
	if !applied {
		return ""
	}
	return category
}

// labelStep labels the video with subcategories of its category. It runs
// after the summary is persisted 'done' and makes no emit, so a slow gate
// call never holds back a page waiting on the summary.
//
// classified is what classifyStep wrote this run. A fresh category dropped
// any old labels, so there is nothing to look up. Otherwise the row's own
// category stands, and the subcategory state says whether a prior run (or
// the sweep) already answered under it. A category picked by hand meanwhile
// is caught by SetSubcategoriesIfCurrent, which refuses labels chosen under
// another category or summary.
func (w *Worker) labelStep(ctx context.Context, video *videos.Video, run *analysisRun, summary, classified string) {
	v := *video
	v.Summary = summary
	if classified != "" {
		v.Category = classified
	} else {
		if v.Category == "" || v.Category == videos.UncategorizedCategory {
			return
		}
		answered, err := w.d.Videos.SubcategoriesAnswered(ctx, v.ID)
		if err != nil {
			w.d.Logger.Warn("summarize worker: read subcategory state failed", append(run.ident(), "err", err)...)
			return
		}
		if answered {
			run.skipped("subcategories", "already labeled")
			return
		}
	}
	sctx, done := run.step("subcategories")
	subs, applied, err := w.labelVideo(sctx, &v)
	if err != nil {
		w.logLabelFailure(err, append(run.ident(), "duration_ms", run.stepElapsedMs()))
		return
	}
	done("subcategories", strings.Join(subs, ","), "applied", applied)
}

// errLabelStore marks a labelVideo failure that came from the store rather
// than the model, so it logs at error, not as routine gate noise.
var errLabelStore = errors.New("store subcategories")

// labelVideo asks for v's subcategories under its category and stores them
// while the row still has the category and summary they were chosen from.
// The analysis step and the idle sweep share it, so both write labels on the
// same terms.
func (w *Worker) labelVideo(ctx context.Context, v *videos.Video) (subs []string, applied bool, err error) {
	subs, err = w.d.Summarizer.ClassifySubcategories(ctx, v.Title, v.Summary, v.Category)
	if err != nil {
		return nil, false, err
	}
	applied, err = w.d.Videos.SetSubcategoriesIfCurrent(ctx, v.ID, v.Category, v.Summary, subs)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", errLabelStore, err)
	}
	return subs, applied, nil
}

// logLabelFailure logs a labelVideo error at the level its source deserves.
func (w *Worker) logLabelFailure(err error, attrs []any) {
	attrs = append(attrs, "err", err)
	if errors.Is(err, errLabelStore) {
		w.d.Logger.Error("summarize worker: set subcategories failed", attrs...)
		return
	}
	w.d.Logger.Warn("summarize worker: subcategories failed", attrs...)
}
