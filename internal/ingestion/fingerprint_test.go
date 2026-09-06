package ingestion

import (
	"fmt"
	"sync"
	"testing"
)

func TestSeenIdenticalBytesReturnsFalseThenTrue(t *testing.T) {
	fp := NewFingerprinter()
	data := []byte("identical-body")
	if got := fp.Seen(data); got {
		t.Fatalf("first Seen of identical data: got %v, want false", got)
	}
	if got := fp.Seen(data); !got {
		t.Fatalf("second Seen of identical data: got %v, want true", got)
	}
}

func TestSeenDistinctBytesEachReturnFalse(t *testing.T) {
	fp := NewFingerprinter()
	cases := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
	}
	for i, data := range cases {
		if got := fp.Seen(data); got {
			t.Fatalf("distinct data #%d: got %v, want false", i, got)
		}
	}
}

func TestResetClearsState(t *testing.T) {
	fp := NewFingerprinter()
	data := []byte("to-be-reset")
	_ = fp.Seen(data)
	if !fp.Seen(data) {
		t.Fatal("expected data to be seen before Reset")
	}
	fp.Reset()
	if fp.Seen(data) {
		t.Fatal("after Reset, previously-seen data should return false again")
	}
}

func TestSeenEmptyDataFingerprintsNormally(t *testing.T) {
	// Empty data has a well-defined SHA-256 digest and is fingerprinted
	// without special-casing.
	fp := NewFingerprinter()
	if got := fp.Seen(nil); got {
		t.Fatalf("first Seen of empty data: got %v, want false", got)
	}
	if got := fp.Seen([]byte("")); !got {
		t.Fatalf("second Seen of empty data: got %v, want true", got)
	}
}

func TestConcurrentSafe(t *testing.T) {
	fp := NewFingerprinter()
	const goroutines = 64
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				data := []byte(fmt.Sprintf("g%d-i%d", g, i))
				fp.Seen(data)
			}
		}(g)
	}
	wg.Wait()

	// All unique entries written concurrently must be recorded.
	for g := 0; g < goroutines; g++ {
		for i := 0; i < iterations; i++ {
			data := []byte(fmt.Sprintf("g%d-i%d", g, i))
			if !fp.Seen(data) {
				t.Fatalf("data not seen after concurrent writes: g=%d i=%d", g, i)
			}
		}
	}
}

func TestDeterminismAcrossInstances(t *testing.T) {
	data := []byte("deterministic-content")
	a := NewFingerprinter()
	b := NewFingerprinter()

	// Independent instances must yield consistent membership semantics.
	// Capture each call's result so a comparison does not itself mutate state.
	aFirst, bFirst := a.Seen(data), b.Seen(data)
	if aFirst != bFirst {
		t.Fatalf("first Seen diverged: a=%v b=%v", aFirst, bFirst)
	}
	if aFirst {
		t.Fatalf("expected first Seen to be false, got a=%v", aFirst)
	}
	aSecond, bSecond := a.Seen(data), b.Seen(data)
	if aSecond != bSecond {
		t.Fatalf("second Seen diverged: a=%v b=%v", aSecond, bSecond)
	}
	if !aSecond {
		t.Fatalf("expected second Seen to be true, got a=%v", aSecond)
	}
}
