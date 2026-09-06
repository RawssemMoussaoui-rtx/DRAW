package evidence

import "strings"

// contradictionSimilarityThreshold is the accepted similarity threshold from the
// Stage 2 acceptance-gate protocol. Selected from the fixed grid {0.5, 0.65, 0.8}:
// tau=0.8 is the strictest candidate that still suppresses the Scenario E
// paraphrase pair (overlap >= tau -> no edge) while preserving the Scenario C
// true-contradiction pair (overlap < tau -> CONTRADICTS emitted). See
// PHASE_J_SPEC.md §2.6 (paraphrase) and §2.3 (contradiction).
//
// This is a pure abstention gate: a high-similarity same-Claim/different-Value
// pair emits no edge at all (not a new relation kind). It does NOT redefine
// SUPPORTS/DUPLICATES/CONTRADICTS semantics, does NOT change the enum, and does
// NOT touch migrations.
const contradictionSimilarityThreshold = 0.8

// ValueSimilarity returns a deterministic n-gram (n in {1,2,3}) term-frequency
// (multiset) Jaccard similarity between two strings, in the range [0,1].
//
// Tokenization lowercases the input and splits on Unicode whitespace, then
// strips common surrounding punctuation from each token. For each n the n-gram
// term-frequency multiset Jaccard is computed; the returned value is the maximum
// across n=1,2,3.
//
// Edge cases:
//   - identical non-empty strings -> 1.0
//   - disjoint token sets -> 0.0
//   - either input empty/whitespace-only -> 0.0
//
// No external dependencies; pure standard library so it can be imported by
// Stage 3B (J1) without pulling in NLP/NER machinery.
func ValueSimilarity(a, b string) float64 {
	ta := tokenize(a)
	tb := tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0.0
	}
	best := 0.0
	for _, n := range []int{1, 2, 3} {
		j := ngramTFJaccard(ta, tb, n)
		if j > best {
			best = j
		}
	}
	return best
}

func tokenize(s string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		t := strings.Trim(f, ".,;:!?\"'()'[]{}")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ngramTFJaccard computes the multiset (term-frequency) Jaccard similarity
// between the n-gram term sets of two token slices.
//
// intersection = sum over shared n-grams of min(countA, countB)
// union        = sum(countsA) + sum(countsB) - intersection
// Jaccard      = intersection / union   (0.0 when union == 0)
func ngramTFJaccard(a, b []string, n int) float64 {
	fa := ngramTF(a, n)
	fb := ngramTF(b, n)
	if len(fa) == 0 && len(fb) == 0 {
		return 0.0
	}
	var inter int
	for k, va := range fa {
		if vb, ok := fb[k]; ok {
			if va < vb {
				inter += va
			} else {
				inter += vb
			}
		}
	}
	union := 0
	for _, v := range fa {
		union += v
	}
	for _, v := range fb {
		union += v
	}
	union -= inter
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

// ngramTF builds a term-frequency map of width-n token n-grams.
func ngramTF(tokens []string, n int) map[string]int {
	if n <= 0 || len(tokens) < n {
		return nil
	}
	m := make(map[string]int, len(tokens)-n+1)
	for i := 0; i+n <= len(tokens); i++ {
		m[strings.Join(tokens[i:i+n], " ")]++
	}
	return m
}
