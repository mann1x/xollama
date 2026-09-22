package tweak

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/xollama"
)

// run drives the real command's build step with a scripted stdin, which is the
// same surface an expect script drives. It deliberately does not stub the
// wizard: a test that reimplemented the prompts would pass while the prompts
// were wrong.
func run(t *testing.T, current *xollama.Config, args []string, answers ...string) (*xollama.Config, string, error) {
	t.Helper()

	cmd := modelCommand(Options{})
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}

	script := ""
	if len(answers) > 0 {
		script = strings.Join(answers, "\n") + "\n"
	}
	var out bytes.Buffer
	a := newAsker(strings.NewReader(script), &out)

	cfg, err := build(cmd, "m:test", current, a)
	return cfg, out.String(), err
}

func boolp(b bool) *bool { return &b }

// A bare flag asks that feature's questions and no others. This is the shape
// the whole flag surface promises: --dca is a scoped wizard, --dca=on is a set.
func TestABareFlagScopesTheWalkToThatFeature(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineOpencoti, FlashAttention: "on"}

	// Two answers: dca.enabled, then dca.chunk_size.
	cfg, out, err := run(t, current, []string{"--dca", "--yes"}, "on", "4096")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DCA == nil || cfg.DCA.Enabled == nil || !*cfg.DCA.Enabled {
		t.Fatalf("dca.enabled = %+v, want on", cfg.DCA)
	}
	if cfg.DCA.ChunkSize != 4096 {
		t.Fatalf("dca.chunk_size = %d, want 4096", cfg.DCA.ChunkSize)
	}
	// Untouched, and not asked about.
	if cfg.Engine != xollama.EngineOpencoti || cfg.FlashAttention != "on" {
		t.Fatalf("scoped walk changed settings outside its scope: %+v", cfg)
	}
	for _, unwanted := range []string{"engine [", "flash-attn [", "kv-k ["} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("scoped walk asked %q\n%s", unwanted, out)
		}
	}
}

// The other half of the same flag: a value sets it and asks nothing at all, so
// the command is usable from a script with no expect.
func TestAValuedFlagSetsWithoutAsking(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{}, []string{"--dca=on"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DCA == nil || !*cfg.DCA.Enabled {
		t.Fatalf("dca.enabled = %+v, want on", cfg.DCA)
	}
	if strings.Contains(out, "> ") {
		t.Fatalf("a valued flag prompted for something:\n%s", out)
	}
}

// The prompt format is a contract, not a detail: expect scripts wait on it.
func TestThePromptNamesTheFieldAndItsDefault(t *testing.T) {
	_, out, err := run(t, &xollama.Config{DCA: &xollama.DCA{Enabled: boolp(true)}},
		[]string{"--dca", "--yes"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\ndca [on]> ") {
		t.Fatalf("want a prompt `dca [on]> ` carrying the current value\n%s", out)
	}
	if !strings.Contains(out, "\ndca-chunk [unset]> ") {
		t.Fatalf("want an unset field to prompt `[unset]`\n%s", out)
	}
}

// Blank keeps what the bracket says, which is what makes a long walk cheap to
// drive: a script answers only the questions it cares about.
func TestBlankKeepsTheCurrentValue(t *testing.T) {
	current := &xollama.Config{DCA: &xollama.DCA{Enabled: boolp(true), ChunkSize: 8192}}
	cfg, _, err := run(t, current, []string{"--dca", "--yes"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DCA.ChunkSize != 8192 || !*cfg.DCA.Enabled {
		t.Fatalf("blank answers changed the config: %+v", cfg.DCA)
	}
}

// A menu number and the printed list cannot disagree, because the menu returns
// the options it printed.
func TestAMenuNumberPicksTheOptionItPrinted(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{}, []string{"--engine", "--yes"}, "2")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("engine = %q, want the second option %q\n%s", cfg.Engine, xollama.EngineLlamaCpp, out)
	}
}

// Consistency during setup: a question the model cannot act on is not asked,
// and the skip says why rather than vanishing.
func TestAQuestionTheModelCannotActOnIsSkippedWithAReason(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineLlamaCpp}
	_, out, err := run(t, current, []string{"--kv-residency", "--yes"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "skipped kv.residency_mode") {
		t.Fatalf("want a named skip\n%s", out)
	}
	if !strings.Contains(out, "opencoti") {
		t.Fatalf("the skip must say why\n%s", out)
	}
}

// Consistency at the end: a setting that became unusable is dropped and named.
// This is the case a scoped walk cannot prevent -- the setting was already in
// the model's config when the engine pin changed under it.
func TestASettingStrandedByAnotherAnswerIsDroppedAndNamed(t *testing.T) {
	current := &xollama.Config{KV: &xollama.KV{ResidencyMode: xollama.ResidencyWindow}}

	cfg, out, err := run(t, current, []string{"--engine=llamacpp"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KV != nil && cfg.KV.ResidencyMode != "" {
		t.Fatalf("kv.residency_mode survived an engine pin that cannot serve it: %+v", cfg.KV)
	}
	if !strings.Contains(out, "dropped kv.residency_mode = window") {
		t.Fatalf("a drop must name the setting and its value\n%s", out)
	}
}

// The other side of the same rule: a combination where only the operator knows
// which half they meant is refused, not silently resolved.
func TestAnAmbiguousCombinationIsRefusedRatherThanResolved(t *testing.T) {
	current := &xollama.Config{Slots: &xollama.Slots{Dynamic: boolp(true)}}

	_, _, err := run(t, current, []string{"--kv-unified=off"})
	if err == nil {
		t.Fatal("want a refusal, got a written config")
	}
	if !strings.Contains(err.Error(), "slots.dynamic needs kv.unified") {
		t.Fatalf("the refusal must name both halves: %v", err)
	}
}

// Interactively the same conflict is caught at the answer, where it can still
// be changed, rather than at the end where it would mean discarding the walk.
func TestAConflictingAnswerIsRefusedAndAskedAgain(t *testing.T) {
	current := &xollama.Config{Slots: &xollama.Slots{Dynamic: boolp(true)}}

	cfg, out, err := run(t, current, []string{"--kv-unified", "--yes"}, "off", "on")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "slots.dynamic needs kv.unified") {
		t.Fatalf("want the reason at the prompt\n%s", out)
	}
	if cfg.KV == nil || cfg.KV.Unified == nil || !*cfg.KV.Unified {
		t.Fatalf("kv.unified = %+v, want the second answer to have landed", cfg.KV)
	}
}

// The measured engine preconditions are applied at configuration time, because
// meeting them at load time means meeting them as a model that will not start.
func TestTheRingCachePreconditionsAreCheckedHere(t *testing.T) {
	_, _, err := run(t, &xollama.Config{}, []string{"--kv-swa-k=q4_0", "--kv-swa-v=q4_0"})
	if err == nil {
		t.Fatal("want a refusal: a ring needs a KVarN base on kv.k and kv.v")
	}
	if !strings.Contains(err.Error(), "KVarN base") {
		t.Fatalf("the refusal must name the precondition: %v", err)
	}

	cfg, _, err := run(t, &xollama.Config{}, []string{
		"--kv-k=kvarn3", "--kv-v=kvarn3", "--kv-swa-k=kvarn4", "--kv-swa-v=kvarn4",
	})
	if err != nil {
		t.Fatalf("a measured-good ring was refused: %v", err)
	}
	if cfg.KV.KSWA != "kvarn4" {
		t.Fatalf("kv.k_swa = %q, want kvarn4", cfg.KV.KSWA)
	}
}

// Half a ring is a configuration the engine refuses at boot, so asking about
// one half asks about both.
func TestAskingAboutOneHalfOfTheRingAsksAboutBoth(t *testing.T) {
	_, out, err := run(t, &xollama.Config{}, []string{"--kv-swa-k", "--yes"}, "unset", "unset")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kv-swa-k [") || !strings.Contains(out, "kv-swa-v [") {
		t.Fatalf("want both halves of the ring asked\n%s", out)
	}
}

// --clear is the only way to say "the fork config is nothing", and it must not
// be mixable with a flag that says the opposite.
func TestClearRemovesEverythingAndRefusesToBeMixed(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineOpencoti, DCA: &xollama.DCA{Enabled: boolp(true)}}

	cfg, _, err := run(t, current, []string{"--clear"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsZero() {
		t.Fatalf("--clear left %+v", cfg)
	}

	if _, _, err := run(t, current, []string{"--clear", "--dca=on"}); err == nil {
		t.Fatal("want --clear combined with a setting to be refused")
	}
}

// The no-flags path offers what to do before asking twenty questions, because
// "start from scratch" and "modify" are different runs.
func TestAnExistingConfigIsOfferedForModifyOrRestart(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineOpencoti, FlashAttention: "on"}

	cfg, out, err := run(t, current, []string{"--yes"}, "3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already has an xollama config") {
		t.Fatalf("want the opening menu\n%s", out)
	}
	if !cfg.IsZero() {
		t.Fatalf("option 3 is clear, got %+v", cfg)
	}

	if _, _, err := run(t, current, []string{"--yes"}, "4"); err != errQuit {
		t.Fatalf("option 4 is quit, got %v", err)
	}
}

// Starting from scratch starts every question unset, rather than from what the
// model states -- which is the difference the menu is offering.
func TestStartingFromScratchForgetsTheCurrentConfig(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineOpencoti, FlashAttention: "on"}

	answers := []string{"2"} // start from scratch
	for range fields {
		answers = append(answers, "")
	}
	cfg, _, err := run(t, current, []string{"--yes"}, answers...)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsZero() {
		t.Fatalf("from scratch, all blank, want nothing stated; got %+v", cfg)
	}
}

// Running out of script is a stop, not a set of defaults: continuing would
// write a configuration nobody chose.
func TestRunningOutOfAnswersStopsRatherThanDefaulting(t *testing.T) {
	if _, _, err := run(t, &xollama.Config{}, []string{"--dca"}); err != errQuit {
		t.Fatalf("want errQuit on EOF, got %v", err)
	}
}

// The confirmation is asked exactly when a person was asked something. A run
// driven entirely by flags has nothing to confirm.
func TestTheConfirmationFollowsWhetherAnythingWasAsked(t *testing.T) {
	_, out, err := run(t, &xollama.Config{}, []string{"--dca"}, "on", "unset", "y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "write [y/N]> ") {
		t.Fatalf("want a confirmation after an interactive run\n%s", out)
	}

	if _, _, err := run(t, &xollama.Config{}, []string{"--dca"}, "on", "unset", "n"); err != errQuit {
		t.Fatalf("declining the confirmation must write nothing, got %v", err)
	}
}

// Every field is reachable by flag and every flag names a field: the table is
// the only source, so a row added without a flag would be unreachable and a
// flag without a row would set nothing.
func TestEveryFieldHasAFlagAndEveryFlagAField(t *testing.T) {
	cmd := modelCommand(Options{})
	for _, f := range fields {
		flag := cmd.Flags().Lookup(f.name)
		if flag == nil {
			t.Errorf("field %q has no flag", f.name)
			continue
		}
		if flag.NoOptDefVal != askSentinel {
			t.Errorf("--%s cannot be given bare; NoOptDefVal = %q", f.name, flag.NoOptDefVal)
		}
	}
}

// A head flag expands to its group; anything else expands to itself and to
// whatever it is only meaningful beside.
func TestScopeOfExpandsHeadsAndPairs(t *testing.T) {
	for _, tt := range []struct {
		name string
		want []string
	}{
		{"dca", []string{"dca", "dca-chunk"}},
		{"slots", []string{"slots", "slots-max", "slots-tps-floor", "slots-vram-reserve", "slots-swa-budget"}},
		{"kv-k", []string{"kv-k", "kv-v"}},
		{"kv-residency", []string{"kv-residency"}},
	} {
		got := scopeOf(tt.name)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("scopeOf(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Every group a head names must exist in the table, or a bare flag would scope
// the walk to a question that is never asked.
func TestEveryGroupMemberIsARealField(t *testing.T) {
	for _, f := range fields {
		for _, n := range append(append([]string{}, f.group...), f.with...) {
			if _, ok := fieldByName(n); !ok {
				t.Errorf("field %q names %q, which is not in the table", f.name, n)
			}
		}
	}
}

// What is printed as JSON is what would be stored, version stamp and all.
func TestTheJSONShownIsTheJSONStored(t *testing.T) {
	cfg := &xollama.Config{KV: &xollama.KV{Unified: boolp(true)}}
	data, err := marshalForDisplay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) {
		t.Fatalf("a v2 field must stamp v2:\n%s", data)
	}

	cfg = &xollama.Config{Engine: xollama.EngineOpencoti}
	data, err = marshalForDisplay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("a v1-only config must stay readable by a v1 build:\n%s", data)
	}
}

// Sub-structs that state nothing are pruned, so a walk that answers everything
// "unset" clears the layer instead of storing `{"kv":{}}`.
func TestAnEmptiedConfigIsZeroRatherThanFullOfEmptyObjects(t *testing.T) {
	cfg := &xollama.Config{}
	for _, f := range fields {
		if err := f.set(cfg, ""); err != nil {
			t.Fatalf("clearing %s: %v", f.name, err)
		}
	}
	prune(cfg)
	if !cfg.IsZero() {
		t.Fatalf("want a zero config, got %+v", cfg)
	}
}

// menuIndex must not swallow a value that happens to look like a number: 4096
// is a chunk size, not menu entry 4096.
func TestAMenuNumberIsOnlyAMenuNumberInRange(t *testing.T) {
	if _, ok := menuIndex("4096", 3); ok {
		t.Fatal("4096 is not one of three options")
	}
	if _, ok := menuIndex("2", 3); !ok {
		t.Fatal("2 is one of three options")
	}
	if _, ok := menuIndex("0", 3); ok {
		t.Fatal("menus are 1-based")
	}
	if _, ok := menuIndex("2x", 3); ok {
		t.Fatal("2x is not a menu number")
	}
}

// A refusal at the end of an interactive walk re-asks only what it names. The
// alternative is throwing away fifteen answers because the sixteenth stranded
// one of them.
func TestARefusalReAsksOnlyTheSettingsItNames(t *testing.T) {
	current := &xollama.Config{
		Engine: xollama.EngineOpencoti,
		KV:     &xollama.KV{K: "kvarn3", V: "kvarn3"},
	}

	// Pin the stock engine, which cannot serve a KVarN cache; then answer the
	// two questions the refusal names.
	// llamacpp, then the two cache types the refusal names. The engine is not
	// asked again: the refusal mentions it as prose, and the setting it is
	// about is the cache type.
	cfg, out, err := run(t, current, []string{"--engine", "--yes"}, "llamacpp", "q8_0", "q8_0")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "is not a cache type stock llama.cpp accepts") {
		t.Fatalf("want the refusal shown\n%s", out)
	}
	if !strings.Contains(out, "kv-k [kvarn3]> ") || !strings.Contains(out, "kv-v [kvarn3]> ") {
		t.Fatalf("want kv.k and kv.v asked again\n%s", out)
	}
	if strings.Contains(out, "dca [") || strings.Contains(out, "slots [") {
		t.Fatalf("a refusal must not re-ask settings it did not name\n%s", out)
	}
	if strings.Count(out, "engine [") != 1 {
		t.Fatalf("the engine was asked %d times; prose mentioning it is not a reason to re-ask\n%s",
			strings.Count(out, "engine ["), out)
	}
	if cfg.KV.K != "q8_0" || cfg.KV.V != "q8_0" || cfg.Engine != xollama.EngineLlamaCpp {
		t.Fatalf("the fix did not land: %+v", cfg)
	}
}

// The same refusal from a script is an error and a non-zero exit, because
// there is nobody to ask and guessing a cache type changes the model's output.
func TestARefusalFromAScriptIsAnError(t *testing.T) {
	current := &xollama.Config{Engine: xollama.EngineOpencoti, KV: &xollama.KV{K: "kvarn3", V: "kvarn3"}}

	if _, _, err := run(t, current, []string{"--engine=llamacpp"}); err == nil {
		t.Fatal("want a refusal with no operator present")
	}
}

// Longest path first: a refusal about the ring must not drag in the base cache
// just because "kv.k" is a prefix of "kv.k_swa".
func TestARefusalNamesTheRingWithoutTheBase(t *testing.T) {
	got := fieldsNamedIn(errors.New(`kv.k_swa = "q4_0" and kv.v_swa = "kvarn4" must both be KVarN widths`))
	if strings.Join(got, ",") != "kv-swa-k,kv-swa-v" {
		t.Fatalf("fieldsNamedIn = %v, want just the ring halves", got)
	}
}

// A fallback this table names must be a variable the server actually reads, or
// the wizard tells an operator that leaving a setting unset hands it to
// something that does not exist.
func TestEveryNamedFallbackIsARealEnvironmentVariable(t *testing.T) {
	all := envconfig.AsMap()
	for _, f := range fields {
		if f.env == "" {
			continue
		}
		if _, ok := all[f.env]; !ok {
			t.Errorf("field %q names %s, which envconfig does not declare", f.name, f.env)
		}
	}
	if got := len(FallbackEnvVars(all)); got == 0 {
		t.Fatal("no fallbacks listed at all; the help would say nothing")
	}
}
