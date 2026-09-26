package tweak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// `--council` asks the council's own questions, in order, and nothing else.
func TestTheCouncilFlagWalksTheCouncil(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{Engine: xollama.EngineOpencoti}, []string{"--council", "--yes"},
		"on",            // council
		"3",             // researchers
		"",              // critics: keep the default
		"0",             // jitter: no spread, which is a stated answer
		"",              // seed: random
		"2",             // max rounds
		"off",           // show deliberation
		"",              // polykv
		"32768", "8192", // window, floor
		"0.8",   // compact at
		"0.7",   // idle compact at
		"basic", // compaction
		"",      // review: keep the default
		"off",   // retrospective
	)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	k := cfg.Council
	if !k.On() || k.Researcher.Count != 3 || k.Critic != nil || k.MaxRounds != 2 ||
		k.TemperatureJitter == nil || *k.TemperatureJitter != 0 || k.Seed != nil ||
		k.ShowDeliberation == nil || *k.ShowDeliberation || k.Context.Window != 32768 ||
		k.Context.Floor != 8192 || k.Context.CompactAt != 0.8 || k.Context.IdleCompactAt != 0.7 ||
		k.Context.Compaction != xollama.CouncilCompactionBasic || k.Context.Review != nil ||
		k.Context.Retrospective == nil || *k.Context.Retrospective {
		t.Fatalf("council = %+v", k)
	}
	if cfg.Engine != xollama.EngineOpencoti {
		t.Fatal("the council walk changed the engine")
	}
	for _, unwanted := range []string{"council-charter [", "council-planner-model [", "engine ["} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("the council walk asked %q\n%s", unwanted, out)
		}
	}
}

func TestCouncilFlagsSetWithoutAsking(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{}, []string{"--council=on", "--council-critics=4", "--council-jitter=0", "--council-seed=42", "--council-researcher-model=qwen3:4b"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	k := cfg.Council
	if k.Critic.Count != 4 || *k.TemperatureJitter != 0 || *k.Seed != 42 || k.Researcher.Model != "qwen3:4b" {
		t.Fatalf("council = %+v", k)
	}
	if strings.Contains(out, "> ") {
		t.Fatalf("valued flags prompted:\n%s", out)
	}
}

// An ordinary model is not asked 22 questions about a council it is not, and
// is not told 22 times that it was not asked.
func TestTheFullWalkDoesNotMentionACouncilThatIsOff(t *testing.T) {
	blanks := make([]string, 80)
	_, out, err := run(t, &xollama.Config{}, []string{"--yes"}, blanks...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "\ncouncil [unset]> ") {
		t.Fatalf("the full walk should offer the council switch\n%s", out)
	}
	for _, unwanted := range []string{"council-researchers [", "skipped council."} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("the full walk said %q about a council that is off\n%s", unwanted, out)
		}
	}
}

// Asked by name, the same question says why it is not asked.
func TestANamedCouncilQuestionSaysWhyItIsSkipped(t *testing.T) {
	_, out, err := run(t, &xollama.Config{}, []string{"--council-researchers", "--yes"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped council.researcher.count: a council setting, and council.enabled is not on") {
		t.Fatalf("want the reason for the skip\n%s", out)
	}
}

// A setting for a council that is not on is dropped by the consistency pass
// and named, like every other setting stranded by its switch.
func TestACouncilSettingWithoutTheSwitchIsDroppedAndNamed(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{}, []string{"--council-researchers=3"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Council != nil {
		t.Fatalf("council.researcher.count stored without council.enabled: %+v", cfg.Council)
	}
	if !strings.Contains(out, "dropped council.researcher.count = 3: a council setting, and council.enabled is not on") {
		t.Fatalf("the dropped setting was not named\n%s", out)
	}
}

// A prompt is text: case, punctuation and newlines survive, and @path reads it.
func TestAPromptIsKeptAsWrittenAndCanComeFromAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "critic.txt")
	prompt := "You are the CRITIC.\nList every Unsupported claim."
	if err := os.WriteFile(p, []byte(prompt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, out, err := run(t, &xollama.Config{Council: &xollama.Council{Enabled: boolp(true)}},
		[]string{"--council-critic-prompt=@" + p, "--council-charter=Be Brief."})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if cfg.Council.Critic.Prompt != prompt || cfg.Council.Charter != "Be Brief." {
		t.Fatalf("prompt = %q, charter = %q", cfg.Council.Critic.Prompt, cfg.Council.Charter)
	}
	if _, _, err := run(t, &xollama.Config{Council: &xollama.Council{Enabled: boolp(true)}},
		[]string{"--council-critic-prompt=@" + filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("a prompt file that does not exist was accepted")
	}
}

// Invalid combinations are refused by xollama.Config.Validate, the one
// authority, not by a second copy of the rules here.
func TestACouncilTheEngineCannotServeIsRefused(t *testing.T) {
	_, _, err := run(t, &xollama.Config{Engine: xollama.EngineLlamaCpp}, []string{"--council=on", "--council-polykv=on"})
	if err == nil || !strings.Contains(err.Error(), "needs the opencoti engine") {
		t.Fatalf("err = %v, want the engine refusal", err)
	}
	_, _, err = run(t, &xollama.Config{}, []string{"--council=on", "--council-researchers=9"})
	if err == nil || !strings.Contains(err.Error(), "above 8") {
		t.Fatalf("err = %v, want the width refusal", err)
	}
}

// `xollama show` lists what a council states, under the paths the wizard uses.
func TestShowListsTheCouncil(t *testing.T) {
	seed := int64(3)
	rows := SettingRows(&xollama.Config{Council: &xollama.Council{
		Enabled: boolp(true), Researcher: &xollama.CouncilRole{Count: 3}, Seed: &seed,
		Context: &xollama.CouncilContext{Window: 32768},
	}})
	got := map[string]string{}
	for _, r := range rows {
		got[r[0]] = r[1]
	}
	want := map[string]string{"council.enabled": "on", "council.researcher.count": "3", "council.seed": "3", "council.context.window": "32768"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("show row %s = %q, want %q (rows %v)", k, got[k], v, rows)
		}
	}
	if len(rows) != len(want) {
		t.Fatalf("show listed unstated settings: %v", rows)
	}
}
