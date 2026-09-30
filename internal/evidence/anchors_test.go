package evidence

import "testing"

func TestHasNegationAnchor_Basic(t *testing.T) {
	for _, text := range []string{"not", "no", "never", "none", "nothing"} {
		if !HasNegationAnchor(text) {
			t.Errorf("HasNegationAnchor(%q) = false; want true", text)
		}
	}
}

func TestHasNegationAnchor_CaseInsensitive(t *testing.T) {
	for _, text := range []string{"NOT", "No", "NEVER", "not"} {
		if !HasNegationAnchor(text) {
			t.Errorf("HasNegationAnchor(%q) = false; want true (case-insensitive)", text)
		}
	}
}

func TestHasNegationAnchor_PunctuationStripped(t *testing.T) {
	for _, text := range []string{"isn't", "don't", "no.", "not,", "never!"} {
		if !HasNegationAnchor(text) {
			t.Errorf("HasNegationAnchor(%q) = false; want true (punctuation stripped)", text)
		}
	}
}

func TestHasNegationAnchor_NoNegation(t *testing.T) {
	for _, text := range []string{"the capital of France is Paris", "hello world", "acquitted"} {
		if HasNegationAnchor(text) {
			t.Errorf("HasNegationAnchor(%q) = true; want false", text)
		}
	}
}

func TestExtractAnchorSignatures_WithNegation(t *testing.T) {
	sigs := ExtractAnchorSignatures("The defendant is not guilty")
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	if sigs[0].Type != AnchorNegation {
		t.Errorf("expected AnchorNegation, got %s", sigs[0].Type)
	}
}

func TestExtractAnchorSignatures_NoNegation(t *testing.T) {
	sigs := ExtractAnchorSignatures("The defendant is guilty")
	if len(sigs) != 0 {
		t.Fatalf("expected 0 signatures, got %d", len(sigs))
	}
}
