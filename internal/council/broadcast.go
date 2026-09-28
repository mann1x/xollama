package council

// The broadcast channel (plans/agentic-council-chat.md 10.6; the owner's
// request 2026-09-27, to be measured and dropped if it does not pay). Members
// that work side by side -- the researchers, or the critics when there are
// several -- may send their mates a terse note: a fix that works, the answer
// to a problem, which part each one takes when the work splits. The risks the
// rules below answer:
//
//   - chatter instead of work: a note is at most maxNoteChars, a member posts
//     at most maxNotes a turn, and the instruction says to post rarely;
//   - blocking: nothing waits on a note. A post is answered at once, and a
//     mate reads notes only before its own next model call;
//   - lost independence and unchecked claims: a note is told to name the
//     evidence it rests on, and the critics still check the findings;
//   - the shared prefix: notes go only into a member's own turns, never into
//     a layer members share, and every member's tool list is the same;
//   - resume: the board and what each member has read are in the Progress.
//
// Notes reach members of the same role only (researchers read researchers).

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
)

// PostTool is the council's broadcast tool, answered in the server.
const PostTool = "council_post"

const (
	maxNoteChars = 600
	maxNotes     = 4
)

// Note is one broadcast: its call's forwarded id, its author and its text.
// Kind is "" for a note, or a verdict -- NoteConfirmed or NoteRefuted -- which
// interrupts the mates generating beside it (preemption, 11.4).
type Note struct {
	ID, From, Text, Kind string
}

// A verdict note says a check showed something works, or does not. The owner's
// ruling (2026-09-28): only test results and verdicts preempt.
const (
	NoteConfirmed = "confirmed"
	NoteRefuted   = "refuted"
)

// maxPreempts bounds how often one member's generation is interrupted.
const maxPreempts = 2

// WithBroadcast adds PostTool to the members' tools. Every member carries
// it, so the shared prefix holds one list; who may call it is decided here.
func WithBroadcast(tools api.Tools) api.Tools {
	if len(tools) == 0 {
		return tools
	}
	for _, t := range tools {
		if t.Function.Name == PostTool {
			return tools
		}
	}
	props := api.NewToolPropertiesMap()
	props.Set("note", api.ToolProperty{Type: api.PropertyType{"string"}, Description: fmt.Sprintf("At most %d characters: what works, what you found and its evidence, or which part you take.", maxNoteChars)})
	props.Set("kind", api.ToolProperty{Type: api.PropertyType{"string"}, Enum: []any{"note", NoteConfirmed, NoteRefuted}, Description: "note, or a verdict you checked: confirmed (it works) or refuted (it does not). A verdict interrupts your mates so they read it at once."})
	t := api.Tool{Type: "function", Function: api.ToolFunction{
		Name:        PostTool,
		Description: "Send a very short note to the council members working beside you. Never wait for an answer. Changes nothing.",
		Parameters:  api.ToolFunctionParameters{Type: "object", Required: []string{"note"}, Properties: props},
		ReadOnly:    true,
	}}
	return append(append(api.Tools{}, tools...), t)
}

// board is one turn's notes and how far each member has read them. It is
// shared by the members RunFrom runs in parallel; sync writes each change
// into the turn's Progress, for its checkpoints.
type board struct {
	mu    sync.Mutex
	notes []Note
	seen  map[string]int
	sync  func(notes []Note, seen map[string]int)
	// generating holds the cancel of each member's model call in flight,
	// and preempted the members a verdict interrupted.
	generating map[string]func()
	preempted  map[string]bool
}

// listen registers the member's model call in flight, so a verdict from a
// mate can interrupt it; the returned func ends that.
func (b *board) listen(key string, cancel func()) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generating == nil {
		b.generating = map[string]func(){}
	}
	b.generating[key] = cancel
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.generating, key)
	}
}

// preempt interrupts every mate of from generating now. Called locked.
func (b *board) preemptLocked(from string) {
	for key, cancel := range b.generating {
		if key != from && roleOf(key) == roleOf(from) {
			if b.preempted == nil {
				b.preempted = map[string]bool{}
			}
			b.preempted[key] = true
			cancel()
		}
	}
}

// wasPreempted reports, once, whether a verdict interrupted the member.
func (b *board) wasPreempted(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.preempted[key]
	delete(b.preempted, key)
	return p
}

func newBoard(p Progress, sync func([]Note, map[string]int)) *board {
	b := &board{notes: append([]Note(nil), p.Notes...), seen: map[string]int{}, sync: sync}
	for k, v := range p.Seen {
		b.seen[k] = v
	}
	return b
}

func (b *board) changed() {
	if b.sync == nil {
		return
	}
	seen := make(map[string]int, len(b.seen))
	for k, v := range b.seen {
		seen[k] = v
	}
	b.sync(append([]Note(nil), b.notes...), seen)
}

// mates reports whether the member has others of its role working beside it.
func (cfg Config) mates(r Role) bool {
	switch r {
	case Researcher:
		return cfg.Researchers > 1
	case Critic:
		return cfg.Critics > 1
	}
	return false
}

func (cfg Config) canPost(r Role) bool {
	return cfg.Broadcast && cfg.board != nil && cfg.mates(r)
}

// roleOf is the role letter a member key starts with (r, c, s, d).
func roleOf(key string) byte {
	if key == "" {
		return 0
	}
	return key[0]
}

// post records the member's notes in a reply it just made, once each.
func (cfg Config) post(r Role, key string, calls []api.ToolCall) {
	if !cfg.canPost(r) {
		return
	}
	b := cfg.board
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := false
	for _, c := range calls {
		if c.Function.Name != PostTool {
			continue
		}
		id := ForwardedID(key, c.ID)
		n := 0
		dup := false
		for _, x := range b.notes {
			if x.From == key {
				n++
			}
			dup = dup || x.ID == id
		}
		text := strings.Join(strings.Fields(stringArg(c, "note")), " ")
		if dup || n >= maxNotes || text == "" {
			continue
		}
		if len(text) > maxNoteChars {
			text = truncate(text, maxNoteChars) + "…"
		}
		kind := stringArg(c, "kind")
		if kind != NoteConfirmed && kind != NoteRefuted {
			kind = ""
		}
		b.notes = append(b.notes, Note{ID: id, From: key, Text: text, Kind: kind})
		changed = true
		if kind != "" {
			b.preemptLocked(key)
		}
	}
	if changed {
		b.changed()
	}
}

// postAnswer is what a PostTool call reads back in the member's transcript.
func (cfg Config) postAnswer(r Role, key string, c api.ToolCall) string {
	if !cfg.canPost(r) {
		return "Refused: no council member works beside you this turn, so there is no one to post to."
	}
	id := ForwardedID(key, c.ID)
	b := cfg.board
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, x := range b.notes {
		if x.ID == id {
			return "Posted. Do not wait for replies; go on with your work."
		}
	}
	return fmt.Sprintf("Not posted: a note must say something, and you may post at most %d a turn. Go on with your work.", maxNotes)
}

// unread takes the notes the member's mates posted since it last read, as one
// message for its own turns, and marks them read. Nil when there are none.
func (cfg Config) unread(r Role, key string) *api.Message {
	if !cfg.canPost(r) {
		return nil
	}
	b := cfg.board
	b.mu.Lock()
	defer b.mu.Unlock()
	from := b.seen[key]
	var lines []string
	for _, x := range b.notes[min(from, len(b.notes)):] {
		if x.From != key && roleOf(x.From) == roleOf(key) {
			if x.Kind != "" {
				lines = append(lines, fmt.Sprintf("- %s (%s, checked): %s", x.From, strings.ToUpper(x.Kind), x.Text))
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", x.From, x.Text))
		}
	}
	if b.seen[key] == len(b.notes) && len(lines) == 0 {
		return nil
	}
	b.seen[key] = len(b.notes)
	b.changed()
	if len(lines) == 0 {
		return nil
	}
	m := user("NOTES FROM THE MEMBERS WORKING BESIDE YOU (for your information; do not reply, do not wait):\n" + strings.Join(lines, "\n"))
	return &m
}

// postNote is the instruction a member with mates gets about the channel.
func (cfg Config) postNote(r Role) string {
	if !cfg.canPost(r) {
		return ""
	}
	return fmt.Sprintf(" You may call %s to tell the members working beside you, in at most %d characters, something that saves them work: a fix that works, the answer to a problem, or which part of the work you take when it splits (keep your brief's part unless you agree otherwise). Post rarely -- at most %d notes -- name the evidence, never wait for replies, and never post to chat. Give kind confirmed or refuted only for a result you checked: it interrupts your mates, so they stop pursuing what it settles.", PostTool, maxNoteChars, maxNotes)
}
