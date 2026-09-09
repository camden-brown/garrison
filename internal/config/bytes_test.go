package config_test

import (
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/config"
)

// The config file is hand-edited, so "4GiB" has to work. Requiring
// 4294967296 would be unambiguous and would guarantee that a typo eventually
// caps a server at four megabytes.
func TestParseBytes(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"1024", 1024},
		{"4GiB", 4 << 30},
		{"4 GiB", 4 << 30},
		{"512MiB", 512 << 20},
		{"2TiB", 2 << 40},
		{"8g", 8 << 30},
		{"1.5GiB", 1610612736},
		// GB and GiB are different numbers, as everywhere else.
		{"1GB", 1_000_000_000},
		{"1GiB", 1 << 30},
		{"100B", 100},
	}

	for _, tt := range tests {
		got, err := config.ParseBytes(tt.in)
		if err != nil {
			t.Errorf("ParseBytes(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseBytes(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// A size that cannot be read must say so rather than silently becoming zero,
// which is "unlimited" and the opposite of what somebody typing a cap meant.
func TestParseBytesRejectsNonsense(t *testing.T) {
	for _, in := range []string{"lots", "-4GiB", "4 gigabytes", "GiB", "4.5.6GiB"} {
		if got, err := config.ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) = %d, want an error", in, got)
		} else if !strings.Contains(err.Error(), in) {
			t.Errorf("error for %q does not quote the input: %v", in, err)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, ""},
		{-1, ""},
		{4 << 30, "4GiB"},
		{512 << 20, "512MiB"},
		{2 << 40, "2TiB"},
		{1536, "1536"}, // not a round unit, so left as bytes
	}

	for _, tt := range tests {
		if got := config.FormatBytes(tt.in); got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBytesRoundTrip(t *testing.T) {
	for _, n := range []int64{0, 1 << 10, 4 << 30, 512 << 20, 2 << 40} {
		text := config.FormatBytes(n)
		back, err := config.ParseBytes(text)
		if err != nil {
			t.Errorf("ParseBytes(FormatBytes(%d)=%q) error = %v", n, text, err)
			continue
		}
		if back != n {
			t.Errorf("%d formatted as %q read back as %d", n, text, back)
		}
	}
}
