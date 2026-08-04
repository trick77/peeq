package rag

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseFTSQueryPreservesOperators(t *testing.T) {
	cases := map[string]string{
		// bare terms are implicitly ANDed, as before
		"electrolytes endurance": `"electrolytes" "endurance"`,
		// a quoted phrase stays one phrase
		`"maximum aerobic function"`: `"maximum aerobic function"`,
		// uppercase keywords are operators
		"sodium OR potassium":   `"sodium" OR "potassium"`,
		"hydration NOT sponsor": `"hydration" NOT "sponsor"`,
		// lowercase ones are words the user wants to find — FTS5's own rule,
		// and what keeps "rock or roll" searchable
		"rock or roll": `"rock" "or" "roll"`,
		// prefix match
		"cramp*": `"cramp" *`,
		// mixed
		`"heart rate" OR hrv*`: `"heart rate" OR "hrv" *`,
		"":                     "",
		"   ":                  "",
		// punctuation-only input yields nothing usable
		"!!! ???": "",
	}
	for in, want := range cases {
		if got := ParseFTSQuery(in); got != want {
			t.Errorf("ParseFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFTSQueryNeverEmitsUserSyntax(t *testing.T) {
	// Anything a user could type that FTS5 would read as syntax must come back
	// re-emitted from recognized tokens, never passed through. A syntax error
	// inside MATCH surfaces as a 500, so this is the injection guard.
	cases := []string{
		`quote"inject`,
		`a) OR (b`,
		`NEAR(a b, 2)`,
		`^anchored`,
		`col:value`,
		`"unbalanced`,
		`* * *`,
		`{braced}`,
		`a AND AND b`,
		`OR leading`,
		`trailing OR`,
	}
	for _, in := range cases {
		got := ParseFTSQuery(in)
		if got == "" {
			continue
		}
		// Every token is either a bare operator, a bare '*', or a quoted term
		// with no interior quote.
		for _, tok := range splitTokens(got) {
			switch tok {
			case "OR", "AND", "NOT", "*":
				continue
			}
			if !strings.HasPrefix(tok, `"`) || !strings.HasSuffix(tok, `"`) {
				t.Errorf("ParseFTSQuery(%q) = %q: token %q is neither operator nor quoted term", in, got, tok)
				continue
			}
			if strings.Contains(strings.Trim(tok, `"`), `"`) {
				t.Errorf("ParseFTSQuery(%q) = %q: token %q has an interior quote", in, got, tok)
			}
		}
		// An expression may not begin or end with a boolean operator.
		toks := splitTokens(got)
		for _, edge := range []string{toks[0], toks[len(toks)-1]} {
			if edge == "OR" || edge == "AND" || edge == "NOT" {
				t.Errorf("ParseFTSQuery(%q) = %q: dangling operator %q", in, got, edge)
			}
		}
	}
}

// splitTokens splits a MATCH expression into quoted terms and bare words,
// so a test can assert on each piece without reimplementing the parser.
func splitTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		switch {
		case s[i] == ' ':
			i++
		case s[i] == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j < len(s) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			j := i
			for j < len(s) && s[j] != ' ' {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}

func TestBuildFTSQueriesRelaxesAQuestion(t *testing.T) {
	// The query from the bug report: every function word ANDed matches nothing,
	// so the keyword lane used to abstain entirely and leave pure vector search.
	got := BuildFTSQueries("Did someone ever talk about electrolytes being useful in endurance sport?")
	if len(got) != 4 {
		t.Fatalf("want 4 tiers, got %d: %+v", len(got), got)
	}
	if got[0].Match != BuildFTSMatch("Did someone ever talk about electrolytes being useful in endurance sport?") {
		t.Errorf("tier 0 must equal BuildFTSMatch, got %q", got[0].Match)
	}
	for _, dropped := range []string{`"did"`, `"someone"`, `"ever"`, `"about"`, `"being"`, `"in"`} {
		if strings.Contains(got[1].Match, dropped) {
			t.Errorf("tier 1 %q should not contain stopword %s", got[1].Match, dropped)
		}
	}
	for _, kept := range []string{`"electrolytes"`, `"endurance"`, `"sport"`, `"talk"`, `"useful"`} {
		if !strings.Contains(got[1].Match, kept) {
			t.Errorf("tier 1 %q dropped content term %s", got[1].Match, kept)
		}
	}
	// The prefix rung keeps the content rung's requirement that every word appears
	// and widens each one to its inflections, so it must still be ANDed.
	if strings.Contains(got[2].Match, " OR ") || !strings.Contains(got[2].Match, `"electrolytes"*`) {
		t.Errorf("tier 2 %q should be the content terms ANDed as prefixes", got[2].Match)
	}
	// The floor is a PREFIX or. Without stemming in the index, a literal floor
	// cannot reach a video that wrote the singular of a word the question pluralised.
	if !strings.Contains(got[3].Match, " OR ") || !strings.Contains(got[3].Match, `"sport"*`) {
		t.Errorf("tier 3 %q should be the prefix OR recall floor", got[3].Match)
	}
	want := []float64{
		WeightKeywordStrict, WeightKeywordContent,
		WeightKeywordPrefix, WeightKeywordAny,
	}
	for i, w := range want {
		if got[i].Weight != w {
			t.Errorf("tier %d weight = %v, want %v", i, got[i].Weight, w)
		}
	}
}

func TestBuildFTSQueriesSkipsRedundantTiers(t *testing.T) {
	// No stopwords and one term: the content-ANDed rung would reproduce the strict
	// tier, so it is dropped. The prefix rung is NOT a duplicate — it is what
	// reaches "electrolyte" — so it stays, and the OR floor of one term would
	// reproduce it, so that goes.
	if got := BuildFTSQueries("electrolytes"); !reflect.DeepEqual(got, []FTSTier{
		{Match: `"electrolytes"`, Weight: WeightKeywordStrict},
		{Match: `"electrolytes"*`, Weight: WeightKeywordPrefix},
	}) {
		t.Errorf("single content term should yield strict + prefix, got %+v", got)
	}
	// No stopwords, several terms: the AND tier duplicates strict, so the ladder is
	// strict, prefixes ANDed, prefixes ORed.
	got := BuildFTSQueries("electrolytes endurance")
	if len(got) != 3 || got[0].Match != `"electrolytes" "endurance"` ||
		got[1].Match != `"electrolytes"* "endurance"*` ||
		got[2].Match != `"electrolytes"* OR "endurance"*` {
		t.Fatalf("unexpected tiers: %+v", got)
	}
	// The rung that matters: with no stopwords the OR floor lands in THIRD
	// position, where the content tier would sit on a query that had stopwords.
	// Weighing by slice position would hand it the content weight and float it
	// above the semantic lane — exactly the burial this ladder exists to prevent.
	if got[2].Weight != WeightKeywordAny {
		t.Errorf("the OR floor weighs %v, want the floor %v — its position in the "+
			"ladder must not decide its weight", got[2].Weight, WeightKeywordAny)
	}
	if got[2].Weight >= WeightSemantic {
		t.Errorf("the OR floor (%v) must weigh less than the semantic lane (%v)",
			got[2].Weight, WeightSemantic)
	}
}

func TestBuildFTSQueriesAllStopwords(t *testing.T) {
	// Nothing survives the stopword filter, so the strict tier must stand alone
	// rather than relaxing to an empty (and invalid) expression.
	got := BuildFTSQueries("what is it about")
	if len(got) != 1 {
		t.Fatalf("want 1 tier, got %d: %+v", len(got), got)
	}
	if got[0].Match == "" {
		t.Error("strict tier must not be empty")
	}
	if got := BuildFTSQueries("   "); got != nil {
		t.Errorf("blank query should yield no tiers, got %+v", got)
	}
}
