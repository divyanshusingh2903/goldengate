package sync

import "testing"

func TestBatchChunks(t *testing.T) {
	b := Batch{items: make([]planned, 25), refresh: []Entry{{SessionID: "r"}}}
	cs := b.Chunks(10)
	if len(cs) != 3 || cs[0].Len() != 10 || cs[1].Len() != 10 || cs[2].Len() != 5 {
		t.Fatalf("25 in tens: %d parts", len(cs))
	}
	if len(cs[0].refresh) != 1 || len(cs[1].refresh)+len(cs[2].refresh) != 0 {
		t.Error("refreshes ride with the first part only")
	}
	if one := b.Chunks(0); len(one) != 1 || one[0].Len() != 25 {
		t.Error("n <= 0 keeps the batch whole")
	}
	if small := (Batch{items: make([]planned, 3)}).Chunks(10); len(small) != 1 {
		t.Error("a small batch stays one part")
	}
}
