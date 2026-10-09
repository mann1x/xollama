package tweak

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/types/xollama"
)

// The drafter rows: which drafter a model carries, how many tokens it
// proposes, the driver it runs under and opencoti's policy for a built-in
// head. `--drafter` alone walks all four.
//
// Unlike every other row these depend on the MODEL and not only on its
// config: whether it has a head inside its weights, whether a drafter is
// attached, and which published drafter fits it are facts of the weights. The
// server reads them (api.ShowResponse.Drafter) and they are kept here for the
// run, so each question can say what this model has and what is right for it
// rather than list every value there is.

// modelDrafter is what the server said about the model being edited:
// drafterKnown is false when no model is (the server's defaults, a test of
// the table), and then nothing below blocks or advises.
var (
	modelDrafter *api.DrafterInfo
	drafterKnown bool
)

// useModelDrafter keeps the server's answer for the run.
func useModelDrafter(d *api.DrafterInfo) { modelDrafter, drafterKnown = d, true }

var drafterGroup = []string{"drafter", "draft-tokens", "spec-type", "mtp-policy"}

func hasBuiltInHead() bool { return modelDrafter != nil && modelDrafter.Source == "built-in" }

func hasAttachedDrafter() bool { return modelDrafter != nil && modelDrafter.Source == "attached" }

// headAfter is the drafter the model will carry once cfg is written: the one
// it names, or the one attached now when it names none.
func headAfter(c *xollama.Config) bool {
	if c.Draft != nil && c.Draft.Head != "" {
		return c.Draft.Head != xollama.DraftHeadNone
	}
	return hasAttachedDrafter()
}

// noDrafter is why a drafter setting cannot be stated: the model has no
// drafter and this config attaches none. It is the check that was missing
// when a driver pinned on a model without a drafter was written and reported
// as something the model could act on.
func noDrafter(what string) func(*xollama.Config) string {
	return func(c *xollama.Config) string {
		if !drafterKnown || hasBuiltInHead() || headAfter(c) {
			return ""
		}
		why := what + " needs a drafter, and this model has none"
		if modelDrafter != nil && modelDrafter.Recommended != nil {
			return why + "; --drafter=auto attaches the one published for it"
		}
		return why + "; attach one with --drafter"
	}
}

// builtInOnly is why the MTP policy cannot be stated on this model: the
// engine applies it only to a driver it selected itself, which it does for a
// head inside the weights and for nothing else.
func builtInOnly(c *xollama.Config) string {
	if why := opencotiOnly("the MTP auto policy")(c); why != "" {
		return why
	}
	if !drafterKnown || hasBuiltInHead() {
		return ""
	}
	if headAfter(c) {
		return "the MTP auto policy applies to a head built into the weights; an attached drafter always drafts at full depth"
	}
	return "the MTP auto policy applies to a head built into the weights, and this model has none"
}

// drafterState describes the model's drafter in a line or two, for the top of
// each drafter question.
func drafterState(c *xollama.Config) []string {
	if !drafterKnown {
		return nil
	}
	var lines []string
	d := modelDrafter
	switch {
	case d == nil || d.Source == api.DrafterSourceNone:
		lines = append(lines, "this model: no drafter attached, and no head inside the weights")
	case d.Source == "built-in":
		lines = append(lines, "this model: a head built into the weights (NextN); it needs no drafter file")
	default:
		what := strings.TrimSpace(d.Architecture + " " + d.QuantizationLevel)
		lines = append(lines, "this model: drafter attached ("+what+")")
		if d.Mismatch != "" {
			lines = append(lines, "  ! it does not fit: "+d.Mismatch)
		}
	}
	if c.Draft != nil && c.Draft.Head != "" && (d == nil || c.Draft.Head != d.Digest) {
		if c.Draft.Head == xollama.DraftHeadNone {
			return append(lines, "this run: detaches it")
		}
		from := pendingHead[c.Draft.Head]
		if from == "" {
			from = c.Draft.Head
		}
		return append(lines, "this run: attaches "+from)
	}
	if d != nil && d.Recommended != nil {
		r := d.Recommended
		if r.Attached {
			lines = append(lines, "published for it: the Gemma 4 "+r.Target+" assistant, which is the one attached")
		} else {
			lines = append(lines, "published for it: "+r.Source)
			lines = append(lines, "  `auto` fetches and attaches that one")
		}
	}
	return lines
}

// inferredSpecType is the driver the model's drafter implies once cfg is
// written, or "" when that cannot be said from here.
func inferredSpecType(c *xollama.Config) string {
	d := modelDrafter
	if d == nil {
		return ""
	}
	if c.Draft != nil && c.Draft.Head != "" && c.Draft.Head != xollama.DraftHeadNone && c.Draft.Head != d.Digest {
		// A head attached in this run: only the published one is known.
		if d.Recommended != nil && pendingHead[c.Draft.Head] == d.Recommended.Source {
			return d.Recommended.SpecType
		}
		return ""
	}
	switch d.Source {
	case "built-in":
		return "draft-mtp"
	case "attached":
		if d.Pinned {
			return ""
		}
		return d.SpecType
	}
	return ""
}

func specTypeState(c *xollama.Config) []string {
	lines := drafterState(c)
	if s := inferredSpecType(c); s != "" {
		lines = append(lines, "its driver, read from the drafter's own metadata: "+s)
		lines = append(lines, "  leave this unset to use it; a pin overrides it, and the wrong one fails the load")
	}
	return lines
}

func specTypeNote(c *xollama.Config, option string) string {
	if s := inferredSpecType(c); s != "" && option == s {
		return "this model's driver (what unset already gives)"
	}
	if option == "unset" && inferredSpecType(c) != "" {
		return "recommended"
	}
	return ""
}

func tokensState(c *xollama.Config) []string {
	lines := drafterState(c)
	d := modelDrafter
	if d == nil || d.Source == api.DrafterSourceNone {
		return lines
	}
	switch d.TokensFrom {
	case "model":
		if d.Tokens == 0 {
			lines = append(lines, "today: drafting is off for this model")
		} else {
			lines = append(lines, fmt.Sprintf("today: %d tokens per step, set on this model", d.Tokens))
		}
	case "default":
		lines = append(lines, fmt.Sprintf("today: %d tokens per step, the default for an attached drafter", d.Tokens))
	case "engine":
		lines = append(lines, "today: left to opencoti, which picks the depth and drafts by its policy; stock llama.cpp does not draft")
	}
	return lines
}

// pendingHead maps a digest resolved in this run to where it came from, for
// the question that follows. The files themselves are in mediaFiles and
// mediaSources, which write already knows how to send.
var pendingHead = map[string]string{}

// setDrafter takes `auto`, a file path, an hf.co reference, a digest, or a
// word that means no drafter.
func setDrafter(c *xollama.Config, v string) error {
	s := strings.TrimSpace(v)
	tokens := 0
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "off", "detach":
		// With a drafter attached there is only one thing clearing this can
		// mean. Without one it states nothing.
		if hasAttachedDrafter() {
			draft(c).Head = xollama.DraftHeadNone
		} else if c.Draft != nil {
			c.Draft.Head = ""
		}
		return nil
	case "auto":
		if modelDrafter == nil || modelDrafter.Recommended == nil {
			return fmt.Errorf("no drafter is published for this model; give a file, an hf.co reference or a digest")
		}
		s = modelDrafter.Recommended.Source
		tokens = modelDrafter.Recommended.Tokens
	}
	var digest string
	if err := setBlob(s, &digest); err != nil {
		return err
	}
	pendingHead[digest] = s
	draft(c).Head = digest
	// The published drafter comes with the length measured best for it,
	// unless the model already states one.
	if tokens > 0 && c.Draft.Tokens == nil {
		c.Draft.Tokens = &tokens
	}
	return nil
}

func setDraftTokens(c *xollama.Config, v string) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "auto":
		if c.Draft != nil {
			c.Draft.Tokens = nil
		}
		return nil
	case "off", "none":
		s = "0"
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("want a whole number, 0 for off, or unset (got %q)", v)
	}
	if n < 0 || n > xollama.MaxDraftTokens {
		return fmt.Errorf("want 0 to %d (got %q)", xollama.MaxDraftTokens, v)
	}
	draft(c).Tokens = &n
	return nil
}

func drafterFields() []field {
	return []field{
		{
			name:  "drafter",
			path:  "draft.head",
			title: "Drafter — the draft model attached beside the weights",
			help: "A drafter proposes several tokens per step and the model verifies them in\n" +
				"one pass, which is where the speed comes from. `auto` fetches the drafter\n" +
				"published for this model from Hugging Face and attaches it; a file path, an\n" +
				"hf.co/<owner>/<repo>/<file> reference or a sha256 digest attaches that one,\n" +
				"replacing any the model has; `none` detaches it. `auto` also sets the draft\n" +
				"length measured best for that drafter, unless the model states one. A\n" +
				"drafter built for another model size is refused here, not at load.",
			kind:      kindBlob,
			head:      true,
			group:     drafterGroup,
			modelOnly: true,
			describe:  drafterState,
			get: func(c *xollama.Config) string {
				return orEmpty(c.Draft != nil, func() string { return c.Draft.Head })
			},
			set: setDrafter,
		},
		{
			name:  "draft-tokens",
			path:  "draft.tokens",
			title: "Draft length — tokens the drafter proposes per step",
			help: "0 turns drafting off: an attached drafter is not loaded, and on opencoti a\n" +
				"head built into the weights is not loaded either (measured: 722 MiB less on\n" +
				"the card for a Qwen 3.6 27B). Unset is 4 for an attached drafter; a built-in\n" +
				"head is then left to opencoti, which picks the depth and drafts by its\n" +
				"policy. A Gemma 4 assistant is fastest at 2: on a 26B-A4B, code went from 94\n" +
				"tok/s to 138 at 2 and 133 at 3, an essay to 111 at 2 and 95 at 4. opencoti\n" +
				"recommends 3 for a Qwen NextN head. What a drafter gains depends on how\n" +
				"predictable the text is. A number on a built-in head states the driver,\n" +
				"which drafts at that depth always and sets the MTP auto policy aside. A\n" +
				"PARAMETER draft_num_predict, or a request's, wins over this.",
			kind:     kindInt,
			unit:     "tokens (0 = off)",
			describe: tokensState,
			blocked:  noDrafter("the draft length"),
			get: func(c *xollama.Config) string {
				return orEmpty(c.Draft != nil && c.Draft.Tokens != nil, func() string { return strconv.Itoa(*c.Draft.Tokens) })
			},
			set: setDraftTokens,
		},
	}
}

// fetchDrafter gets the server the head this config attaches: a local file is
// uploaded, a Hugging Face file is fetched by the server itself. A digest
// typed as such is the operator's word that the server has it; create refuses
// it otherwise.
func fetchDrafter(ctx context.Context, client *api.Client, cfg *xollama.Config, out io.Writer) error {
	digest := cfg.Draft.HeadDigest()
	if digest == "" {
		return nil
	}
	path, local := mediaFiles[digest]
	source, remote := mediaSources[digest]
	if !local && !remote {
		return nil
	}
	have, err := client.HeadBlob(ctx, digest)
	if err != nil {
		return fmt.Errorf("drafter: %w", err)
	}
	if have {
		fmt.Fprintf(out, "   the drafter is already on the server\n")
		return nil
	}
	if !local {
		return pullMedia(ctx, client, "drafter", source, digest, out)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "   uploading the drafter (%s, %s)\n", filepath.Base(path), format.HumanBytes(fi.Size()))
	if err := client.CreateBlob(ctx, digest, f); err != nil {
		return fmt.Errorf("upload the drafter: %w", err)
	}
	return nil
}

// DrafterRows are the rows of `xollama show`'s Drafter table.
func DrafterRows(d *api.DrafterInfo) (out [][]string) {
	if d == nil {
		return nil
	}
	out = append(out, []string{"", "source", d.Source})
	if d.Architecture != "" {
		out = append(out, []string{"", "architecture", d.Architecture})
	}
	if d.ParameterSize != "" {
		out = append(out, []string{"", "parameters", d.ParameterSize})
	}
	if d.QuantizationLevel != "" {
		out = append(out, []string{"", "quantization", d.QuantizationLevel})
	}
	if d.Source != api.DrafterSourceNone {
		// Say where the answer came from, not just what it is: "pinned" is
		// the difference between a setting someone chose and one the
		// drafter's own metadata implied, and that is the whole reason
		// draft.spec_type exists.
		switch {
		case d.SpecType == "":
			out = append(out, []string{"", "spec type", "unresolved"})
		case d.Pinned:
			out = append(out, []string{"", "spec type", d.SpecType + " (pinned)"})
		default:
			out = append(out, []string{"", "spec type", d.SpecType + " (inferred)"})
		}
	}
	switch d.TokensFrom {
	case "model":
		if d.Tokens == 0 {
			out = append(out, []string{"", "draft tokens", "0 (off)"})
		} else {
			out = append(out, []string{"", "draft tokens", strconv.Itoa(d.Tokens) + " (set on the model)"})
		}
	case "default":
		out = append(out, []string{"", "draft tokens", strconv.Itoa(d.Tokens) + " (default)"})
	case "engine":
		out = append(out, []string{"", "draft tokens", "the engine's choice"})
	}
	if d.Mismatch != "" {
		out = append(out, []string{"", "fits", "no: " + d.Mismatch})
	}
	if r := d.Recommended; r != nil && !r.Attached {
		out = append(out, []string{"", "published", r.Source})
		out = append(out, []string{"", "attach it", "--drafter=auto"})
	}
	return out
}

// The two rows go in front of spec-type, so a walk asks which drafter before
// it asks how to drive it.
func init() {
	for i, f := range fields {
		if f.name == "spec-type" {
			fields = append(fields[:i:i], append(drafterFields(), fields[i:]...)...)
			return
		}
	}
	panic("tweak: no spec-type row to put the drafter rows before")
}
