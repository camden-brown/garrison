package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBytes reads a memory size the way a person writes one.
//
// The config file is meant to be edited by hand, and "4GiB" is what somebody
// writes when they mean four gibibytes. Requiring 4294967296 would be
// technically unambiguous and would guarantee a typo eventually caps a server
// at four megabytes.
//
// Both conventions are accepted and they mean different things: GiB is 1024³
// and GB is 1000³, as everywhere else. A bare number is bytes.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	// Longest suffixes first, or "GB" matches inside "GiB".
	units := []struct {
		suffix string
		scale  int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
		{"B", 1},
	}

	upper := strings.ToUpper(s)
	for _, u := range units {
		if !strings.HasSuffix(upper, strings.ToUpper(u.suffix)) {
			continue
		}
		digits := strings.TrimSpace(s[:len(s)-len(u.suffix)])
		v, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a size: %q is not a number", s, digits)
		}
		if v < 0 {
			return 0, fmt.Errorf("%q is not a size: it is negative", s)
		}
		return int64(v * float64(u.scale)), nil
	}

	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size: expected something like 4GiB", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("%q is not a size: it is negative", s)
	}
	return v, nil
}

// FormatBytes writes a size back the way it was likely typed. Zero is empty,
// because an omitted limit and a limit of zero mean the same thing here and
// writing "0B" into a file a person reads is noise.
func FormatBytes(n int64) string {
	if n <= 0 {
		return ""
	}

	units := []struct {
		suffix string
		scale  int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	}
	for _, u := range units {
		if n >= u.scale && n%u.scale == 0 {
			return strconv.FormatInt(n/u.scale, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10)
}
