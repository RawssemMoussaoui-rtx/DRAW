package evidence

import "strings"

// AnchorType classifies a linguistic anchor detected in evidence text.
type AnchorType string

const (
	AnchorNone     AnchorType = ""
	AnchorNegation AnchorType = "NEGATION"
)

// negationTokens is the set of English negation tokens recognised by
// HasNegationAnchor. Tokens are matched case-insensitively at the
// whole-word level (surrounding punctuation is stripped before matching).
var negationTokens = []string{
	"not", "no", "never", "none", "nothing", "neither", "nor",
	"cannot", "cant", "without", "hardly", "barely", "scarcely",
	"dont", "doesnt", "didnt", "isnt", "wasnt", "arent", "arent",
	"werent", "wont", "wouldnt", "shouldnt", "couldnt", "aint",
}

// stripPunct is the set of characters trimmed from each token before
// whole-word negation matching.
const stripPunct = ".,;:!?\"'()[]{}"

// HasNegationAnchor reports whether s contains a negation token at the
// whole-word level (case-insensitive, after stripping surrounding
// punctuation from each whitespace-delimited token).
func HasNegationAnchor(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		t := strings.Trim(f, stripPunct)
		t = strings.ReplaceAll(t, "'", "")
		if t == "" {
			continue
		}
		for _, neg := range negationTokens {
			if t == neg {
				return true
			}
		}
	}
	return false
}

// AnchorSignature records a linguistic anchor detected in evidence text.
type AnchorSignature struct {
	Type  AnchorType
	Match string
}

// ExtractAnchorSignatures returns the anchor signatures for text.
// Currently the only anchor recognised is NEGATION; if a negation token
// is present a single AnchorNegation signature is returned, otherwise
// nil.
func ExtractAnchorSignatures(text string) []AnchorSignature {
	if HasNegationAnchor(text) {
		return []AnchorSignature{{Type: AnchorNegation, Match: "negation"}}
	}
	return nil
}
