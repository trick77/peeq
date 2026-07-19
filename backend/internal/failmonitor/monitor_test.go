package failmonitor

import (
	"sync"
	"testing"
)

func TestEngagesOnDistinctThreshold(t *testing.T) {
	engaged := 0
	m := New(3, func() { engaged++ })
	m.Fail("a")
	m.Fail("a") // duplicate — must not count
	m.Fail("b")
	if engaged != 0 {
		t.Fatalf("engaged=%d after 2 distinct, want 0", engaged)
	}
	m.Fail("c") // third distinct → engage
	if engaged != 1 {
		t.Fatalf("engaged=%d after 3 distinct, want 1", engaged)
	}
	m.Fail("d") // stays engaged, no re-fire
	if engaged != 1 {
		t.Fatalf("engaged=%d, want 1 (fires once until reset)", engaged)
	}
}

func TestResetClears(t *testing.T) {
	engaged := 0
	m := New(2, func() { engaged++ })
	m.Fail("a"); m.Fail("b") // engage (1)
	m.Reset()
	m.Fail("a"); m.Fail("b") // engage again (2)
	if engaged != 2 {
		t.Fatalf("engaged=%d, want 2", engaged)
	}
}

func TestConcurrentFailReset(t *testing.T) {
	m := New(1000, func() {})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); m.Fail(string(rune('a' + n%26))) }(i)
		go func() { defer wg.Done(); m.Reset() }()
	}
	wg.Wait() // -race must see no data race
}
