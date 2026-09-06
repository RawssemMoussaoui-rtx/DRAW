package frontier

import (
	"math"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

// TestSaturateContradiction isolates saturateContradiction (P8) from all
// concurrency, frontier state, and storage. It has no dependency on the
// scheduler or any I/O.
func TestSaturateContradiction(t *testing.T) {
	const alpha, scale = 2.0, 1.0

	t.Run("bounded_by_scale", func(t *testing.T) {
		for x := -100.0; x <= 100.0; x += 0.25 {
			got := saturateContradiction(x, alpha, scale)
			if got > scale+1e-12 || got < -scale-1e-12 {
				t.Errorf("saturateContradiction(%v) = %v, want within [-%v, %v]", x, got, scale, scale)
			}
		}
	})

	t.Run("odd_symmetry", func(t *testing.T) {
		for x := 0.0; x <= 10.0; x += 0.5 {
			pos := saturateContradiction(x, alpha, scale)
			neg := saturateContradiction(-x, alpha, scale)
			if math.Abs(pos+neg) > 1e-12 {
				t.Errorf("f(-x) != -f(x) for x=%v: f(x)=%v f(-x)=%v", x, pos, neg)
			}
		}
	})

	t.Run("saturates_for_large_inputs", func(t *testing.T) {
		small := saturateContradiction(5.0, alpha, scale)
		large := saturateContradiction(50.0, alpha, scale)
		// Both must be asymptotic to +scale (tanh saturates); allow float slack.
		if math.Abs(small-large) > 1e-6 {
			t.Errorf("large inputs must saturate: f(5)=%v f(50)=%v, want ~equal", small, large)
		}
		if math.Abs(large-scale) > 1e-6 {
			t.Errorf("f(50)=%v, want ~%v (asymptotic to +scale)", large, scale)
		}
	})

	t.Run("near_linear_for_small_inputs", func(t *testing.T) {
		// For small z, tanh(z) ~= z, so f(x) ~= alpha*x. Verify relative error
		// stays small in the linear region |alpha*x| < 0.2.
		const maxRelErr = 0.02 // <2% deviation from the linear approximation
		for x := 0.0; x <= 0.1; x += 0.01 {
			got := saturateContradiction(x, alpha, scale)
			lin := alpha * x
			if lin == 0 {
				continue
			}
			rel := math.Abs(got-lin) / lin
			if rel > maxRelErr {
				t.Errorf("non-linear near 0: f(%v)=%v, linear=%v, relErr=%v > %v", x, got, lin, rel, maxRelErr)
			}
		}
	})

	t.Run("zero_alpha_zero_scale", func(t *testing.T) {
		if got := saturateContradiction(3.0, 0, 0); got != 0 {
			t.Errorf("alpha=0,scale=0 => %v, want 0", got)
		}
	})

	t.Run("scale_scales_output", func(t *testing.T) {
		x := 0.7
		f1 := saturateContradiction(x, alpha, 1.0)
		f3 := saturateContradiction(x, alpha, 3.0)
		// scale multiplies the output linearly.
		if math.Abs(f3-3.0*f1) > 1e-12 {
			t.Errorf("scale not linear: f(scale=3)=%v, 3*f(scale=1)=%v", f3, 3.0*f1)
		}
		// And the saturated term is bounded by |scale|.
		if f3 > 3.0+1e-12 {
			t.Errorf("f(scale=3)=%v must be <= 3", f3)
		}
	})
}

// TestScoreUsesSaturatedContradiction confirms the saturated contradiction term
// is wired into scorer.score: a candidate whose source quality is unknown
// (qualityFor baseline 0.3 -> signal 0.7) must score higher than an identical
// candidate against a trusted source (quality 1.0 -> signal 0.0), all other
// weights equal.
func TestScoreUsesSaturatedContradiction(t *testing.T) {
	w := config.DefaultScoringWeights()
	// Zero SourceValue so the only quality-dependent term is the (saturated)
	// contradiction contribution; this isolates the P8 wiring under test.
	w.SourceValue = 0
	s := scorer{
		weights:  w,
		costs:    map[model.TaskType]int{model.TaskTypeFetchHTTP: 1},
		maxCost:  1,
	}

	poor := URLCandidate{URL: mustParse(t, "https://poor.example/x"), Domain: "poor.example", PriorityHint: 500}
	trusted := URLCandidate{URL: mustParse(t, "https://trusted.example/x"), Domain: "trusted.example", PriorityHint: 500}

	src := fakeRegistry{profiles: map[string]model.SourceProfile{
		"trusted.example": {Domain: "trusted.example", QualityScore: 1.0},
	}}
	// "poor.example" is absent -> qualityFor returns 0.3 baseline -> signal 0.7.

	badSource := s.score(poor, src)
	goodSource := s.score(trusted, src)
	diff := w.ContradictionValue * saturateContradiction(0.7, w.ContradictionAlpha, w.ContradictionScale)

	if goodSource >= badSource {
		t.Errorf("poor-source candidate must score higher than trusted source: good=%v poor=%v", goodSource, badSource)
	}
	if math.Abs((badSource-goodSource)-diff) > 1e-9 {
		t.Errorf("score delta = %v, expected exactly the saturated contradiction contribution %v", badSource-goodSource, diff)
	}
}
