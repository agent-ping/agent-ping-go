package agentping

import (
	"regexp"
	"strings"
	"testing"
)

func TestUUID7HexLength(t *testing.T) {
	for i := 0; i < 100; i++ {
		h := uuid7Hex()
		if len(h) != 32 {
			t.Fatalf("expected 32 hex chars, got %d (%q)", len(h), h)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(h) {
			t.Fatalf("not lowercase hex: %q", h)
		}
		// Version nibble at position 12 should be 7.
		if h[12] != '7' {
			t.Fatalf("expected version 7, got %c", h[12])
		}
		// Variant top bits in byte 8 (hex offset 16) should be 8, 9, a or b.
		switch h[16] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("invalid variant: %c", h[16])
		}
	}
}

func TestUUID7Monotonic(t *testing.T) {
	prev := uuid7Hex()
	for i := 0; i < 200; i++ {
		cur := uuid7Hex()
		if cur <= prev {
			t.Fatalf("uuid7 not monotonic: prev=%s cur=%s", prev, cur)
		}
		prev = cur
	}
}

func TestExtractRegion(t *testing.T) {
	cases := map[string]string{
		"":        "eu",
		"garbage": "eu",
		"apk_eu_00000000000000000000000000000000": "eu",
		"apk_us_00000000000000000000000000000000": "us",
		"apk_ZZ_00000000000000000000000000000000": "eu", // uppercase region rejected
	}
	for k, want := range cases {
		got := extractRegion(k)
		if got != want {
			t.Errorf("extractRegion(%q)=%q want %q", k, got, want)
		}
	}
}

func TestNewIDFormat(t *testing.T) {
	id := newID("run", "us")
	if !strings.HasPrefix(id, "run_us_") {
		t.Fatalf("prefix/region mismatch: %s", id)
	}
	if !isValidID(id) {
		t.Fatalf("isValidID rejected %s", id)
	}
}

func TestIsValidID(t *testing.T) {
	good := []string{
		"run_eu_00000000000000000000000000000000",
		"evt_us_abcdef0123456789abcdef0123456789",
	}
	bad := []string{
		"",
		"run_us_short",
		"RUN_us_00000000000000000000000000000000",
		"run_USA_00000000000000000000000000000000",
		"run_us_0000000000000000000000000000000Z",
	}
	for _, v := range good {
		if !isValidID(v) {
			t.Errorf("expected valid: %s", v)
		}
	}
	for _, v := range bad {
		if isValidID(v) {
			t.Errorf("expected invalid: %s", v)
		}
	}
}
