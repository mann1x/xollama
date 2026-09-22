package tweak

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// reconcile drops the settings this config cannot act on and says which, then
// reports whatever is still wrong with what remains.
//
// The two halves are different in kind, and keeping them apart is the point:
//
//   - A setting is DROPPED when it is meaningless on its own terms -- an
//     opencoti-only flag on a model that pins llamacpp, a chunk length under a
//     DCA switch that is off. There is nothing for the operator to decide: the
//     setting cannot do anything, and leaving it in the file would read as if
//     it did. Each drop is named, with the reason.
//   - A setting is an ERROR when a combination is wrong and only the operator
//     knows which half they meant. kv.unified off beside dynamic slots is a
//     real choice between two features, not a redundancy; picking one here
//     would silently serve a model differently from how it was configured.
//
// It runs to a fixed point because dropping one setting can strand another --
// switching the engine to llamacpp drops the ring types, which can strand the
// KVarN base they required.
func reconcile(cfg *xollama.Config, w io.Writer) error {
	if err := dropBlocked(cfg, w); err != nil {
		return err
	}
	if err := validate(cfg); err != nil {
		return err
	}
	if why := cacheShape(cfg); why != "" {
		return fmt.Errorf("xollama config: %s", why)
	}
	return nil
}

// dropBlocked removes the settings this config cannot act on, naming each.
//
// It runs to a fixed point because dropping one setting can strand another --
// switching the engine to llamacpp drops the ring types, which can strand the
// KVarN base they required.
func dropBlocked(cfg *xollama.Config, w io.Writer) error {
	for range len(fields) + 1 {
		dropped := false
		for _, f := range fields {
			if f.blocked == nil || f.get(cfg) == "" {
				continue
			}
			if why := f.blocked(cfg); why != "" {
				fmt.Fprintf(w, "   dropped %s = %s: %s\n", f.path, f.get(cfg), why)
				if err := f.set(cfg, ""); err != nil {
					return fmt.Errorf("%s: %w", f.path, err)
				}
				dropped = true
			}
		}
		prune(cfg)
		if !dropped {
			break
		}
	}
	return nil
}

// answerFault reports what is wrong with a trial config AS A RESULT OF this
// field's answer, or "" when the answer is one to accept.
//
// Two rules make this the right question rather than "is the config valid".
//
// First, the drop pass runs on the trial before anything is judged: an answer
// whose only consequence is that some other setting is now unusable has not
// done anything wrong, and that setting is about to be dropped and named. An
// operator switching to the stock engine cannot be made to clear the four
// opencoti settings by hand first -- there is no way to do it from inside the
// question they are being asked.
//
// Second, only a fault that NAMES this setting is the operator's to fix here.
// A cache pair is refused as a pair, so blocking a good answer to kv.k because
// kv.v has not moved yet would make the pair unfixable one question at a time.
// Anything left is caught by the consistency pass, which re-asks what it names.
func answerFault(trial *xollama.Config, f field) string {
	// Judged on a copy, so the drops themselves still happen in the
	// consistency pass where they are NAMED. Dropping them here would mean the
	// answer was accepted and something else quietly disappeared with it.
	judged := clone(trial)
	if err := dropBlocked(judged, io.Discard); err != nil {
		return err.Error()
	}
	if err := validate(judged); err != nil {
		if contains(fieldsNamedIn(err), f.name) {
			return strings.TrimPrefix(err.Error(), "xollama config: ")
		}
	}
	if why := cacheShape(judged); why != "" {
		if contains(fieldsNamedIn(errors.New(why)), f.name) {
			return why
		}
	}
	return ""
}

// fieldsNamedIn returns the fields a refusal mentions, in table order, so an
// interactive run can re-ask exactly those rather than abandoning the walk.
//
// Every message from Validate and from the cache rules names the JSON paths it
// is about, which is what makes this reliable rather than a guess. Longer paths
// are matched and removed first: "kv.k" is a prefix of "kv.k_swa", and a
// refusal about the ring must not drag in the base cache as well.
func fieldsNamedIn(err error) []string {
	text := err.Error()
	byLength := make([]field, len(fields))
	copy(byLength, fields)
	sort.SliceStable(byLength, func(i, j int) bool { return len(byLength[i].path) > len(byLength[j].path) })

	named := map[string]bool{}
	for _, f := range byLength {
		if namesField(text, f.path) {
			named[f.name] = true
			text = strings.ReplaceAll(text, f.path, "")
		}
	}

	var out []string
	for _, f := range fields {
		if named[f.name] {
			out = append(out, f.name)
		}
	}
	return out
}

// namesField reports whether a message is ABOUT a setting, rather than merely
// containing its name.
//
// A dotted path is unambiguous: nothing says "kv.k" in passing. A top-level one
// is a plain English word -- "engine" appears in half these messages as prose
// ("needs the opencoti engine") -- so it counts only where it is used as a
// setting: followed by its value, or named as unknown. Getting this wrong is
// not cosmetic: a false match re-asks a question the operator already answered,
// and the answer they typed for the next question lands in it.
func namesField(text, path string) bool {
	if strings.Contains(path, ".") {
		return strings.Contains(text, path)
	}
	for _, form := range []string{path + " =", path + " \"", "unknown " + path} {
		if strings.Contains(text, form) {
			return true
		}
	}
	return false
}

// validate checks a config the way storage would.
//
// It goes through Marshal rather than calling Validate directly because the
// version field is COMPUTED at storage time, from the fields the config
// actually uses -- so a config being edited carries version 0 and Validate,
// reasonably, refuses it. Validating the way it will be stored also means this
// command cannot accept something the create path would then reject.
func validate(cfg *xollama.Config) error {
	_, err := cfg.Marshal()
	return err
}

// cacheShape applies the launch path's own cache rules at configuration time.
//
// These are MEASURED preconditions of the engine (llm.CacheShapeError names the
// probe rows), and they are not in xollama.Config.Validate deliberately: that
// package refuses to know which cache types exist, because the set depends on
// the engine serving the load and a model published by a newer xollama must
// stay creatable on an older one. Checking here costs nothing -- a config that
// breaks these would be refused at load, which reaches the operator as a model
// that will not start rather than as a line to change.
func cacheShape(cfg *xollama.Config) string {
	if cfg.KV == nil {
		return ""
	}
	if why := llm.CacheShapeError(cfg.KV.K, cfg.KV.V, cfg.KV.KSWA, cfg.KV.VSWA); why != "" {
		return why
	}
	// A KVarN cache is not servable with flash attention off -- the engine
	// says so and exits. "auto" is left alone: it resolves against the devices
	// at load time and is usually on, and refusing it here would refuse the
	// configuration most models should have.
	if llm.CacheNeedsFlashAttention(cfg.KV.K, cfg.KV.V, cfg.KV.KSWA, cfg.KV.VSWA) && cfg.FlashAttention == "off" {
		return "a KVarN cache type needs flash attention, and flash_attention is off; the engine refuses the pair at startup"
	}
	// Only a model that PINS the stock engine is refused. One that states no
	// engine may still be served by opencoti, and the operator who typed a
	// kvarn width has said which they expect.
	if cfg.Engine == xollama.EngineLlamaCpp {
		if why := llm.CacheTypesNeedOpencoti(cfg.KV.K, cfg.KV.V, cfg.KV.KSWA, cfg.KV.VSWA); why != "" {
			return why + "; this model pins engine llamacpp"
		}
	}
	return ""
}
