package httpapi

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/llm"
)

// Manual measurement harnesses, not tests of anything. They answer the question
// a unit test cannot: what actually makes Ask — peeq's only user-blocking model
// call — slow, and which knob moves it.
//
// WHAT THEY ESTABLISHED (2026-08-04, the 12-excerpt prompt below, one question,
// eight repeats per setting):
//
//	reasoning_effort=high    ttft 4858ms   reasoning [25 26 26 26 26 157 221 266]
//	reasoning_effort=low     ttft 6200ms   reasoning [26 26 26 28 28 307 333 383]
//	thinking disabled        ttft 1084ms   reasoning [0 0 0 0 0 0 0 0]
//
// reasoning_effort is INERT here. high and low produce the same distribution —
// low came out marginally deeper, which is noise, not an inversion. Probably
// MiMo's native thinking field takes precedence over the OpenAI-compatible
// parameter, but that mechanism is a guess; the measurement is not. Do not reach
// for WithReasoningEffort expecting a latency change.
//
// thinking:disabled is the lever that works, and it is worth ~3.8s of
// time-to-first-token. Whether Ask can afford it is a quality question, which is
// what TestAskThinkingQuality prints the evidence for.
//
// The bimodal reasoning counts (five calls near 26, three in the hundreds)
// reproduce at every setting and are unexplained. Whatever drives them, it is
// not the effort parameter.
//
// Skipped unless PEEQ_ASK_SWEEP=1: they call the real endpoint and cost real
// tokens. Run from the repo root with the environment loaded:
//
//	set -a; . ./.env; set +a
//	PEEQ_ASK_SWEEP=1 go test ./internal/httpapi/ -run TestAsk -v -timeout 30m
//
// Retrieval is deliberately not involved: it is identical at every setting, and
// wiring it in would make the corpus depend on whatever the local library holds.
// The excerpts below are fixed and sized like real ones.
func TestAskEffortSweep(t *testing.T) {
	if os.Getenv("PEEQ_ASK_SWEEP") != "1" {
		t.Skip("manual: set PEEQ_ASK_SWEEP=1 (calls the real chat endpoint)")
	}
	baseURL := os.Getenv("BACKEND_CHAT_BASE_URL")
	if baseURL == "" {
		t.Fatal("BACKEND_CHAT_BASE_URL is unset — load .env first")
	}

	client := llm.NewClient(llm.Config{
		BaseURL:     baseURL,
		APIKey:      os.Getenv("BACKEND_CHAT_API_KEY"),
		CallTimeout: 5 * time.Minute,
	}, nil)

	type run struct {
		effort   string
		question string
		ttft     time.Duration
		total    time.Duration
		reason   int64
		out      int64
		answer   string
		err      error
	}
	var runs []run

	// Interleaved by question rather than grouped by effort, so a slow patch at
	// the endpoint spreads across all three levels instead of landing on one and
	// being read as a property of that level.
	for _, q := range sweepQuestions {
		for _, effort := range []string{"high", "medium", "low"} {
			totals := &llm.Totals{}
			ctx := llm.WithCall(context.Background(), llm.CallInfo{Step: "answer-sweep", Totals: totals})
			ctx = llm.WithMaxTokens(ctx, answerMaxTokens)
			ctx = llm.WithReasoningEffort(ctx, effort)

			start := time.Now()
			var ttft time.Duration
			answer, err := client.CompleteStream(ctx, answerMessages(q, sweepExcerpts), func(string) {
				if ttft == 0 {
					ttft = time.Since(start)
				}
			})
			u := totals.Snapshot()
			runs = append(runs, run{
				effort: effort, question: q, ttft: ttft, total: time.Since(start),
				reason: u.ReasoningTokens, out: u.CompletionTokens, answer: answer, err: err,
			})
			if err != nil {
				t.Logf("ERROR effort=%s q=%q: %v", effort, q, err)
			}
		}
	}

	// Per-effort medians. The mean is the wrong summary here: one 60s queue
	// wait moves it by seconds and says nothing about the typical answer.
	t.Log("\n=== per-run ===")
	for _, r := range runs {
		t.Logf("%-6s ttft=%5dms total=%5dms reasoning=%4d out=%4d q=%.40q",
			r.effort, r.ttft.Milliseconds(), r.total.Milliseconds(), r.reason, r.out, r.question)
	}

	t.Log("\n=== medians ===")
	for _, effort := range []string{"high", "medium", "low"} {
		var ttfts, totals, reasons []int64
		for _, r := range runs {
			if r.effort == effort && r.err == nil {
				ttfts = append(ttfts, r.ttft.Milliseconds())
				totals = append(totals, r.total.Milliseconds())
				reasons = append(reasons, r.reason)
			}
		}
		t.Logf("%-6s n=%d  ttft=%dms  total=%dms  reasoning=%d tokens",
			effort, len(ttfts), median(ttfts), median(totals), median(reasons))
	}

	// The answers themselves, so the quality half of the decision is made by
	// reading them rather than by trusting the timings.
	t.Log("\n=== answers ===")
	for _, q := range sweepQuestions {
		t.Logf("\nQ: %s", q)
		for _, r := range runs {
			if r.question == q {
				t.Logf("  [%s] %s", r.effort, strings.ReplaceAll(r.answer, "\n", " "))
			}
		}
	}
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
}

// Questions chosen for the shapes the system prompt has rules about: plain
// synthesis, a claim only one excerpt supports, two excerpts that disagree, and
// one the corpus cannot answer at all (which must produce a refusal, not a
// guess). Depth is most likely to show up in the last two.
var sweepQuestions = []string{
	"Why do endurance athletes cramp late in a race?",
	"What does the evidence say about heat acclimation before a hot race?",
	"Do the videos agree about whether carbohydrate intake should be raised above 90g an hour?",
	"How should I pace a long climb at altitude?",
	"What is the recommended tyre pressure for wet cobbles?",
	"What do these videos say about sleep and recovery?",
}

// Twelve excerpts, the number retrieval hands the model, each in the exact
// wrapper handleAnswer builds and sized in the same range (~400-900 runes).
var sweepExcerpts = func() []string {
	type src struct {
		title string
		at    int
		text  string
	}
	srcs := []src{
		{"Why Athletes Cramp", 872, "The old electrolyte story does not survive contact with the data. Riders who cramp and riders who do not show the same sodium losses across a stage, and the cramping limb is almost always the one doing the most work. What separates them is neuromuscular fatigue: the muscle spindle keeps firing while the Golgi tendon organ, which would normally damp it, goes quiet under prolonged load. That is why a cramp arrives in the last hour and not the first, and why it hits the quadriceps of a rider who has been pushing a big gear rather than the calf of one spinning."},
		{"Why Athletes Cramp", 1240, "Pickle juice works, but not for the reason people assume. Relief arrives in about thirty seconds, far faster than anything absorbed from the gut could act. The acid appears to trigger receptors in the mouth and throat that interrupt the reflex loop driving the cramp. This matters practically: you do not need to drink much, and topping up electrolytes hours in advance will not prevent a cramp that is really a fatigue phenomenon."},
		{"Fuelling the Long Ride", 310, "Ninety grams an hour was the ceiling for a decade, set by how fast the gut can move glucose and fructose together. That number has moved. Riders on structured gut-training protocols now tolerate 110 to 120 grams an hour in competition, and the limiting factor turned out to be trainable transporter density rather than a hard anatomical limit. The catch is that gut training takes weeks and has to continue through the season."},
		{"Fuelling the Long Ride", 980, "Do not take the higher numbers as a target for everyone. In our own testing, riders who jumped straight to 120 grams an hour without the preparation had gastrointestinal distress at roughly twice the rate, and a rider who cannot finish the ride has not gained anything from the extra carbohydrate. Ninety grams an hour remains the sensible default for anyone who has not deliberately trained the gut."},
		{"Racing in the Heat", 145, "Heat acclimation is the largest free gain available to an endurance athlete, and it is badly underused. Ten to fourteen consecutive days of heat exposure raises plasma volume by five to ten per cent, drops core temperature at a given workload, and lowers heart rate by around ten beats a minute in the heat. The adaptations begin within three or four days but decay almost as fast: miss a week and most of it is gone."},
		{"Racing in the Heat", 720, "The protocol matters less than the consistency. Sitting in a sauna after a normal ride works about as well as training in the heat, and it is far easier to fit around a training plan. What does not work is a single long exposure the week before the event: that produces the fatigue without the adaptation, and riders who try it usually arrive at the start line worse off than if they had done nothing."},
		{"Altitude and Pacing", 402, "Power at altitude falls roughly one per cent for every hundred metres above fifteen hundred, and the loss is larger for efforts above threshold than below it. The practical consequence is that a rider who paces a high climb by the numbers they know from sea level will blow before the top. Pace by perceived effort and by breathing rather than by the power meter, and accept the lower number without treating it as a bad day."},
		{"Altitude and Pacing", 1105, "On a long climb, the first two kilometres decide the rest. Riders lose more time to going out too hard in the opening section than to anything that happens later, because the recovery from an early overshoot costs more than the time it bought. Start at a pace you believe you could hold for twenty minutes longer than the climb actually lasts, and let the last third be where you spend what is left."},
		{"Sleep and the Athlete", 88, "Sleep is the only recovery intervention with an effect size large enough to be uncontroversial. Extending sleep to nine or ten hours a night improved sprint times, reaction time and accuracy in every controlled study we could find, and the effect appeared within a week. No supplement, no recovery boot and no ice bath comes close, and yet it is the first thing cut when a training plan gets busy."},
		{"Sleep and the Athlete", 640, "Napping is not a consolation prize. A twenty to thirty minute nap in the early afternoon restores most of the performance lost to a short night, provided it stays short enough to avoid deep sleep. Riders who nap after a hard morning session also show lower evening cortisol, which suggests the benefit is not only in how they feel."},
		{"Wet Weather Handling", 210, "Everything about riding on wet cobbles is counterintuitive. The instinct is to grip harder and brake earlier; both make it worse. A loose upper body lets the bike move under you, which is what keeps the tyres tracking. Brake on the smooth sections between the stones, never on the stones themselves, and choose the crown of the road where the camber is flattest even though the gutter looks faster."},
		{"Wet Weather Handling", 915, "Riders ask about tyre choice and we keep giving the same unsatisfying answer: the tyre matters less than the line. A wider tyre helps at the margin, but the rider who picks a clean line on a mediocre tyre will be upright at the finish and the one who fights the surface on a perfect tyre will not. Take the corner wide, get the bike vertical before you touch the brakes, and let the line do the work."},
	}
	out := make([]string, 0, len(srcs))
	for i, s := range srcs {
		out = append(out, fmt.Sprintf("<excerpt n=%q title=%q at=\"%ds\">\n%s\n</excerpt>",
			fmt.Sprint(i+1), s.title, s.at, s.text))
	}
	return out
}()

// The sweep above showed reasoning-token counts that did not track the effort
// level — high returning 28 tokens on one question while low returned 312 on
// another. Either the parameter is inert on this endpoint or six samples per
// level cannot see past the variance. This separates the two: ONE question, many
// repeats, three settings. If effort works, the distributions separate; if it is
// inert, high and low overlap and only thinking:disabled moves.
//
// thinking:disabled is in the comparison because it is the lever peeq already
// knows works (the classify and keypoints calls rely on it), so it calibrates
// what a real effect looks like against this same endpoint.
func TestAskEffortIsInert(t *testing.T) {
	if os.Getenv("PEEQ_ASK_SWEEP") != "1" {
		t.Skip("manual: set PEEQ_ASK_SWEEP=1 (calls the real chat endpoint)")
	}
	baseURL := os.Getenv("BACKEND_CHAT_BASE_URL")
	if baseURL == "" {
		t.Fatal("BACKEND_CHAT_BASE_URL is unset — load .env first")
	}
	client := llm.NewClient(llm.Config{
		BaseURL: baseURL, APIKey: os.Getenv("BACKEND_CHAT_API_KEY"), CallTimeout: 5 * time.Minute,
	}, nil)

	const q = "Why do endurance athletes cramp late in a race?"
	const repeats = 8
	settings := []struct {
		name     string
		decorate func(context.Context) context.Context
	}{
		{"high", func(c context.Context) context.Context { return llm.WithReasoningEffort(c, "high") }},
		{"low", func(c context.Context) context.Context { return llm.WithReasoningEffort(c, "low") }},
		{"no-think", llm.WithoutThinking},
	}

	for _, s := range settings {
		var ttfts, reasons []int64
		for i := 0; i < repeats; i++ {
			totals := &llm.Totals{}
			ctx := llm.WithCall(context.Background(), llm.CallInfo{Step: "inert-probe", Totals: totals})
			ctx = s.decorate(llm.WithMaxTokens(ctx, answerMaxTokens))
			start := time.Now()
			var ttft time.Duration
			if _, err := client.CompleteStream(ctx, answerMessages(q, sweepExcerpts), func(string) {
				if ttft == 0 {
					ttft = time.Since(start)
				}
			}); err != nil {
				t.Logf("ERROR %s #%d: %v", s.name, i, err)
				continue
			}
			u := totals.Snapshot()
			ttfts = append(ttfts, ttft.Milliseconds())
			reasons = append(reasons, u.ReasoningTokens)
		}
		sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
		t.Logf("%-9s n=%d ttft_median=%5dms reasoning_median=%4d reasoning_all=%v",
			s.name, len(ttfts), median(ttfts), median(reasons), reasons)
	}
}

// Given that effort is inert and thinking:disabled is not, the real question
// becomes whether Ask can afford to lose thinking. This prints both answers to
// every sweep question side by side so the citation behaviour, the refusal on a
// question the corpus cannot answer, and the six-sentence limit can be read
// rather than assumed. Timing is reported too, but the decision here is quality.
func TestAskThinkingQuality(t *testing.T) {
	if os.Getenv("PEEQ_ASK_SWEEP") != "1" {
		t.Skip("manual: set PEEQ_ASK_SWEEP=1 (calls the real chat endpoint)")
	}
	baseURL := os.Getenv("BACKEND_CHAT_BASE_URL")
	if baseURL == "" {
		t.Fatal("BACKEND_CHAT_BASE_URL is unset — load .env first")
	}
	client := llm.NewClient(llm.Config{
		BaseURL: baseURL, APIKey: os.Getenv("BACKEND_CHAT_API_KEY"), CallTimeout: 5 * time.Minute,
	}, nil)

	for _, q := range sweepQuestions {
		t.Logf("\n──────── %s", q)
		for _, mode := range []string{"thinking", "no-think"} {
			ctx := llm.WithMaxTokens(context.Background(), answerMaxTokens)
			if mode == "no-think" {
				ctx = llm.WithoutThinking(ctx)
			}
			start := time.Now()
			var ttft time.Duration
			answer, err := client.CompleteStream(ctx, answerMessages(q, sweepExcerpts), func(string) {
				if ttft == 0 {
					ttft = time.Since(start)
				}
			})
			if err != nil {
				t.Logf("  [%s] ERROR %v", mode, err)
				continue
			}
			t.Logf("  [%-8s ttft=%4dms] %s", mode, ttft.Milliseconds(), strings.ReplaceAll(answer, "\n", " "))
		}
	}
}
