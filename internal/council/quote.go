package council

// Misquoted changes (plan 11.20). A change names the text it replaces, and on
// e75c7c3e 6 of 21 edits named text that was not there: the fault sat at the
// end of a 564-character line, and the synthesizer quoted the whole line,
// copying 560 characters exactly and then writing the tail as code usually
// looks rather than as the read showed it -- seven attempts on one line, a
// trip each (on text that was not even a fault: what the read had there was
// valid). When the member's own last read of the same target shows a long
// start of the quote but not the whole of it, the call is answered in place,
// before it costs a trip, with where the quote stops matching and what the
// read has there, verbatim.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ollama/ollama/api"
)

// targetArgs are the arguments a call names what it acts on by.
var targetArgs = []string{"path", "file_path", "filePath", "file", "filename", "filepath"}

// minQuoted is how much of a quote must match before a mismatch after it is
// a misquote rather than a quote of something else.
const minQuoted = 32

// maxMisquotes bounds how often a member's changes are answered in place;
// past it they go to the client, whose own answer stands.
const maxMisquotes = 4

// failedWrite is how a change the tool refused usually says so: such a
// change did not alter what an earlier read showed.
var failedWrite = []string{"0 times", "not found", "no match", "does not occur", "must occur", "not unique"}

// gutter is the line number a reading tool puts before each line.
var gutter = regexp.MustCompile(`^\s*\d+(?:: ?|\t| *[|│→] ?)`)

func argString(c api.ToolCall, names ...string) string {
	for _, n := range names {
		if v, ok := c.Function.Arguments.Get(n); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// quoted is the text a change says it replaces.
func quoted(c api.ToolCall) string {
	for _, p := range noOpPairs {
		if s := argString(c, p[0]); s != "" {
			return s
		}
	}
	return ""
}

// ungutter drops the line numbers of a read whose every line carries one.
func ungutter(s string) string {
	lines := strings.Split(s, "\n")
	for _, l := range lines {
		if l != "" && !gutter.MatchString(l) {
			return s
		}
	}
	for i, l := range lines {
		lines[i] = gutter.ReplaceAllString(l, "")
	}
	return strings.Join(lines, "\n")
}

// misquote is the answer to a quote that read shows only the start of, or ""
// when read has all of it, too little of it, or the mismatch is at a line's
// end (a read of some lines, not all of them).
func misquote(target, q, read string) (string, int) {
	if strings.Contains(read, q) {
		return "", -1
	}
	// The longest start of q that read has: a start that matches has every
	// shorter start matching too.
	lo, hi := 0, len(q)
	for lo < hi {
		m := (lo + hi + 1) / 2
		if strings.Contains(read, q[:m]) {
			lo = m
		} else {
			hi = m - 1
		}
	}
	for lo > 0 && !utf8.RuneStart(q[lo]) {
		lo--
	}
	if lo < minQuoted || q[lo-1] == '\n' {
		return "", 0
	}
	at := strings.Index(read, q[:lo]) + lo
	if at >= len(read) {
		// The read ends where the quote goes on: it showed only part.
		return "", 0
	}
	has := clip(read[at:], 120)
	if i := strings.IndexByte(has, '\n'); i >= 0 {
		has = has[:i]
	}
	has = "«" + has + "»"
	if has == "«»" {
		has = "the end of the line"
	}
	return fmt.Sprintf("Not sent: the text this call replaces is not in %s as your last read of it shows. Your quote matches it for %d characters, up to «%s»; from there the read has %s where your quote has «%s». Copy the text as the read shows it, without line numbers, and quote only the smallest span around the change that occurs once.",
		target, lo, tailClip(q[:lo], 40), has, clip(q[lo:], 60)), lo
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func tailClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// misquotes are the member's changes answered in place, by call id, each with
// its answer: at most maxMisquotes, in the order it sent them.
func (cfg Config) misquotes(r Role, key string, turns []api.Message) map[string]string {
	if !writes(r) {
		return nil
	}
	out := map[string]string{}
	for t := range turns {
		for _, c := range turns[t].ToolCalls {
			if len(out) >= maxMisquotes {
				return out
			}
			if h := cfg.misquoted(key, turns[:t], c); h != "" {
				out[c.ID] = h
			}
		}
	}
	return out
}

// misquoted is the in-place answer to change c after the member's turns
// before, "" when it goes on.
//
// The member's reads of the same target count, latest first. Its own changes
// to that target since a read are replayed onto it, so a read stays current
// across them: on cf223635 13 of 32 edits missed their text, nearly all right
// after the member's previous edit went through, when the read counted as
// stale. A change it cannot replay (its quote not once in the text), or a
// change of another kind, ends the search.
func (cfg Config) misquoted(key string, before []api.Message, c api.ToolCall) string {
	if local(c) || cfg.readOnly(c) {
		return ""
	}
	q, target := quoted(c), argString(c, targetArgs...)
	if q == "" || target == "" {
		return ""
	}
	best, hint := 0, ""
	// since holds the member's changes to the target made after the call
	// being looked at, latest first.
	var since []api.ToolCall
	// seen checks text as it stands now; false when it has the whole quote.
	seen := func(text string) bool {
		text, ok := replay(text, since)
		if !ok {
			return true
		}
		h, n := misquote(target, q, text)
		if n < 0 {
			return false
		}
		if n > best {
			best, hint = n, h
		}
		return true
	}
	for t := len(before) - 1; t >= 0; t-- {
		for j := len(before[t].ToolCalls) - 1; j >= 0; j-- {
			p := before[t].ToolCalls[j]
			if local(p) || argString(p, targetArgs...) != target {
				continue
			}
			res, _, ok := cfg.result(key, p)
			switch {
			case !ok, !cfg.readOnly(p) && refused(res):
			case cfg.readOnly(p):
				if !seen(ungutter(res)) {
					return ""
				}
			case argString(p, wholeArgs...) != "":
				// A whole write is the text itself.
				if !seen(argString(p, wholeArgs...)) {
					return ""
				}
				return hint
			default:
				if _, _, ok := edited(p); !ok {
					return hint
				}
				since = append(since, p)
			}
		}
	}
	return hint
}

// wholeArgs are the arguments a write names the whole new text by.
var wholeArgs = []string{"content", "contents", "file_text"}

// edited is the old and new text a change names.
func edited(c api.ToolCall) (string, string, bool) {
	for _, p := range noOpPairs {
		o, ok1 := c.Function.Arguments.Get(p[0])
		n, ok2 := c.Function.Arguments.Get(p[1])
		os, isO := o.(string)
		ns, isN := n.(string)
		if ok1 && ok2 && isO && isN && os != "" {
			return os, ns, true
		}
	}
	return "", "", false
}

// replay applies the changes, given latest first, to text in the order they
// were made; false when one's quote is not once in the text as it stood.
func replay(text string, since []api.ToolCall) (string, bool) {
	for i := len(since) - 1; i >= 0; i-- {
		o, n, _ := edited(since[i])
		if strings.Count(text, o) != 1 {
			return "", false
		}
		text = strings.Replace(text, o, n, 1)
	}
	return text, true
}

func refused(res string) bool {
	l := strings.ToLower(res)
	for _, w := range failedWrite {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}
