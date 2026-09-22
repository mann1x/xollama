package tweak

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ollama/ollama/types/xollama"
)

// errQuit is what a question returns when the operator asks to stop. It is not
// an error condition -- nothing is written and nothing is wrong -- so the
// command turns it into a clean exit.
var errQuit = errors.New("tweak: cancelled")

// asker is the whole terminal surface of this command, kept behind an
// interface so the tests drive the real wizard rather than a reimplementation
// of it: every prompt string a person sees is a prompt string a test sees.
//
// THE PROMPT FORMAT IS PART OF THE CONTRACT. Each question ends with a line
//
//	<field-name> [<default>]>
//
// with no trailing newline, so an expect script can wait on a fixed, unique
// token per question and reply with either the menu number or the value word.
// Changing that shape breaks scripts, not just tests.
type asker struct {
	in  *bufio.Reader
	out io.Writer
	// answered counts questions actually put to the operator, so a run driven
	// entirely by flags knows it asked nothing and can skip the confirmation.
	answered int
}

func newAsker(in io.Reader, out io.Writer) *asker {
	return &asker{in: bufio.NewReader(in), out: out}
}

func (a *asker) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

// readLine returns the next line, or errQuit on EOF. EOF means the driving
// script ran out of answers, and continuing with defaults would write a
// configuration nobody chose.
func (a *asker) readLine() (string, error) {
	line, err := a.in.ReadString('\n')
	if err != nil && line == "" {
		return "", errQuit
	}
	return strings.TrimSpace(line), nil
}

// ask puts one field to the operator and applies the answer to cfg.
//
// It loops until the answer is both parseable and legal: an answer that would
// make the config invalid is rejected with the reason and asked again, so the
// consistency check happens where the operator can still act on it rather than
// at the end, where it would mean discarding work.
func (a *asker) ask(cfg *xollama.Config, f field) error {
	for {
		current := f.get(cfg)
		options := a.render(cfg, f, current)

		def := current
		if def == "" {
			def = "unset"
		}
		a.printf("\n%s [%s]> ", f.name, def)

		raw, err := a.readLine()
		if err != nil {
			return err
		}
		a.answered++

		switch strings.ToLower(raw) {
		case "":
			// Blank keeps what is there, which is what the bracket says.
			return nil
		case "q", "quit", "abort":
			return errQuit
		case "?", "h", "help":
			a.printf("\n%s\n", f.help)
			continue
		}

		// A bare number picks from the menu, where there is one. Values are
		// accepted by name too, so a human need not count lines.
		if n, ok := menuIndex(raw, len(options)); ok {
			raw = options[n]
		}

		trial := clone(cfg)
		if err := f.set(trial, raw); err != nil {
			a.printf("  ! %s: %v\n", f.path, err)
			continue
		}
		prune(trial)
		if why := answerFault(trial, f); why != "" {
			a.printf("  ! %s\n", why)
			continue
		}
		*cfg = *trial
		return nil
	}
}

// render prints the question and returns the selectable options in menu order,
// so a numeric answer and the printed list cannot disagree.
func (a *asker) render(cfg *xollama.Config, f field, current string) []string {
	a.printf("\n── %s\n", f.title)
	for _, line := range strings.Split(f.help, "\n") {
		a.printf("   %s\n", line)
	}

	shown := current
	if shown == "" {
		shown = "not set"
		if f.env != "" {
			shown += ", so " + f.env + " decides"
		}
	}
	a.printf("\n   current: %s\n", shown)

	var options []string
	switch f.kind {
	case kindTri:
		options = []string{"on", "off", "unset"}
	case kindChoice:
		options = append(f.choices(cfg), "unset")
	case kindOpenChoice:
		options = append(f.choices(cfg), "unset")
	case kindInt, kindFloat:
		unit := f.unit
		if unit != "" {
			unit = " of " + unit
		}
		a.printf("   a number%s, or `unset`\n", unit)
		return nil
	}

	for i, o := range options {
		a.printf("   %d) %s\n", i+1, o)
	}
	if f.kind == kindOpenChoice {
		a.printf("   or type a value not listed -- the list is what this build knows,\n")
		a.printf("   not a limit on what the engine accepts\n")
	}
	return options
}

// menuIndex reads a bare menu number. Anything else -- including a value that
// happens to look numeric, which is why this only applies where a menu was
// printed -- is left to the field's own parser.
func menuIndex(raw string, n int) (int, bool) {
	if n == 0 {
		return 0, false
	}
	var i int
	if _, err := fmt.Sscanf(raw, "%d", &i); err != nil {
		return 0, false
	}
	if fmt.Sprint(i) != raw || i < 1 || i > n {
		return 0, false
	}
	return i - 1, true
}

// confirm asks a yes/no question, defaulting to no. The prompt shape matches
// the field prompts so one expect pattern covers both.
func (a *asker) confirm(name, question string) (bool, error) {
	a.printf("\n%s\n%s [y/N]> ", question, name)
	raw, err := a.readLine()
	if err != nil {
		return false, err
	}
	a.answered++
	switch strings.ToLower(raw) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// menu asks a numbered question with no field behind it -- the opening
// modify/restart/clear choice. It returns the chosen option's key.
func (a *asker) menu(name, title string, options [][2]string, def int) (string, error) {
	a.printf("\n── %s\n", title)
	for i, o := range options {
		a.printf("   %d) %s\n", i+1, o[1])
	}
	a.printf("\n%s [%d]> ", name, def+1)

	raw, err := a.readLine()
	if err != nil {
		return "", err
	}
	a.answered++
	if raw == "" {
		return options[def][0], nil
	}
	if n, ok := menuIndex(raw, len(options)); ok {
		return options[n][0], nil
	}
	for _, o := range options {
		if strings.EqualFold(raw, o[0]) {
			return o[0], nil
		}
	}
	a.printf("  ! want 1-%d (got %q)\n", len(options), raw)
	return a.menu(name, title, options, def)
}

// review prints every stated setting, in field order, so what is about to be
// written is visible as one block rather than as the trail of answers.
func (a *asker) review(cfg *xollama.Config, w io.Writer) {
	stated := statedFields(cfg)
	if len(stated) == 0 {
		fmt.Fprintf(w, "\n── Review\n   (nothing stated -- this clears the model's xollama config)\n")
		return
	}
	width := 0
	for _, s := range stated {
		if len(s[0]) > width {
			width = len(s[0])
		}
	}
	fmt.Fprintf(w, "\n── Review\n")
	for _, s := range stated {
		fmt.Fprintf(w, "   %-*s  %s\n", width, s[0], s[1])
	}
}

// statedFields returns path/value pairs for every field that states something,
// in table order.
func statedFields(cfg *xollama.Config) [][2]string {
	var out [][2]string
	for _, f := range fields {
		if v := f.get(cfg); v != "" {
			out = append(out, [2]string{f.path, v})
		}
	}
	return out
}

func clone(c *xollama.Config) *xollama.Config {
	out := *c
	if c.KV != nil {
		kv := *c.KV
		if c.KV.Unified != nil {
			b := *c.KV.Unified
			kv.Unified = &b
		}
		out.KV = &kv
	}
	if c.Slots != nil {
		s := *c.Slots
		if c.Slots.Dynamic != nil {
			b := *c.Slots.Dynamic
			s.Dynamic = &b
		}
		out.Slots = &s
	}
	if c.DCA != nil {
		d := *c.DCA
		if c.DCA.Enabled != nil {
			b := *c.DCA.Enabled
			d.Enabled = &b
		}
		out.DCA = &d
	}
	if c.Session != nil {
		s := *c.Session
		if c.Session.Affinity != nil {
			b := *c.Session.Affinity
			s.Affinity = &b
		}
		if c.Session.Pool != nil {
			b := *c.Session.Pool
			s.Pool = &b
		}
		out.Session = &s
	}
	if c.Draft != nil {
		d := *c.Draft
		out.Draft = &d
	}
	return &out
}
