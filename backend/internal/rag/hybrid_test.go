package rag

import "testing"

func TestBuildFTSMatch(t *testing.T) {
	cases := map[string]string{
		"sourdough starter": `"sourdough" "starter"`,
		"  hello  ":         `"hello"`,
		`quote"inject`:      `"quote" "inject"`, // stray quote is a delimiter, not an operator
		"":                  "",
		`AND OR NOT`:        `"and" "or" "not"`, // operators neutralized by quoting+lowercasing
	}
	for in, want := range cases {
		if got := BuildFTSMatch(in); got != want {
			t.Errorf("BuildFTSMatch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFuseRRFPrefersBothLists(t *testing.T) {
	a := Hit{VideoID: "v", Ordinal: 1, Text: "a"}
	b := Hit{VideoID: "v", Ordinal: 2, Text: "b"}
	c := Hit{VideoID: "v", Ordinal: 3, Text: "c"}
	// a is rank-2 in list1 but appears in BOTH lists; b/c appear once each.
	list1 := []Hit{b, a}
	list2 := []Hit{a, c}
	out := FuseRRF([][]Hit{list1, list2}, 10)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if !(out[0].Ordinal == 1) {
		t.Errorf("top ordinal = %d, want 1 (found by both lists)", out[0].Ordinal)
	}
}
