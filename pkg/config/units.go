package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ByteLimitMode distinguishes the three states of a byte-threshold setting.
type ByteLimitMode int

const (
	ByteLimitValue ByteLimitMode = iota // use Bytes
	ByteLimitAuto                       // decided at run time (confirm_bytes only)
	ByteLimitOff                        // disabled
)

// ByteLimit is a byte threshold that can be automatic or off.
type ByteLimit struct {
	Mode  ByteLimitMode
	Bytes int64
}

func (b ByteLimit) String() string {
	switch b.Mode {
	case ByteLimitAuto:
		return "auto"
	case ByteLimitOff:
		return "off"
	}
	return FormatBytes(b.Bytes)
}

// ParseBytes parses "10GiB", "1.5 TB", "1024" or 1024 into bytes.
func ParseBytes(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		if x < 0 {
			return 0, fmt.Errorf("must not be negative")
		}
		return x, nil
	case int:
		return ParseBytes(int64(x))
	case float64:
		return ParseBytes(strconv.FormatFloat(x, 'f', -1, 64))
	case string:
		return parseByteString(x)
	}
	return 0, fmt.Errorf("expected a size such as \"10GiB\", got %T", v)
}

var byteUnits = map[string]float64{
	"": 1, "b": 1,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

func parseByteString(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, unit := strings.TrimSpace(s[:i]), strings.ToLower(strings.TrimSpace(s[i:]))
	if num == "" {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	mult, ok := byteUnits[unit]
	if !ok {
		return 0, fmt.Errorf("unknown size unit %q in %q (use B, KB, MB, GB, TB, KiB, MiB, GiB, TiB)", unit, s)
	}
	n := f * mult
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(n), nil
}

// FormatBytes renders a byte count with binary units.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d%s", int64(v), suffix)
	}
	return fmt.Sprintf("%.1f%s", v, suffix)
}

// ParseDuration is time.ParseDuration plus a "d" (day) suffix.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use values like 30s, 15m, 24h, 2d)", s)
	}
	return d, nil
}
