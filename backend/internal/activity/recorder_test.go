package activity

import "testing"

type countingRecorder struct{ n int }

func (c *countingRecorder) Record(Event) { c.n++ }

func TestRecord_nilRecorderIsANoOp(t *testing.T) {
	Record(nil, Event{Kind: KindDownload})
	c := &countingRecorder{}
	Record(c, Event{Kind: KindDownload})
	if c.n != 1 {
		t.Fatalf("recorded %d events, want 1", c.n)
	}
}
