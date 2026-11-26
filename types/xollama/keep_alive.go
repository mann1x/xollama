package xollama

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// KeepAliveForever is what a model states to stay loaded until it is unloaded
// or evicted. It is stored as "-1", upstream's spelling of the same request.
const KeepAliveForever = "-1"

// ParseKeepAlive reads a model's keep_alive as upstream reads a request's
// (api.Duration): a Go duration ("30m", "1h"), or a number of seconds; any
// negative value is forever. "forever" is accepted as the word for -1. The
// empty string is "not stated" and reports ok false.
//
// This package cannot import api (api imports it), so the rule is restated
// here; TestKeepAliveReadsAsARequestDoes holds the two together.
func ParseKeepAlive(s string) (d time.Duration, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	if strings.EqualFold(s, "forever") {
		return time.Duration(math.MaxInt64), true, nil
	}
	if n, perr := strconv.ParseFloat(s, 64); perr == nil {
		if n < 0 {
			return time.Duration(math.MaxInt64), true, nil
		}
		return time.Duration(n * float64(time.Second)), true, nil
	}
	d, err = time.ParseDuration(s)
	if err != nil {
		return 0, false, fmt.Errorf("xollama config: keep_alive %q is neither a duration (30m, 1h) nor a number of seconds; -1 or forever keeps the model loaded", s)
	}
	if d < 0 {
		d = time.Duration(math.MaxInt64)
	}
	return d, true, nil
}

// KeepAliveDuration is the model's own keep-alive, when it states one.
func (c *Config) KeepAliveDuration() (time.Duration, bool) {
	if c == nil {
		return 0, false
	}
	d, ok, err := ParseKeepAlive(c.KeepAlive)
	if err != nil {
		return 0, false
	}
	return d, ok
}

// NormalizeKeepAlive is the spelling a model stores: "forever" becomes -1,
// anything else as written once it parses.
func NormalizeKeepAlive(s string) (string, error) {
	s = strings.TrimSpace(s)
	if _, _, err := ParseKeepAlive(s); err != nil {
		return "", err
	}
	if strings.EqualFold(s, "forever") {
		return KeepAliveForever, nil
	}
	return s, nil
}
