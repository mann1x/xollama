package tweak

import (
	"fmt"
	"slices"
	"strings"
)

// referenceSections orders the reference by the config section a setting
// lives in, with the heading each gets. A section missing here still appears,
// after these, under its own name: a new one cannot fall out of the page.
var referenceSections = []struct{ section, heading string }{
	{"engine", "Engine"},
	{"flash_attention", "Engine"},
	{"kv", "KV cache"},
	{"slots", "Concurrent requests"},
	{"session", "Sessions and shared prefixes"},
	{"dca", "Long context (DCA)"},
	{"fit", "Fitting the model in memory"},
	{"tuning", "Engine tuning"},
	{"devices", "Devices"},
	{"draft", "Drafter"},
	{"council", "Council"},
	{"media.image", "Image generation and edit"},
	{"media.stt", "Speech to text"},
	{"media.tts", "Text to speech"},
	{"media.video", "Video"},
}

// Reference renders every setting `xollama tweak model` has as the page
// docs/xollama/tweak-reference.mdx. It is generated from the same table the
// flags are, so a setting cannot exist without being documented:
// TestTheFlagReferenceIsCurrent fails when the page is not this text.
func Reference() string {
	var b strings.Builder
	b.WriteString(`---
title: Every tweak setting
---

{/* Generated from cmd/tweak by: XOLLAMA_UPDATE_DOCS=1 go test ./cmd/tweak -run TestTheFlagReferenceIsCurrent. Do not edit by hand. */}

Every setting ` + "`xollama tweak model`" + ` can state, one row each: the flag, the
path it writes in the model's ` + "`xollama.json`" + `, and the values it takes. How to
use the command is in [Changing a model's settings](/xollama/tweak); what the
settings mean is in the page each section links to.

- A flag with a value sets that one setting and asks nothing:
  ` + "`--kv-k=q8_0`" + `. Write the value with ` + "`=`" + `.
- A bare flag asks that setting's questions: ` + "`--kv-k`" + `.
- ` + "`unset`" + ` removes the setting from the model, so the server's default or
  the environment decides again.
- **Server** marks a setting ` + "`xollama tweak server`" + ` can give a default for,
  under the same flag.
- **Falls back to** names the environment variable that decides when neither
  the model nor the server states the setting.

`)

	order := make([]string, 0, len(referenceSections))
	for _, s := range referenceSections {
		order = append(order, s.section)
	}
	sectionOf := func(path string) string {
		best := ""
		for _, s := range order {
			if (path == s || strings.HasPrefix(path, s+".")) && len(s) > len(best) {
				best = s
			}
		}
		if best == "" {
			best, _, _ = strings.Cut(path, ".")
		}
		return best
	}
	heading := func(section string) string {
		for _, s := range referenceSections {
			if s.section == section {
				return s.heading
			}
		}
		return section
	}

	server := serverFields()
	byHeading := map[string][]field{}
	var headings []string
	for _, s := range referenceSections {
		if !slices.Contains(headings, s.heading) {
			headings = append(headings, s.heading)
		}
	}
	for _, f := range fields {
		h := heading(sectionOf(f.path))
		if !slices.Contains(headings, h) {
			headings = append(headings, h)
		}
		byHeading[h] = append(byHeading[h], f)
	}

	total := 0
	for _, h := range headings {
		rows := byHeading[h]
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n| Flag | Setting | Values | What it is | Server | Falls back to |\n|---|---|---|---|---|---|\n", h)
		for _, f := range rows {
			usage := flagUsage(f)
			values := ""
			if i := strings.Index(usage, " ("); i >= 0 {
				values = strings.TrimSuffix(usage[i+2:], "); bare asks")
			}
			onServer := ""
			if slices.Contains(server, f.name) {
				onServer = "yes"
			}
			env := ""
			if f.env != "" {
				env = "`" + f.env + "`"
			}
			fmt.Fprintf(&b, "| `--%s` | `%s` | %s | %s | %s | %s |\n", f.name, f.path, cell(strings.ReplaceAll(values, "|", ", ")), cell(f.title), onServer, env)
			total++
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, `## The command's own flags

| Flag | What it does |
|---|---|
| `+"`--dry-run`"+` | Show the resulting config without writing it |
| `+"`--json`"+` | Print the resulting `+"`xollama.json`"+` to stdout |
| `+"`--clear`"+` | Remove the model's xollama config entirely (an attached drafter stays; `+"`--drafter=none`"+` detaches it) |
| `+"`-y`, `--yes`"+` | Do not ask for confirmation before writing |

%d settings. `+"`xollama tweak model --help`"+` prints the same list as your build has it.
`, total)
	return b.String()
}

// cell makes text safe inside a Markdown table cell of an MDX page.
func cell(s string) string {
	s = strings.NewReplacer("|", "\\|", "<", "&lt;", ">", "&gt;", "{", "&#123;", "}", "&#125;", "\n", " ").Replace(s)
	return s
}
