package evalharness

import (
	"testing"
)

// TestDeterminismN5 proves R6 (N>=5) at the full-system level via M7.
//
// CONSTRUCT: For each scenario A-E, RunV1OnScenario is executed N=5 times,
// each with a fresh in-memory store. determinismSignature (M7) is computed
// on every run. All five signatures must be byte-identical.
//
// On the first variance the test fails fast, reporting the scenario, the
// failing run number, and the reason (signature mismatch).
func TestDeterminismN5(t *testing.T) {
	for _, s := range allScenarios() {
		s := s
		t.Run(string(s.ID), func(t *testing.T) {
			const N = 5
			signatures := make([]string, 0, N)
			for i := 0; i < N; i++ {
				st := setupTestStore(t)
				state := RunV1OnScenario(t, s, &st)
				signatures = append(signatures, determinismSignature(state))
			}

			first := signatures[0]
			t.Logf("Scenario %s: %d runs; signature length=%d bytes", s.ID, N, len(first))

			for i := 1; i < N; i++ {
				if signatures[i] != first {
					t.Fatalf("Scenario %s: determinism variance detected at run #%d "+
						"(reason: M7 signature differs from run #1)\n"+
						"run #1 sig: %q\n"+
						"run #%d sig: %q",
						s.ID, i+1, first, i+1, signatures[i])
				}
			}
			t.Logf("Scenario %s: PASS (all %d signatures byte-identical)", s.ID, N)
		})
	}
}
