package council

// Instructions and the harness directive (plans/council-harness.md, Phases 1
// and 2). A model's owner and a client may add guidance to every role, one
// role or the builder, and a harness may state what it already knows about
// a turn -- its mode, a build, its agent's evidence, the tool that checks --
// so the council does not spend a call working it out again.

import (
	"context"
	"fmt"
	"strings"

	"github.com/ollama/ollama/api"
)

// Everyone is the instruction slot every role reads, after the charter.
const Everyone Role = "council"

// Said is guidance by where it came from.
type Said struct{ Owner, Client string }

// Instruction slots a directive may fill, by name.
var instructionSlots = []Role{Everyone, Builder, Planner, Researcher, Critic, Synthesizer}

const (
	ownerSaid  = "INSTRUCTIONS FROM THE MODEL'S OWNER"
	clientSaid = "INSTRUCTIONS FROM THE CLIENT"
)

// instructions is the guidance slot r carries, each part under its source,
// or "".
func (cfg Config) instructions(r Role) string {
	s := cfg.Instructions[r]
	var parts []string
	if t := strings.TrimSpace(s.Owner); t != "" {
		parts = append(parts, ownerSaid+":\n"+t)
	}
	if t := strings.TrimSpace(s.Client); t != "" {
		parts = append(parts, clientSaid+":\n"+t)
	}
	return strings.Join(parts, "\n\n")
}

// withInstructions is s followed by the guidance of the slots, in order.
func (cfg Config) withInstructions(s string, slots ...Role) string {
	for _, r := range slots {
		if in := cfg.instructions(r); in != "" {
			if s != "" {
				s += "\n\n"
			}
			s += in
		}
	}
	return s
}

// Mode is how much of the council a turn uses, from the directive.
func (cfg Config) mode() string {
	if cfg.Mode == "" {
		return api.CouncilModeAuto
	}
	return cfg.Mode
}

// Direct applies a harness's directive to cfg. An unknown mode or slot is an
// error, never ignored: the harness would otherwise believe it was obeyed.
func (cfg Config) Direct(d *api.CouncilDirective) (Config, error) {
	if d == nil {
		return cfg, nil
	}
	switch d.Mode {
	case "", api.CouncilModeAuto, api.CouncilModeAnswer, api.CouncilModeEscalate, api.CouncilModeDeliberate:
		cfg.Mode = d.Mode
	default:
		return cfg, fmt.Errorf("council.mode %q: want auto, answer, escalate or deliberate", d.Mode)
	}
	if len(d.Instructions) > 0 {
		in := make(map[Role]Said, len(cfg.Instructions)+len(d.Instructions))
		for r, s := range cfg.Instructions {
			in[r] = s
		}
		for k, v := range d.Instructions {
			r := Role(k)
			if !containsRole(instructionSlots, r) {
				return cfg, fmt.Errorf("council.instructions.%s: want council, builder, planner, researcher, critic or synthesizer", k)
			}
			s := in[r]
			s.Client = v
			in[r] = s
		}
		cfg.Instructions = in
	}
	if len(d.Build) > 0 {
		if b := parseBuild(string(d.Build), cfg); b.Target != "" {
			cfg.Stated = b
		}
	}
	cfg.Evidence = d.Evidence
	cfg.CheckTool = d.Check
	return cfg, nil
}

func containsRole(rs []Role, r Role) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// makeBuild is the turn's build: the harness's, when it stated one, else the
// builder's.
func (cfg Config) makeBuild(ctx context.Context, m Model, d Draws, conv []api.Message, emit Emit) (*Build, error) {
	if cfg.Stated != nil {
		return cfg.Stated.clone(), nil
	}
	return MakeBuild(ctx, m, cfg, d, conv, emit)
}

// evidenceIntro opens one attempt of the harness's agent among the checks
// that already failed.
const evidenceIntro = "The agent that worked on this before the council tried it, and it was not settled."

// escalated is the harness's evidence as the turn's failed checks.
func (cfg Config) escalated() []string {
	var out []string
	for _, e := range cfg.Evidence {
		var b strings.Builder
		b.WriteString(evidenceIntro)
		if t := strings.TrimSpace(e.Tried); t != "" {
			b.WriteString("\nWhat it tried: " + t)
		}
		if t := strings.TrimSpace(e.Check); t != "" {
			b.WriteString("\nHow it checked: " + t)
		}
		if e.Result != "" {
			b.WriteString("\nWhat the check returned:\n" + e.Result)
		}
		out = append(out, truncate(b.String(), maxPriorChars))
	}
	return out
}

// priorCheck is the output of the agent's last check, which the council's
// first check is compared with (stuck.go), or "".
func (cfg Config) priorCheck() string {
	if cfg.mode() != api.CouncilModeEscalate || len(cfg.Evidence) == 0 {
		return ""
	}
	return truncate(cfg.Evidence[len(cfg.Evidence)-1].Result, maxCheckChars)
}

// The user's cues (plans/council-harness.md, Phase 3): how much care a turn
// deserves follows what the user last said about it, in words that name no
// topic. A stated mode is never routed, so it wins over a cue.
const (
	routeCue = "The user may have said how much care requests deserve: a quick answer, or to take the time and check. Follow what the user said about it most recently, which holds until the user says otherwise: a quick answer is direct, a request to take the time goes to the council."
	frontCue = "The user may have said how much care requests deserve: follow what the user said about it most recently, which holds until the user says otherwise. A quick answer you give yourself; a request to take the time and check you forward."
)
