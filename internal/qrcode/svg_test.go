package qrcode

import (
	"strings"
	"testing"
)

func TestSVGIsCompactSelfContainedAndDeterministic(t *testing.T) {
	content := "agentcontinuity://invite?token=qri_0123456789abcdefghijklmnopqrstuvwxyzABCDE"
	first, err := SVG(content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SVG(content)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("QR renderer is not deterministic")
	}
	for _, expected := range []string{`<svg xmlns="http://www.w3.org/2000/svg"`, `viewBox="0 0 `, `<rect`, `<path`} {
		if !strings.Contains(first, expected) {
			t.Fatalf("SVG is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"<script", "<image", "<use", "javascript:", "data:"} {
		if strings.Contains(strings.ToLower(first), forbidden) {
			t.Fatalf("SVG contains unsafe external content %q", forbidden)
		}
	}
	if len(first) > 12<<10 {
		t.Fatalf("SVG unexpectedly large: %d bytes", len(first))
	}
}

func TestSVGRejectsInvalidContent(t *testing.T) {
	if _, err := SVG(""); err == nil {
		t.Fatal("expected empty content to fail")
	}
	if _, err := SVG(strings.Repeat("x", maxContentBytes+1)); err == nil {
		t.Fatal("expected oversized content to fail")
	}
}
