package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/summarize"
	"github.com/trick77/peeq/internal/videos"
)

// filteredAnswerDeps builds a two-channel library where both videos are
// indexed on the same subject, one watched and one not. Every question below is
// about which slice of it the answer was allowed to see.
func filteredAnswerDeps(t *testing.T) (Deps, *sql.DB, *fakeAsk, *fakeUnderstander) {
	t.Helper()
	deps, db, ragStore := searchTestDepsWithStores(t)
	for _, v := range []videos.Video{
		{ID: "v1", URL: "u1", Title: "What Is An Ontology", ChannelID: "UC1",
			ChannelName: "Veritasium", Watched: true, Category: "science",
			PublishedAt: "2020-05-01"},
		{ID: "v2", URL: "u2", Title: "Ontology For Engineers", ChannelID: "UC2",
			ChannelName: "Kurzgesagt", Category: "science", PublishedAt: "2026-05-01"},
	} {
		if err := deps.Videos.Upsert(v); err != nil {
			t.Fatalf("seed %s: %v", v.ID, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO channels (id, handle, name) VALUES
		('UC1','@veritasium','Veritasium'), ('UC2','@kurzgesagt','Kurzgesagt')`); err != nil {
		t.Fatal(err)
	}
	// Upsert is the discovery path and writes only what discovery knows: watch
	// state, category and chapters are written later by the player and the
	// summarize worker. Set them the way those do.
	if _, err := db.Exec(`UPDATE videos SET watched = 1 WHERE id = 'v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE videos SET category = 'science'`); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "an ontology is a shared vocabulary", StartSeconds: 100},
	})
	seedChunks(t, ragStore, "v2", []rag.ChunkRow{
		{Ordinal: 0, Text: "an ontology in engineering practice", StartSeconds: 200},
	})
	ask := &fakeAsk{deltas: []string{"Both cover it[1]."}}
	understand := &fakeUnderstander{}
	deps.Ask = ask
	deps.Understand = understand
	return deps, db, ask, understand
}

// frame returns the decoded data of the first SSE event with this name.
func frame(t *testing.T, body, name string) map[string]any {
	t.Helper()
	for _, e := range events(t, body) {
		if e[0] != name {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(e[1]), &m); err != nil {
			t.Fatalf("decode %s frame: %v", name, err)
		}
		return m
	}
	t.Fatalf("no %s frame in:\n%s", name, body)
	return nil
}

// tokens concatenates every streamed token, which is what the reader sees.
func tokens(t *testing.T, body string) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events(t, body) {
		if e[0] != "token" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(e[1]), &m); err != nil {
			t.Fatalf("decode token: %v", err)
		}
		b.WriteString(m["text"])
	}
	return b.String()
}

func askFor(t *testing.T, deps Deps, q string) string {
	t.Helper()
	h := New(deps)
	rec := doReq(t, h, loginAndGetCookie(t, h), http.MethodGet, "/api/search/answer?q="+q, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

// The question the whole feature was built for. "unwatched" has to reach
// retrieval and keep the watched video out of the sources entirely.
func TestAnswerAppliesWatchedFilter(t *testing.T) {
	deps, _, ask, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"watched":"unwatched"}}`
	body := askFor(t, deps, "do+we+have+unwatched+videos+about+ontology")

	sources := frame(t, body, "sources")
	got, _ := json.Marshal(sources["sources"])
	if strings.Contains(string(got), "v1") {
		t.Fatalf("the watched video reached the sources:\n%s", got)
	}
	if !strings.Contains(string(got), "v2") {
		t.Fatalf("the unwatched video is missing:\n%s", got)
	}
	if applied, _ := json.Marshal(sources["filters"]); string(applied) != `["unwatched"]` {
		t.Errorf("filters frame = %s", applied)
	}
	// The model must be told the excerpts are a slice, or it writes about "your
	// library" as though it had seen all of it.
	if !strings.Contains(ask.messages[1].Content, "Constraints applied to the search: unwatched.") {
		t.Errorf("the constraints line is missing:\n%s", ask.messages[1].Content)
	}
}

func TestAnswerAppliesChannelFilter(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"channels":["Veritasium"]}}`
	body := askFor(t, deps, "does+Veritasium+cover+ontology")

	got, _ := json.Marshal(frame(t, body, "sources")["sources"])
	if strings.Contains(string(got), "v2") {
		t.Fatalf("the other channel reached the sources:\n%s", got)
	}
	if !strings.Contains(string(got), "v1") {
		t.Fatalf("the named channel is missing:\n%s", got)
	}
}

// A typo is corrected without a word said about it — but the corrected name is
// shown as the applied filter, which is the disclosure.
func TestAnswerResolvesChannelTypoSilently(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"channels":["Veritaseum"]}}`
	body := askFor(t, deps, "does+Veritaseum+cover+ontology")

	applied, _ := json.Marshal(frame(t, body, "sources")["filters"])
	if string(applied) != `["Veritasium"]` {
		t.Fatalf("filters frame = %s, want the corrected name", applied)
	}
	if text := tokens(t, body); strings.Contains(text, "Veritaseum") {
		t.Errorf("a silent correction must not be narrated: %q", text)
	}
}

// A channel that is not in the library at all is a different event: the search
// widens, and the reader is told so. Widening in silence would hand them another
// channel's videos with no indication their question was not honoured.
func TestAnswerReportsAnUnknownChannel(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"channels":["Numberphile"]}}`
	body := askFor(t, deps, "does+Numberphile+cover+ontology")

	text := tokens(t, body)
	if !strings.Contains(text, `There is no channel called "Numberphile" in your library.`) {
		t.Fatalf("the reader was not told: %q", text)
	}
	// And the search still answered, from the whole library.
	got, _ := json.Marshal(frame(t, body, "sources")["sources"])
	if !strings.Contains(string(got), "v1") || !strings.Contains(string(got), "v2") {
		t.Fatalf("the widened search should have found both:\n%s", got)
	}
	unresolved, _ := json.Marshal(frame(t, body, "sources")["unresolved_channels"])
	if string(unresolved) != `["Numberphile"]` {
		t.Errorf("unresolved_channels = %s", unresolved)
	}
}

// The failure this feature introduces, and the one it must not commit: a filter
// that finds nothing used to report that the library covers nothing.
func TestAnswerRelaxesAFilterThatFoundNothing(t *testing.T) {
	deps, _, ask, understand := filteredAnswerDeps(t)
	// Everything about ontology in this library is in 'science'; asking for
	// 'gaming' matches nothing at all.
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"category":"gaming"}}`
	body := askFor(t, deps, "any+gaming+videos+about+ontology")

	text := tokens(t, body)
	if strings.Contains(text, "Nothing in your library covers that") {
		t.Fatalf("the filter was not relaxed, so the answer lied: %q", text)
	}
	if !strings.Contains(text, "so this is drawn from the rest of your library") {
		t.Fatalf("the relaxation was not disclosed: %q", text)
	}
	sources := frame(t, body, "sources")
	relaxed, _ := json.Marshal(sources["relaxed"])
	if string(relaxed) != `["Gaming"]` {
		t.Errorf("relaxed = %s", relaxed)
	}
	if got, _ := json.Marshal(sources["sources"]); !strings.Contains(string(got), "v1") {
		t.Errorf("the relaxed search found nothing:\n%s", got)
	}
	// The model is told it is looking at the whole library, not at the slice
	// that was asked for and turned out to be empty.
	if !strings.Contains(ask.messages[1].Content, "found nothing, so these excerpts come from the whole library") {
		t.Errorf("the model was not told about the relaxation:\n%s", ask.messages[1].Content)
	}
}

// Relaxation must not turn a genuinely uncovered subject into a widened search
// that answers about something else.
func TestAnswerKeepsSayingNothingWhenNothingCovers(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"submarines","counting":false,"filters":{"watched":"unwatched"}}`
	// No embedder is wired here, so retrieval is FTS-only and a word the
	// library does not contain finds nothing under any filter.
	deps.Embedder = nil
	body := askFor(t, deps, "unwatched+videos+about+submarines")

	if text := tokens(t, body); !strings.Contains(text, "Nothing in your library covers that") {
		t.Fatalf("want the empty answer, got %q", text)
	}
}

// An inventory question is answered with a count, not with an estimate from
// whatever twelve excerpts happened to be chosen.
// The question a SQL count can actually answer: structural, no subject.
//
// Note what such a question does to retrieval. It names nothing to search FOR,
// so the lanes come back empty and the model is never called — the count IS the
// answer, and it has to be written here or the page says "nothing covers that"
// directly above a count line reading "2 videos".
func TestAnswerCountsForAnInventoryQuestion(t *testing.T) {
	deps, _, ask, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"","counting":true,"filters":{"category":"science"}}`
	body := askFor(t, deps, "how+many+science+videos+do+I+have")

	counts, ok := frame(t, body, "sources")["counts"].(map[string]any)
	if !ok {
		t.Fatal("no counts on the sources frame")
	}
	if counts["videos"] != float64(2) || counts["channels"] != float64(2) {
		t.Fatalf("counts = %+v", counts)
	}
	text := tokens(t, body)
	if strings.Contains(text, "Nothing in your library covers that") {
		t.Fatalf("the prose contradicts the count beside it: %q", text)
	}
	if !strings.Contains(text, "You have 2 videos matching Science & Research") {
		t.Fatalf("the count was not stated as the answer: %q", text)
	}
	if ask.called {
		t.Error("a structural count needs no model call")
	}
}

// A counted question whose count is zero says so, rather than reporting that
// the library covers nothing — the reader asked what they HOLD, not what is
// written about a subject.
func TestAnswerCountOfZeroSaysNothingMatches(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"","counting":true,"filters":{"category":"gaming"}}`
	body := askFor(t, deps, "how+many+gaming+videos+do+I+have")

	text := tokens(t, body)
	if !strings.Contains(text, "You have nothing matching Gaming.") {
		t.Fatalf("want a plain zero, got %q", text)
	}
}

// The count is SQL over the videos table and NO COLUMN HOLDS "is about
// ontology". A question carrying a topic must get no count at all — otherwise
// "how many unwatched videos about ontology" is answered with the size of the
// whole unwatched shelf, printed above the answer and handed to the model under
// a rule saying it is authoritative.
//
// Note this case HAS a filter, so gating on the filter alone would not catch
// it. The topic is what makes the number unanswerable.
func TestAnswerSkipsCountsForATopicalQuestion(t *testing.T) {
	deps, _, ask, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":true,"filters":{"watched":"unwatched"}}`
	body := askFor(t, deps, "how+many+unwatched+videos+about+ontology+do+I+have")

	if _, present := frame(t, body, "sources")["counts"]; present {
		t.Fatal("a topical question got a count SQL cannot compute")
	}
	if strings.Contains(ask.messages[1].Content, "Library counts") {
		t.Errorf("a topic-blind count reached the model:\n%s", ask.messages[1].Content)
	}
}

// And with no constraint either there is no scope row saying what was counted,
// so the number would just be the size of the library.
func TestAnswerSkipsCountsWithoutAFilter(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"","counting":true,"filters":{}}`
	body := askFor(t, deps, "how+many+videos+do+I+have")

	if _, present := frame(t, body, "sources")["counts"]; present {
		t.Fatal("an unfiltered count is a library size, not an answer")
	}
}

// The count answers the question that was ASKED, not the one that was
// eventually searched. "How many gaming videos" is zero even while the sources
// below it show the science ones, and the relaxation note reconciles them.
func TestAnswerCountsUseTheOriginalFilterNotTheRelaxedOne(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"","counting":true,"filters":{"category":"gaming"}}`
	body := askFor(t, deps, "how+many+gaming+videos+do+I+have")

	counts := frame(t, body, "sources")["counts"].(map[string]any)
	if counts["videos"] != float64(0) {
		t.Fatalf("count must reflect the asked-for filter, got %+v", counts)
	}
}

// A content question pays for no count at all.
func TestAnswerSkipsCountsForAContentQuestion(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{}}`
	body := askFor(t, deps, "what+is+an+ontology")
	if _, present := frame(t, body, "sources")["counts"]; present {
		t.Fatal("a content question should carry no counts")
	}
}

// An unfiltered question must behave exactly as it did before any of this
// existed: no constraints line, no counts, no notes.
func TestAnswerUnfilteredIsUnchanged(t *testing.T) {
	deps, _, ask, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{}}`
	body := askFor(t, deps, "what+is+an+ontology")

	if text := tokens(t, body); text != "Both cover it[1]." {
		t.Fatalf("unexpected prose: %q", text)
	}
	for _, unwanted := range []string{"Constraints applied", "Library counts", "found nothing"} {
		if strings.Contains(ask.messages[1].Content, unwanted) {
			t.Errorf("unfiltered prompt carries %q:\n%s", unwanted, ask.messages[1].Content)
		}
	}
	sources := frame(t, body, "sources")
	if applied, _ := json.Marshal(sources["filters"]); string(applied) != "null" {
		t.Errorf("filters = %s, want null", applied)
	}
}

// Relaxation re-runs retrieval, and retrieval embeds. It must not embed twice:
// the vectors do not depend on which videos are being searched.
func TestRelaxationReusesTheEmbedding(t *testing.T) {
	deps, _, _, understand := filteredAnswerDeps(t)
	embedder := &recordingEmbedder{}
	deps.Embedder = embedder
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"category":"gaming"}}`
	askFor(t, deps, "any+gaming+videos+about+ontology")

	if len(embedder.calls) != 1 {
		t.Fatalf("embedded %d times, want 1: %v", len(embedder.calls), embedder.calls)
	}
}

func TestChapterAt(t *testing.T) {
	v := &videos.Video{Chapters: `[{"title":"Intro","ts":0},
		{"title":"Aristotle's categories","ts":600},
		{"title":"Modern usage","ts":1800}]`}
	cases := map[int]string{
		0:    "Intro",
		599:  "Intro",
		600:  "Aristotle's categories",
		1799: "Aristotle's categories",
		5000: "Modern usage",
	}
	for at, want := range cases {
		if got := chapterAt(v, at); got != want {
			t.Errorf("chapterAt(%d) = %q, want %q", at, got, want)
		}
	}
	// A moment before the first chapter, a video with none, and a malformed
	// list all mean "no chapter" rather than an error on the answer path.
	if got := chapterAt(&videos.Video{Chapters: `[{"title":"Later","ts":60}]`}, 10); got != "" {
		t.Errorf("before the first chapter = %q, want empty", got)
	}
	if got := chapterAt(&videos.Video{Chapters: "[]"}, 10); got != "" {
		t.Errorf("no chapters = %q", got)
	}
	if got := chapterAt(&videos.Video{Chapters: "not json"}, 10); got != "" {
		t.Errorf("malformed chapters = %q", got)
	}
	if got := chapterAt(nil, 10); got != "" {
		t.Errorf("nil video = %q", got)
	}
	// Unordered lists must not label every moment with chapter one.
	unordered := &videos.Video{Chapters: `[{"title":"Second","ts":600},{"title":"First","ts":0}]`}
	if got := chapterAt(unordered, 700); got != "Second" {
		t.Errorf("unordered chapters = %q, want Second", got)
	}
}

func TestAnswerExcerptsCarryTheChapter(t *testing.T) {
	deps, db, ask, understand := filteredAnswerDeps(t)
	if _, err := db.Exec(`UPDATE videos SET chapters = ? WHERE id = 'v1'`,
		`[{"title":"Aristotle's categories","ts":60}]`); err != nil {
		t.Fatal(err)
	}
	understand.reply = `{"topic":"ontology","counting":false,"filters":{}}`
	askFor(t, deps, "what+is+an+ontology")

	if !strings.Contains(ask.messages[1].Content, `chapter="Aristotle's categories"`) {
		t.Fatalf("no chapter attribute in the excerpts:\n%s", ask.messages[1].Content)
	}
	// The video with no chapters gets no attribute rather than an empty one.
	if strings.Contains(ask.messages[1].Content, `chapter=""`) {
		t.Error("an empty chapter attribute was emitted")
	}
}

func TestDeterministicNote(t *testing.T) {
	if got := deterministicNote(nil, nil, 3); got != "" {
		t.Errorf("nothing to say = %q", got)
	}
	// A relaxation with no sources left is the empty answer's business, not
	// this one's — saying both would contradict itself.
	if got := deterministicNote(nil, []string{"unwatched"}, 0); got != "" {
		t.Errorf("relaxed with no sources = %q", got)
	}
	got := deterministicNote([]string{"A", "B"}, []string{"unwatched"}, 2)
	if !strings.Contains(got, `channels called "A" or "B"`) {
		t.Errorf("plural channels: %q", got)
	}
	if !strings.HasSuffix(got, " ") {
		t.Errorf("must end in a space so the model's first token continues it: %q", got)
	}
}

func TestEmptyAnswerNamesTheConstraint(t *testing.T) {
	if got := emptyAnswer(nil, nil, nil); got != "Nothing in your library covers that." {
		t.Errorf("unfiltered = %q", got)
	}
	if got := emptyAnswer([]string{"unwatched"}, nil, nil); got != "Nothing in your library covers that, within unwatched." {
		t.Errorf("filtered = %q", got)
	}
	// After a relaxation the search WAS the whole library, so the unqualified
	// sentence is the true one.
	if got := emptyAnswer([]string{"unwatched"}, []string{"unwatched"}, nil); got != "Nothing in your library covers that." {
		t.Errorf("relaxed = %q", got)
	}
	// A count overrides all of it: the reader asked what they hold, and
	// retrieval finding nothing to quote does not make the answer zero.
	got := emptyAnswer([]string{"unwatched"}, nil,
		&rag.LibraryCount{Videos: 12, DurationSeconds: 5400, Channels: 3})
	if got != "You have 12 videos matching unwatched, 1 h 30 min in all." {
		t.Errorf("counted = %q", got)
	}
	got = emptyAnswer([]string{"unwatched"}, nil, &rag.LibraryCount{})
	if got != "You have nothing matching unwatched." {
		t.Errorf("counted zero = %q", got)
	}
	if got := emptyAnswer(nil, nil,
		&rag.LibraryCount{Videos: 1, DurationSeconds: 600, Channels: 1}); !strings.Contains(got, "1 video ") {
		t.Errorf("singular = %q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int]string{
		0: "under a minute", 59: "under a minute", 60: "1 min", 3599: "59 min",
		3600: "1 h", 7200: "2 h", 5400: "1 h 30 min",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

// A comparison question is answered from whole summaries, not from a dozen
// transcript fragments taken out of the middle of two arguments.
func TestChooseExcerptsPrefersSummariesWhenComparing(t *testing.T) {
	deps, _, _ := searchTestDepsWithStores(t)
	for _, id := range []string{"v1", "v2"} {
		if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "u", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Transcript chunks rank above the summaries, so only a summary-first pass
	// can reach the summaries at all.
	var hits []rag.Hit
	for _, id := range []string{"v1", "v2"} {
		for i := range 4 {
			hits = append(hits, rag.Hit{VideoID: id, Ordinal: i, Text: "fragment",
				Kind: rag.KindTranscript, StartSeconds: i * 600})
		}
	}
	for i, id := range []string{"v1", "v2"} {
		hits = append(hits, rag.Hit{VideoID: id, Ordinal: 90 + i, Text: "whole summary",
			Kind: rag.KindSummary, StartSeconds: 0})
	}
	testee := &server{videos: deps.Videos}

	plain := testee.chooseExcerpts(hits, false)
	compare := testee.chooseExcerpts(hits, true)

	countSummaries := func(cs []excerptCandidate) int {
		n := 0
		for _, c := range cs {
			if c.hit.Kind == rag.KindSummary {
				n++
			}
		}
		return n
	}
	if countSummaries(compare) != 2 {
		t.Fatalf("comparison mode picked %d summaries, want both", countSummaries(compare))
	}
	if countSummaries(compare) <= countSummaries(plain) {
		t.Fatalf("comparison mode should favour summaries: %d vs %d",
			countSummaries(compare), countSummaries(plain))
	}
	// Transcript passages are still there — a comparison is not summaries only.
	if len(compare) <= 2 {
		t.Fatalf("comparison dropped the transcript evidence: %d excerpts", len(compare))
	}
}

// chapterAt reads a column WRITTEN BY ANOTHER PACKAGE, and this test is the only
// thing that keeps the two in step.
//
// The first version of chapterAt decoded `json:"start_seconds"`. The column
// actually holds summarize.Chapter, whose timestamp marshals as `ts`. Every
// chapter therefore parsed with a zero timestamp, and the "last chapter at or
// before this moment" loop then always chose the LAST chapter in the array — so
// every excerpt from a chaptered video was labelled with the title of that
// video's final chapter, and the answer prompt tells the model to use that label
// to say where in a long video something is covered.
//
// It passed every test, because the fixtures were hand-written with the same
// wrong key. Hand-written fixtures cannot catch a wrong key: they encode the
// author's belief about the format twice and agree with themselves. So this one
// marshals the producer's own type — if summarize.Chapter ever renames a field,
// this fails instead of quietly mislabelling every chapter again.
func TestChapterAtReadsWhatSummarizeWrites(t *testing.T) {
	stored, err := json.Marshal([]summarize.Chapter{
		{TS: 0, Title: "Intro", Source: "yt-dlp"},
		{TS: 600, Title: "Aristotle's categories", Source: "yt-dlp"},
		{TS: 1800, Title: "Modern usage", Source: "yt-dlp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	v := &videos.Video{Chapters: string(stored)}
	for at, want := range map[int]string{
		0:    "Intro",
		599:  "Intro",
		600:  "Aristotle's categories",
		5000: "Modern usage",
	} {
		if got := chapterAt(v, at); got != want {
			t.Errorf("chapterAt(%d) = %q, want %q — chapterAt and summarize.Chapter have drifted apart", at, got, want)
		}
	}
}

// A relaxed search runs the keyword ladder TWICE, and ftsMs is the sum of both.
// Reporting only the second ladder's rungs would print a total no rung can
// account for — the exact confusion per-rung timing exists to remove.
func TestAnswerLogsBothLaddersRungsWhenRelaxed(t *testing.T) {
	logs := captureLogs(t)
	deps, _, _, understand := filteredAnswerDeps(t)
	understand.reply = `{"topic":"ontology","counting":false,"filters":{"category":"gaming"}}`
	askFor(t, deps, "any+gaming+videos+about+ontology")

	line := logs.String()
	if !strings.Contains(line, "keyword_rungs=") {
		t.Fatalf("no retrieval line was logged:\n%s", line)
	}
	if !strings.Contains(line, secondLadderMarker) {
		t.Errorf("keyword_rungs carries one ladder's rungs against two ladders' ftsMs:\n%s", line)
	}
}
