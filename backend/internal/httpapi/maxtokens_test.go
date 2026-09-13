package httpapi

import (
	"testing"

	"github.com/trick77/peeq/internal/llm"
)

// Every cap this package pins has to sit inside what the model can emit. Above
// it the endpoint does not reject the request, it ignores the excess — so the
// only place a cap outgrowing the model shows up is here.
func TestMaxTokenCapsFitTheModel(t *testing.T) {
	limit := llm.MaxOutputTokens()
	for name, n := range map[string]int{
		"answerMaxTokens":     answerMaxTokens,
		"understandMaxTokens": understandMaxTokens,
	} {
		if n <= 0 || n > limit {
			t.Errorf("%s = %d, want within (0, %d]", name, n, limit)
		}
	}
}
