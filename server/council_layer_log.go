package server

// xollama: what each council pool layer holds, in the debug log -- part of
// the `council` hook. A layer is shared only by members whose stage renders
// byte for byte the same; two layers forking one parent are members that
// could have shared the part before they diverge. On hard (569747788) pools
// 13 and 14 were built 7 s apart on one parent, both 6724 long.

import (
	"context"
	"log/slog"
	"strings"
)

// layerDivergence is how far two texts agree, and each one's next few
// characters there.
func layerDivergence(a, b string) (common int, aNext, bNext string) {
	n := min(len(a), len(b))
	for common < n && a[common] == b[common] {
		common++
	}
	next := func(s string) string { return s[common:min(len(s), common+80)] }
	return common, next(a), next(b)
}

// logLayerText logs the part of l past its parent and, for every sibling
// built on the same parent, where the two part.
func (t *councilTree) logLayerText(l *councilLayer) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	base := 0
	if l.parent != nil {
		base = len(l.parent.text)
	}
	slog.Debug("council: pool text", "pool", l.id, "text", l.text[base:])
	t.mu.Lock()
	var sibs []*councilLayer
	for _, o := range t.order {
		if o != l && o.parent == l.parent && o.err == nil {
			sibs = append(sibs, o)
		}
	}
	t.mu.Unlock()
	for _, o := range sibs {
		common, mine, theirs := layerDivergence(l.text, o.text)
		if common <= base {
			continue
		}
		slog.Debug("council: pool shares a prefix with a sibling", "pool", l.id, "sibling", o.id,
			"parent_chars", base, "common_chars", common, "own_chars", len(l.text)-common,
			"here", strings.TrimSpace(mine), "there", strings.TrimSpace(theirs))
	}
}
