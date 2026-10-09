package tweak

import (
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/types/xollama"
)

var drafterSetByTest bool

// withDrafter is what the server said about the model under test.
func withDrafter(t *testing.T, d *api.DrafterInfo) {
	t.Helper()
	useModelDrafter(d)
	drafterSetByTest = true
	t.Cleanup(func() { modelDrafter, drafterKnown, drafterSetByTest = nil, false, false })
}

const a4bHead = "hf.co/ManniX-ITA/gemma-4-26B-A4B-it-assistant-GGUF/gemma-4-26B-A4B-it-assistant.Q8_0.gguf"

var a4bDigest = "sha256:" + strings.Repeat("5", 64)

// noDrafterA4B is a Gemma 4 26B-A4B with nothing attached: the model the
// pin was written on.
func noDrafterA4B() *api.DrafterInfo {
	return &api.DrafterInfo{
		Source:      api.DrafterSourceNone,
		Recommended: &api.DrafterRecommendation{Target: "26B-A4B", Source: a4bHead, SpecType: "draft-assistant", Tokens: 2},
	}
}

func resolveTo(t *testing.T, digest string) *[]string {
	t.Helper()
	var asked []string
	old := hubResolve
	hubResolve = func(_ context.Context, r mediahub.Ref) (mediahub.File, error) {
		asked = append(asked, r.String())
		return mediahub.File{Ref: r, Digest: digest}, nil
	}
	t.Cleanup(func() { hubResolve = old })
	return &asked
}

// The report this file exists for: `--spec-type=draft-assistant` on a model
// with no drafter was written, and the run said every setting was one the
// model could act on. A driver with no drafter behind it does nothing, so it
// is dropped and the reason names the way to attach one.
func TestADriverPinnedWithoutADrafterIsDroppedAndSaysHowToAttachOne(t *testing.T) {
	withDrafter(t, noDrafterA4B())
	cfg, out, err := run(t, &xollama.Config{}, []string{"--spec-type=draft-assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft != nil {
		t.Fatalf("draft = %+v, want nothing written", cfg.Draft)
	}
	for _, want := range []string{"draft.spec_type", "this model has none", "--drafter=auto"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the drop does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "every setting is one this model can act on") {
		t.Fatalf("a dropped pin was reported as kept:\n%s", out)
	}
}

// auto is the published drafter for THIS model, by the reference the server
// gave, and the driver pin beside it is then one the model can act on.
func TestDrafterAutoAttachesTheOnePublishedForTheModel(t *testing.T) {
	withDrafter(t, noDrafterA4B())
	asked := resolveTo(t, a4bDigest)

	cfg, out, err := run(t, &xollama.Config{}, []string{"--drafter=auto", "--spec-type=draft-assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 1 || (*asked)[0] != a4bHead {
		t.Fatalf("resolved %v, want %s", *asked, a4bHead)
	}
	if cfg.Draft == nil || cfg.Draft.Head != a4bDigest {
		t.Fatalf("draft.head = %+v, want %s", cfg.Draft, a4bDigest)
	}
	if cfg.Draft.SpecType != "draft-assistant" {
		t.Fatalf("the pin was dropped although a drafter is being attached:\n%s", out)
	}
	if mediaSources[a4bDigest] != a4bHead {
		t.Fatalf("write would not know where to fetch the drafter from: %v", mediaSources)
	}
	// The length measured best for it comes with it.
	if cfg.Draft.Tokens == nil || *cfg.Draft.Tokens != 2 {
		t.Fatalf("draft.tokens = %v, want the published drafter's 2", cfg.Draft.Tokens)
	}
}

// A length the model already states is the operator's, and auto leaves it.
func TestDrafterAutoKeepsALengthTheModelStates(t *testing.T) {
	withDrafter(t, noDrafterA4B())
	resolveTo(t, a4bDigest)
	four := 4
	cfg, _, err := run(t, &xollama.Config{Draft: &xollama.Draft{Tokens: &four}}, []string{"--drafter=auto"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft.Tokens == nil || *cfg.Draft.Tokens != 4 {
		t.Fatalf("draft.tokens = %v, want the model's own 4", cfg.Draft.Tokens)
	}
}

func TestARunThatChangesNothingIsToldApart(t *testing.T) {
	three := 3
	a := &xollama.Config{Draft: &xollama.Draft{Tokens: &three}}
	if !unchanged(a, clone(a)) || !unchanged(&xollama.Config{}, &xollama.Config{Draft: &xollama.Draft{}}) {
		t.Fatal("the same config was called a change")
	}
	if unchanged(a, &xollama.Config{}) || unchanged(&xollama.Config{}, a) {
		t.Fatal("a change was called nothing")
	}
}

func TestDrafterAutoIsRefusedWhereNoneIsPublished(t *testing.T) {
	withDrafter(t, nil)
	_, _, err := run(t, &xollama.Config{}, []string{"--drafter=auto"})
	if err == nil || !strings.Contains(err.Error(), "no drafter is published") {
		t.Fatalf("err = %v, want a refusal naming the reason", err)
	}
}

// none detaches the drafter a model carries; on a model without one it
// states nothing, so nothing is written.
func TestDrafterNoneDetachesOnlyWhatIsAttached(t *testing.T) {
	withDrafter(t, &api.DrafterInfo{Source: "attached", Digest: a4bDigest, SpecType: "draft-assistant"})
	cfg, _, err := run(t, &xollama.Config{Draft: &xollama.Draft{Head: a4bDigest}}, []string{"--drafter=none"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft == nil || cfg.Draft.Head != xollama.DraftHeadNone {
		t.Fatalf("draft = %+v, want head none", cfg.Draft)
	}

	withDrafter(t, noDrafterA4B())
	cfg, _, err = run(t, &xollama.Config{}, []string{"--drafter=none"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft != nil {
		t.Fatalf("draft = %+v, want nothing", cfg.Draft)
	}
}

// 0 is a value here, not the absence of one: it is what turns drafting off.
func TestDraftTokensZeroIsOffAndUnsetIsNothing(t *testing.T) {
	withDrafter(t, &api.DrafterInfo{Source: "built-in", SpecType: "draft-mtp", TokensFrom: "engine"})
	cfg, _, err := run(t, &xollama.Config{}, []string{"--draft-tokens=0"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft == nil || cfg.Draft.Tokens == nil || *cfg.Draft.Tokens != 0 {
		t.Fatalf("draft = %+v, want tokens 0", cfg.Draft)
	}

	three := 3
	cfg, _, err = run(t, &xollama.Config{Draft: &xollama.Draft{Tokens: &three}}, []string{"--draft-tokens=unset"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft != nil {
		t.Fatalf("draft = %+v, want nothing", cfg.Draft)
	}

	if _, _, err := run(t, &xollama.Config{}, []string{"--draft-tokens=65"}); err == nil {
		t.Fatal("a draft length past the limit was accepted")
	}
}

// The menu the report quoted listed six drivers and said nothing about which
// one was this model's. It names it now, and says unset already gives it.
func TestTheSpecTypeMenuNamesThisModelsDriver(t *testing.T) {
	withDrafter(t, &api.DrafterInfo{
		Source: "attached", Architecture: "gemma4-assistant", QuantizationLevel: "Q8_0", Digest: a4bDigest, SpecType: "draft-assistant",
		Recommended: &api.DrafterRecommendation{Target: "26B-A4B", Source: a4bHead, SpecType: "draft-assistant", Attached: true},
	})
	_, out, err := run(t, &xollama.Config{Draft: &xollama.Draft{Head: a4bDigest}}, []string{"--spec-type", "--yes"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"this model: drafter attached (gemma4-assistant Q8_0)",
		"its driver, read from the drafter's own metadata: draft-assistant",
		"draft-assistant  <- this model's driver",
		"unset  <- recommended",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the menu does not say %q:\n%s", want, out)
		}
	}
}

// The policy is the engine's for a driver it chose itself, which it does
// only for a head inside the weights.
func TestTheMTPPolicyIsNotAskedOfAnAttachedDrafter(t *testing.T) {
	withDrafter(t, &api.DrafterInfo{Source: "attached", Digest: a4bDigest, SpecType: "draft-assistant"})
	cfg, out, err := run(t, &xollama.Config{Draft: &xollama.Draft{Head: a4bDigest}}, []string{"--mtp-policy=off"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft.AutoMTPPolicy != "" {
		t.Fatalf("draft.auto_mtp_policy = %q, want dropped", cfg.Draft.AutoMTPPolicy)
	}
	if !strings.Contains(out, "an attached drafter always drafts at full depth") {
		t.Fatalf("the drop does not say why:\n%s", out)
	}

	withDrafter(t, &api.DrafterInfo{Source: "built-in", SpecType: "draft-mtp"})
	cfg, _, err = run(t, &xollama.Config{}, []string{"--mtp-policy=off"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft == nil || cfg.Draft.AutoMTPPolicy != "off" {
		t.Fatalf("draft = %+v, want the policy kept on a built-in head", cfg.Draft)
	}
}

// --drafter alone walks the whole section, in the order someone decides it.
func TestABareDrafterFlagWalksTheSection(t *testing.T) {
	withDrafter(t, noDrafterA4B())
	resolveTo(t, a4bDigest)
	cfg, out, err := run(t, &xollama.Config{}, []string{"--drafter", "--yes"}, "auto", "3", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Draft == nil || cfg.Draft.Head != a4bDigest || cfg.Draft.Tokens == nil || *cfg.Draft.Tokens != 3 || cfg.Draft.SpecType != "" {
		t.Fatalf("draft = %+v", cfg.Draft)
	}
	for _, want := range []string{"published for it: " + a4bHead, "`auto` fetches and attaches that one", "this run: attaches " + a4bHead, "draft-tokens [2]> ", "spec-type [unset]> "} {
		if !strings.Contains(out, want) {
			t.Fatalf("the walk does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mtp-policy [") {
		t.Fatalf("the policy was asked of a model with no built-in head:\n%s", out)
	}
}

// A drafter is one model's file; the length it drafts at can be a default.
func TestTheServerDefaultsTakeTheLengthAndNotTheDrafter(t *testing.T) {
	names := serverFields()
	has := func(n string) bool {
		for _, s := range names {
			if s == n {
				return true
			}
		}
		return false
	}
	if has("drafter") {
		t.Fatal("the drafter is offered as a server default")
	}
	if !has("draft-tokens") || !has("spec-type") {
		t.Fatalf("the draft length or the driver is missing from the server defaults: %v", names)
	}
}

func TestDrafterRowsSayWhatIsPublishedForAModelWithout(t *testing.T) {
	rows := DrafterRows(noDrafterA4B())
	var flat []string
	for _, r := range rows {
		flat = append(flat, strings.Join(r[1:], "="))
	}
	got := strings.Join(flat, "|")
	want := "source=none|published=" + a4bHead + "|attach it=--drafter=auto"
	if got != want {
		t.Fatalf("rows = %s\nwant   %s", got, want)
	}
}
