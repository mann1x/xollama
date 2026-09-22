package tweak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// askSentinel is what a flag given with no value carries.
//
// cobra's NoOptDefVal is how one flag spells two different requests: --dca
// scopes the wizard to the DCA questions, --dca=on sets that one field and asks
// nothing. Both are drivable from a script, which is the requirement -- the
// first with expect, the second without needing it at all.
//
// It is not a value any field accepts, so a literal --dca='<ask>' still lands
// in the scoping branch rather than being mistaken for a setting.
const askSentinel = "<ask>"

// Options are the pieces the command needs from the CLI package it is
// registered in, passed rather than imported so this package does not depend
// on cmd (which depends on it).
type Options struct {
	// Heartbeat checks the server is up and starts it if it is not, the same
	// check every other model-touching command makes.
	Heartbeat func(cmd *cobra.Command, args []string) error
}

// Command returns `xollama tweak`.
func Command(opts Options) *cobra.Command {
	tweakCmd := &cobra.Command{
		Use:   "tweak",
		Short: "Adjust xollama's own settings on a model",
		Long: `Adjust xollama's own settings on a model.

These are the settings the fork adds and upstream ollama has no field for -- the
engine pin, KV cache types, dynamic slots, DCA, session affinity and prefix
pooling. They live in the model's xollama.json layer, which a stock ollama
carries unchanged and ignores, so a model configured here still runs there.

The layer could only be written by hand before this command: a Modelfile with a
XOLLAMA directive holding JSON, which meant knowing the field names, the value
sets and which settings need which others.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	tweakCmd.AddCommand(modelCommand(opts))
	return tweakCmd
}

func modelCommand(opts Options) *cobra.Command {
	modelCmd := &cobra.Command{
		Use:   "model MODEL",
		Short: "Set xollama's model settings, interactively or by flag",
		Long: `Set xollama's model settings on MODEL.

With no flags it walks every setting, showing what the model states now and what
each one does, and offers to modify that configuration, start from scratch, or
clear it.

A flag given a value sets that one setting and asks nothing:

    xollama tweak model qwen3.6:latest --dca=on
    xollama tweak model qwen3.6:latest --kv-k=kvarn4 --kv-v=kvarn4

The same flag given no value scopes the walk to that feature, leaving everything
else untouched:

    xollama tweak model qwen3.6:latest --dca

Settings the model cannot act on are dropped, each named with its reason: an
opencoti-only setting on a model that pins llamacpp, a chunk length under a DCA
switch that is off. Combinations where only you know which half you meant are
refused instead, because choosing for you would serve the model differently from
how you configured it.

The result replaces the model's xollama.json layer. Nothing else about the model
changes -- its weights, template, system prompt and parameters are untouched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModel(cmd, args, opts)
		},
	}

	// One flag per row of the table, so the two cannot drift.
	for _, f := range fields {
		modelCmd.Flags().String(f.name, "", flagUsage(f))
		modelCmd.Flags().Lookup(f.name).NoOptDefVal = askSentinel
	}
	modelCmd.Flags().Bool("clear", false, "Remove the model's xollama config entirely")
	modelCmd.Flags().Bool("dry-run", false, "Show the resulting config without writing it")
	modelCmd.Flags().Bool("json", false, "Print the resulting xollama.json to stdout")
	modelCmd.Flags().BoolP("yes", "y", false, "Do not ask for confirmation before writing")

	return modelCmd
}

// flagUsage keeps `--help` in step with the table: the values a flag takes are
// the values the wizard offers.
func flagUsage(f field) string {
	var values string
	switch f.kind {
	case kindTri:
		values = "on|off|unset"
	case kindChoice, kindOpenChoice:
		vals := f.choices(&xollama.Config{})
		if f.kind == kindOpenChoice && len(vals) > 4 {
			vals = append(vals[:4:4], "...")
		}
		values = strings.Join(append(vals, "unset"), "|")
	case kindInt, kindFloat:
		values = "N|unset"
	}
	return fmt.Sprintf("%s (%s); bare asks", f.path, values)
}

func runModel(cmd *cobra.Command, args []string, opts Options) error {
	name := args[0]
	if !model.ParseName(name).IsValid() {
		return fmt.Errorf("invalid model name: %s", name)
	}

	if opts.Heartbeat != nil {
		if err := opts.Heartbeat(cmd, args); err != nil {
			return err
		}
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return err
	}

	current, err := showConfig(cmd.Context(), client, name)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()

	cfg, err := build(cmd, name, current, newAsker(in, out))
	if err != nil {
		if errors.Is(err, errQuit) {
			fmt.Fprintf(out, "\nnothing written.\n")
			return nil
		}
		return err
	}
	return write(cmd, client, name, cfg, out)
}

// showConfig reads what the model states today. /api/show carries the layer
// (api.ShowResponse.Xollama), so this works against a remote server too; the
// manifest is not read directly for exactly that reason.
func showConfig(ctx context.Context, client *api.Client, name string) (*xollama.Config, error) {
	resp, err := client.Show(ctx, &api.ShowRequest{Model: name})
	if err != nil {
		return nil, err
	}
	if resp.Xollama == nil {
		return &xollama.Config{}, nil
	}
	return clone(resp.Xollama), nil
}

// build turns the flags and, where they ask for it, the operator's answers into
// the config to write. It returns errQuit when the operator stops.
func build(cmd *cobra.Command, name string, current *xollama.Config, a *asker) (*xollama.Config, error) {
	out := a.out

	clearAll, _ := cmd.Flags().GetBool("clear")
	if clearAll {
		if cmd.Flags().NFlag() > countBoolFlags(cmd) {
			return nil, errors.New("--clear removes every setting, so it cannot be combined with a flag that sets one")
		}
		fmt.Fprintf(out, "clearing the xollama config on %s.\n", name)
		return &xollama.Config{}, nil
	}

	set, scope := partitionFlags(cmd)
	cfg := clone(current)

	// Valued flags first, so a walk scoped by a bare flag sees them.
	for _, s := range set {
		f, _ := fieldByName(s.name)
		if err := f.set(cfg, s.value); err != nil {
			return nil, fmt.Errorf("--%s: %w", f.name, err)
		}
	}
	prune(cfg)

	switch {
	case len(scope) > 0:
		// Scoped walk: only the named features, everything else left alone.
		if err := walk(a, cfg, scope); err != nil {
			return nil, err
		}
	case len(set) > 0:
		// Fully non-interactive. Nothing to ask.
	default:
		var err error
		cfg, err = walkAll(a, cfg, current, name)
		if err != nil {
			return nil, err
		}
	}

	fmt.Fprintf(out, "\n── Consistency\n")
	before := len(statedFields(cfg))
	if err := settle(a, cfg); err != nil {
		return nil, err
	}
	if before == len(statedFields(cfg)) {
		fmt.Fprintf(out, "   nothing dropped; every setting is one this model can act on.\n")
	}

	a.review(cfg, out)

	yes, _ := cmd.Flags().GetBool("yes")
	if a.answered > 0 && !yes {
		ok, err := a.confirm("write", fmt.Sprintf("Write this to %s?", name))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errQuit
		}
	}

	// Send exactly what would be stored, schema stamp included. The server
	// stamps it again on the way into the layer, so this is belt and braces --
	// but it also means --json prints the bytes that land, and that a config
	// this command accepts is one the create path has already agreed to.
	if !cfg.IsZero() {
		data, err := cfg.Marshal()
		if err != nil {
			return nil, err
		}
		stamped, err := xollama.Parse(data)
		if err != nil {
			return nil, err
		}
		cfg = stamped
	}
	return cfg, nil
}

// settle runs the consistency pass, and where the operator is present, offers
// to fix what it refuses instead of throwing the walk away.
//
// Only the settings the refusal NAMES are re-asked. That is the difference
// between a consistency check that helps and one that punishes: a run that
// answered fifteen questions and then pinned an engine that cannot serve one
// cache type should be asked about that cache type, not about all fifteen
// again. A run driven entirely by flags has nobody to ask, so it gets the
// refusal and a non-zero exit, which is what a script needs.
func settle(a *asker, cfg *xollama.Config) error {
	for attempt := range 3 {
		err := reconcile(cfg, a.out)
		if err == nil {
			return nil
		}
		named := fieldsNamedIn(err)
		if a.answered == 0 || len(named) == 0 || attempt == 2 {
			return err
		}
		a.printf("   ! %s\n", strings.TrimPrefix(err.Error(), "xollama config: "))
		a.printf("   the settings it names are asked again; `unset` clears one.\n")
		if err := walk(a, cfg, named); err != nil {
			return err
		}
	}
	return nil
}

// walkAll is the no-flags path: it offers what to do with an existing config
// before asking anything, because "start from scratch" and "modify" are
// different runs and asking twenty questions to find that out is worse.
func walkAll(a *asker, cfg, current *xollama.Config, name string) (*xollama.Config, error) {
	if !current.IsZero() {
		a.printf("\n%s states:\n", name)
		for _, s := range statedFields(current) {
			a.printf("   %-24s %s\n", s[0], s[1])
		}
		choice, err := a.menu("start", "This model already has an xollama config", [][2]string{
			{"modify", "modify it -- every question starts from what is there"},
			{"scratch", "start from scratch -- every question starts unset"},
			{"clear", "clear it -- remove the config entirely and write nothing else"},
			{"quit", "quit -- change nothing"},
		}, 0)
		if err != nil {
			return nil, err
		}
		switch choice {
		case "scratch":
			cfg = &xollama.Config{}
		case "clear":
			return &xollama.Config{}, nil
		case "quit":
			return nil, errQuit
		}
	}

	all := make([]string, 0, len(fields))
	for _, f := range fields {
		all = append(all, f.name)
	}
	if err := walk(a, cfg, all); err != nil {
		return nil, err
	}
	return cfg, nil
}

// walk asks the named fields in table order, skipping the ones this config
// cannot act on and saying why -- the skip is the consistency check happening
// during setup rather than only at the end.
func walk(a *asker, cfg *xollama.Config, names []string) error {
	for _, f := range fields {
		if !contains(names, f.name) {
			continue
		}
		if f.blocked != nil {
			if why := f.blocked(cfg); why != "" {
				a.printf("\n   skipped %s: %s\n", f.path, why)
				continue
			}
		}
		if err := a.ask(cfg, f); err != nil {
			return err
		}
		prune(cfg)
	}
	return nil
}

type setFlag struct {
	name  string
	value string
}

// partitionFlags splits the field flags into the ones carrying a value and the
// ones given bare, which name the features to walk.
func partitionFlags(cmd *cobra.Command) (set []setFlag, scope []string) {
	for _, f := range fields {
		flag := cmd.Flags().Lookup(f.name)
		if flag == nil || !flag.Changed {
			continue
		}
		if flag.Value.String() == askSentinel {
			for _, n := range scopeOf(f.name) {
				if !contains(scope, n) {
					scope = append(scope, n)
				}
			}
			continue
		}
		set = append(set, setFlag{name: f.name, value: flag.Value.String()})
	}
	return set, scope
}

// countBoolFlags counts the flags that are not settings, so --clear can tell
// "--clear alone" from "--clear --dca=on".
func countBoolFlags(cmd *cobra.Command) int {
	n := 0
	for _, name := range []string{"clear", "dry-run", "json", "yes"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			n++
		}
	}
	return n
}

// write replaces the model's xollama.json layer, and nothing else.
//
// It goes through the ordinary create path with From set to the model itself:
// the layers are read, the config layer is swapped, and a new manifest is
// written. An empty config removes the layer rather than writing an empty one
// (create.ApplyModelfileLayers), which is what makes `--clear` expressible.
func write(cmd *cobra.Command, client *api.Client, name string, cfg *xollama.Config, out io.Writer) error {
	if show, _ := cmd.Flags().GetBool("json"); show {
		data, err := marshalForDisplay(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s\n", data)
	}

	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		fmt.Fprintf(out, "\n--dry-run: %s not changed.\n", name)
		return nil
	}

	req := &api.CreateRequest{
		Model: name,
		From:  name,
		// Non-nil and empty are different requests here: nil would inherit the
		// layer this command was asked to change.
		Xollama: cfg,
	}
	fn := func(resp api.ProgressResponse) error {
		if resp.Status != "" {
			fmt.Fprintf(out, "   %s\n", resp.Status)
		}
		return nil
	}
	fmt.Fprintf(out, "\nwriting %s\n", name)
	if err := client.Create(cmd.Context(), req, fn); err != nil {
		return err
	}
	if cfg.IsZero() {
		fmt.Fprintf(out, "%s no longer carries an xollama config.\n", name)
	} else {
		fmt.Fprintf(out, "%s updated. `xollama show %s` reports it.\n", name, name)
	}
	return nil
}

// marshalForDisplay renders what would be stored, version stamp and all, so
// what is printed is what lands in the layer.
func marshalForDisplay(cfg *xollama.Config) ([]byte, error) {
	if cfg.IsZero() {
		return []byte("{}  // empty: the xollama.json layer is removed"), nil
	}
	data, err := cfg.Marshal()
	if err != nil {
		return nil, err
	}
	var pretty map[string]any
	if err := json.Unmarshal(data, &pretty); err != nil {
		return nil, err
	}
	return json.MarshalIndent(pretty, "", "  ")
}
