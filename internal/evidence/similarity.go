package evidence

import "strings"

// bcneAcceptanceThreshold is the acceptance threshold from the BCNE
// (Bidirectional Character N-gram Envelope) gate. A same-Claim,
// different-Value pair whose char-trigram coverage is >= this threshold
// is treated as a paraphrase (SUPPORTS); pairs below the threshold with
// no negation anchor are treated as genuine contradictions (CONTRADICTS).
const bcneAcceptanceThreshold = 0.6

// charTrigrams returns the lowercased length-3 character substring
// (trigram) term-frequency multiset of s.
//
// Edge cases:
//   - empty string  -> nil
//   - length < 3    -> {lowercased(s): 1}
func charTrigrams(s string) map[string]int {
	if s == "" {
		return nil
	}
	s = strings.ToLower(s)
	if len(s) < 3 {
		return map[string]int{s: 1}
	}
	m := make(map[string]int, len(s)-2)
	for i := 0; i+3 <= len(s); i++ {
		tri := s[i:i+3]
		if strings.ContainsAny(tri, " \t\n\r\f\v") {
			continue
		}
		m[tri]++
	}
	return m
}

// BcneCoverage returns the multiset (term-frequency) Jaccard similarity
// of character trigrams between a and b, in the range [0,1].
//
// Edge cases:
//   - either input empty -> 0.0
//   - identical inputs   -> 1.0
//   - otherwise          -> |intersection| / |union|
func BcneCoverage(a, b string) float64 {
	if a == "" || b == "" {
		return 0.0
	}
	if a == b {
		return 1.0
	}
	fa := charTrigrams(a)
	fb := charTrigrams(b)
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
