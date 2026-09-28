package council

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ollama/ollama/api"
)

// History is the conversation as the members read it. The client keeps a
// council turn's forwarded calls as it received them: all the members of a
// step in one assistant message, with whatever the member streamed beside
// them. ab-5 measured what that does to the next turns: every member's calls
// read as one speaker's, the front's wrong theory stayed in view, and one
// 30 KB file was there five times. So, in the turns before the last user
// message:
//   - a message's calls are split by the member that made them, one message
//     per member, headed with its role, and followed by that member's results;
//   - the member's own text beside its calls is dropped: it was a working
//     note, not an answer, and only its calls and their results are facts;
//   - a long result identical to an earlier one is pointed at, not repeated.
//
// The rewrite depends only on the messages, so it is the same on every turn
// and every member's prefix stays shared. Messages that carry no council
// call -- the user's, the answers, a plain model's turns -- pass unchanged.
func History(conv []api.Message) []api.Message {
	out := make([]api.Message, 0, len(conv))
	seen := map[string]string{} // a long result -> where it was first returned
	for i := 0; i < len(conv); i++ {
		m := conv[i]
		if m.Role != "assistant" || !councilCalls(m.ToolCalls) {
			out = append(out, m)
			continue
		}
		// The results that answer this message's calls follow it.
		j := i + 1
		for j < len(conv) && conv[j].Role == "tool" {
			j++
		}
		results := conv[i+1 : j]
		byID := map[string]api.Message{}
		var unmatched []api.Message
		for _, r := range results {
			if r.ToolCallID != "" {
				byID[r.ToolCallID] = r
			} else {
				unmatched = append(unmatched, r)
			}
		}
		var order []string
		groups := map[string][]api.ToolCall{}
		for _, c := range m.ToolCalls {
			key := callMember(c.ID)
			if _, ok := groups[key]; !ok {
				order = append(order, key)
			}
			groups[key] = append(groups[key], c)
		}
		for _, key := range order {
			out = append(out, api.Message{Role: "assistant", Content: header(memberLabel(key) + " · TOOL CALLS"), ToolCalls: groups[key]})
			for _, c := range groups[key] {
				r, ok := byID[c.ID]
				if !ok {
					if len(unmatched) == 0 {
						continue
					}
					r, unmatched = unmatched[0], unmatched[1:]
				}
				out = append(out, pointAtRepeat(r, c, key, seen))
			}
		}
		out = append(out, unmatched...)
		i = j - 1
	}
	return out
}

// repeatAt is the length from which an identical result is pointed at.
const repeatAt = 1000

func pointAtRepeat(r api.Message, c api.ToolCall, key string, seen map[string]string) api.Message {
	if len(r.Content) < repeatAt {
		return r
	}
	where := fmt.Sprintf("%s %s for %s", c.Function.Name, c.Function.Arguments.String(), strings.ToLower(memberLabel(key)))
	if first, ok := seen[r.Content]; ok {
		r.Content = fmt.Sprintf("(the same %d characters as the earlier %s returned: unchanged since)", len(r.Content), first)
		return r
	}
	seen[r.Content] = where
	return r
}

var memberKey = regexp.MustCompile(`^(f|s|d|[rc][0-9]+)(\.[0-9]+)?$`)

// councilCalls reports whether any call carries a council member's id.
func councilCalls(calls []api.ToolCall) bool {
	for _, c := range calls {
		if callMember(c.ID) != "" {
			return true
		}
	}
	return false
}

// callMember is the member key a forwarded call's id carries (ForwardedID),
// or "" for a call no member made.
func callMember(id string) string {
	key, _, ok := strings.Cut(id, ":")
	if !ok || !memberKey.MatchString(key) {
		return ""
	}
	return key
}

// memberLabel names a member key for a header: "RESEARCHER 1, ROUND 2".
func memberLabel(key string) string {
	if key == "" {
		return "THE ASSISTANT"
	}
	base, round, _ := strings.Cut(key, ".")
	var name string
	switch base[0] {
	case 'f':
		name = "SYNTHESIZER, TAKING THE REQUEST"
	case 's':
		name = "SYNTHESIZER"
	case 'd':
		name = "PLANNER, ANSWERING DIRECTLY"
	case 'r':
		name = "RESEARCHER " + base[1:]
	case 'c':
		name = "CRITIC " + base[1:]
	}
	if n, err := strconv.Atoi(round); err == nil {
		name += fmt.Sprintf(", ROUND %d", n)
	}
	return name
}
