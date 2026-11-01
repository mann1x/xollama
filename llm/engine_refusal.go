package llm

import "strings"

// engineRefusalLine is a line in which opencoti refuses to serve in words of
// its own, from "host commit" on, or "". Since c14 a Windows engine that loads
// with less than its margin of the commit limit left exits rather than die on
// the first request (0814, bug-4282). The line carries no error prefix, so
// without this the client read only that the runner had terminated. The same
// words open a line on every healthy boot, so the match is on the refusal's
// own, never on "host commit".
func engineRefusalLine(line string) string {
	if !strings.Contains(line, "of the system's commit limit is left") {
		return ""
	}
	if i := strings.Index(line, "host commit ["); i >= 0 {
		return line[i:]
	}
	return ""
}
