package app

import "testing"

func TestWFMediaKind(t *testing.T) {
	cases := []struct {
		mime string
		kind string
		ok   bool
	}{
		{"image/jpeg", "image", true},
		{"image/png", "image", true},
		{"image/HEIC", "image", true}, // lowered by the caller
		{"audio/webm", "audio", true},
		{"audio/mpeg", "audio", true},
		{"application/octet-stream", "", false},
		{"text/plain", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		kind, ok := wfMediaKind(c.mime)
		if kind != c.kind || ok != c.ok {
			t.Errorf("wfMediaKind(%q) = (%q,%v), want (%q,%v)", c.mime, kind, ok, c.kind, c.ok)
		}
	}
}

func TestWFFieldNameOK(t *testing.T) {
	for _, ok := range []string{"id_slip", "id_voice", "x", "a1_b2"} {
		if !wfFieldNameOK(ok) {
			t.Errorf("wfFieldNameOK(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "id slip", "a;b", "drop table", "field\x00", "1234567890123456789012345678901234567890123456789012345678901234567890"} {
		if wfFieldNameOK(bad) {
			t.Errorf("wfFieldNameOK(%q) = true, want false", bad)
		}
	}
}

func TestWFIDNumberFrom(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"NIN: 12345678901", "12345678901"},
		{"My BVN is 01234567890 thanks", "01234567890"},
		{"no number here", ""},
		{"short 12345", ""},
		{"1234567890x12345678901", "12345678901"}, // first 11-digit run wins
		{"", ""},
	}
	for _, c := range cases {
		if got := wfIDNumberFrom(c.raw); got != c.want {
			t.Errorf("wfIDNumberFrom(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello world", 5); got != "hello…" {
		t.Errorf("truncateRunes short = %q, want %q", got, "hello…")
	}
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("truncateRunes no-op = %q, want %q", got, "hello")
	}
	if got := truncateRunes("", 3); got != "" {
		t.Errorf("truncateRunes empty = %q, want empty", got)
	}
	// Multi-byte runes must not be split mid-character.
	long := "ñañañañañañañañañañañaña"
	if got := truncateRunes(long, 4); got != "ñaña…" {
		t.Errorf("truncateRunes unicode = %q, want %q", got, "ñaña…")
	}
}
