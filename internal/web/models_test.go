package web

import "testing"

func TestFormatBytesAndAge(t *testing.T) {
	if got := FormatBytes(512); got != "512 B" {
		t.Fatalf("got %s", got)
	}
	if got := FormatBytes(1536); got != "1.5 KiB" {
		t.Fatalf("got %s", got)
	}
	if got := FormatAge(0); got != "0s" {
		t.Fatalf("got %s", got)
	}
	if got := ShortHash("short"); got != "short" {
		t.Fatalf("got %s", got)
	}
	if got := ShortHash("0123456789abcdef0123"); got != "0123456789ab…" {
		t.Fatalf("got %s", got)
	}
}
