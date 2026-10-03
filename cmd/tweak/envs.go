package tweak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
)

// envsCommand is `xollama tweak envs`: the server's environment variables,
// overridden in its settings file rather than in the global environment
// (plans/system-settings.md).
func envsCommand(opts Options) *cobra.Command {
	envsCmd := &cobra.Command{
		Use:   "envs [NAME=VALUE ...]",
		Short: "Override the server's environment variables without touching the environment",
		Long: `Override the server's environment variables in its own settings file.

Upstream configures a server through environment variables: a change means the
global environment or a systemd override, the documentation to find the name,
and a restart. An override set here is kept by the server itself
(~/.ollama/xollama-settings.json in the server's home) and wins over the same
variable in the environment, under either spelling (OLLAMA_ or XOLLAMA_).

    xollama tweak envs OLLAMA_KV_CACHE_TYPE=q8_0 XOLLAMA_MAX_PARALLEL=4
    xollama tweak envs --unset OLLAMA_KV_CACHE_TYPE
    xollama tweak envs                     ask
    xollama tweak show envs                what is set, and where it comes from

Most variables apply at once, or at the next model load; the command offers to
unload the running models. A few are read only when the server starts -- the
listen address, the models directory, origins, GPU visibility -- and the
command says so. Variables the engines read themselves (LLAMA_ARG_*,
OPENCOTI_*, CUDA_VISIBLE_DEVICES and the like) are handed to every engine the
server starts.

The API key cannot be set here: ` + "`xollama tweak server --api-key`" + ` keeps only
its digest on disk. Only a client on the server's own machine can change the
settings, never through a proxy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvs(cmd, args, opts)
		},
	}
	envsCmd.Flags().StringSlice("unset", nil, "Remove the override for NAME, so the environment applies again")
	envsCmd.Flags().BoolP("yes", "y", false, "Unload the running models without asking when a change needs it")
	return envsCmd
}

func runEnvs(cmd *cobra.Command, args []string, opts Options) error {
	if opts.Heartbeat != nil {
		if err := opts.Heartbeat(cmd, nil); err != nil {
			return err
		}
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	a := newAsker(cmd.InOrStdin(), out)
	ctx := cmd.Context()

	cur, err := client.Settings(ctx, nil)
	if err != nil {
		return err
	}

	changes, err := envChangesFromArgs(cmd, args)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		if changes, err = askEnvChanges(a, cur); err != nil {
			if errors.Is(err, errQuit) {
				fmt.Fprintf(out, "\nnothing changed.\n")
				return nil
			}
			return err
		}
		if len(changes) == 0 {
			fmt.Fprintf(out, "\nnothing changed.\n")
			return nil
		}
	}

	resp, err := client.Settings(ctx, &api.SettingsRequest{Envs: changes})
	if err != nil {
		return err
	}
	reportEnvChanges(out, changes, resp)

	yes, _ := cmd.Flags().GetBool("yes")
	return offerUnload(ctx, client, a, out, changes, resp.Restart, yes)
}

// envChangesFromArgs reads NAME=VALUE arguments and --unset names.
func envChangesFromArgs(cmd *cobra.Command, args []string) (map[string]*string, error) {
	changes := map[string]*string{}
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("%q: want NAME=VALUE (use --unset NAME to remove an override)", arg)
		}
		changes[name] = &value
	}
	unset, _ := cmd.Flags().GetStringSlice("unset")
	for _, name := range unset {
		name = strings.TrimSpace(name)
		if _, both := changes[name]; both {
			return nil, fmt.Errorf("%s is both set and unset", name)
		}
		changes[name] = nil
	}
	return changes, nil
}

// askEnvChanges walks the operator through one change at a time until done.
func askEnvChanges(a *asker, cur *api.SettingsResponse) (map[string]*string, error) {
	changes := map[string]*string{}
	byName := make(map[string]api.SettingsEnv, len(cur.Envs))
	for _, e := range cur.Envs {
		byName[e.Name] = e
	}
	printEnvTable(a.out, cur.Envs, false)
	for {
		options := [][2]string{{"done", "done"}, {"set", "set a variable"}}
		if slices.ContainsFunc(cur.Envs, func(e api.SettingsEnv) bool { return e.Tweak != nil }) {
			options = append(options, [2]string{"unset", "remove an override: the environment applies again"})
		}
		action, err := a.menu("envs", "the server's environment overrides", options, 0)
		if err != nil {
			return nil, err
		}
		if action == "done" {
			return changes, nil
		}

		a.printf("\nvariable name, e.g. OLLAMA_KV_CACHE_TYPE\nname> ")
		name, err := a.readLine()
		if err != nil {
			return nil, err
		}
		a.answered++
		name = strings.ToUpper(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		e, known := byName[name]
		if action == "unset" {
			if e.Tweak == nil {
				a.printf("  ! %s has no override\n", name)
				continue
			}
			changes[name] = nil
			continue
		}
		if known && e.Description != "" {
			a.printf("  %s\n", e.Description)
		}
		current := ""
		if e.Tweak != nil {
			current = *e.Tweak
		}
		a.printf("\nvalue (empty keeps %q)\nvalue [%s]> ", current, current)
		value, err := a.readLine()
		if err != nil {
			return nil, err
		}
		a.answered++
		if value == "" {
			if e.Tweak == nil {
				continue
			}
			value = current
		}
		changes[name] = &value
	}
}

// reportEnvChanges says what changed and what waits for a restart.
func reportEnvChanges(out io.Writer, changes map[string]*string, resp *api.SettingsResponse) {
	names := make([]string, 0, len(changes))
	for name := range changes {
		names = append(names, name)
	}
	slices.Sort(names)
	fmt.Fprintln(out)
	for _, name := range names {
		if v := changes[name]; v == nil {
			fmt.Fprintf(out, "  %s: override removed\n", name)
		} else {
			fmt.Fprintf(out, "  %s=%s\n", name, *v)
		}
	}
	fmt.Fprintf(out, "written to %s\n", resp.Path)
	if len(resp.Restart) > 0 {
		fmt.Fprintf(out, "\nrestart the server for these to apply: %s\n", strings.Join(resp.Restart, ", "))
	}
}

// offerUnload unloads the running models when a change applies at the next
// load. It asks first unless yes is set; a model left running keeps the
// settings it was loaded with.
func offerUnload(ctx context.Context, client *api.Client, a *asker, out io.Writer, changes map[string]*string, restart []string, yes bool) error {
	atLoad := false
	for name := range changes {
		if !slices.Contains(restart, name) {
			atLoad = true
			break
		}
	}
	if !atLoad {
		return nil
	}
	names := runningModels(ctx, client)
	if len(names) == 0 {
		return nil
	}
	if !yes {
		ok, err := a.confirm("unload", fmt.Sprintf("running models keep the settings they were loaded with: %s\nunload them now, so the next request loads them with the new settings?", strings.Join(names, ", ")))
		if errors.Is(err, errQuit) {
			ok, err = false, nil
		}
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(out, "left running; the change applies at their next load.\n")
			return nil
		}
	}
	for _, name := range names {
		req := &api.GenerateRequest{Model: name, KeepAlive: &api.Duration{Duration: 0}}
		if err := client.Generate(ctx, req, func(api.GenerateResponse) error { return nil }); err != nil {
			return fmt.Errorf("unloading %s: %w", name, err)
		}
		fmt.Fprintf(out, "unloaded %s\n", name)
	}
	return nil
}

// printEnvTable lists the variables. Unless all is set, only those with a
// value from either source.
func printEnvTable(out io.Writer, envs []api.SettingsEnv, all bool) {
	rows := [][]string{{"NAME", "VALUE", "SOURCE", "NOTE"}}
	for _, e := range envs {
		if !all && e.Source == "" {
			continue
		}
		source := e.Source
		if source == "" {
			source = "default"
		}
		var notes []string
		if e.Tweak != nil && e.Env != nil && *e.Env != *e.Tweak {
			notes = append(notes, fmt.Sprintf("overrides env %q", *e.Env))
		}
		if e.External {
			notes = append(notes, "passed to the engine")
		}
		if e.Restart && e.Tweak != nil {
			notes = append(notes, "applies at server start")
		}
		rows = append(rows, []string{e.Name, e.Value, source, strings.Join(notes, "; ")})
	}
	if len(rows) == 1 {
		fmt.Fprintln(out, "no variable is set: the server runs on its defaults.")
		return
	}
	printRows(out, rows)
}

// printRows prints aligned columns.
func printRows(out io.Writer, rows [][]string) {
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				break
			}
			fmt.Fprintf(&b, "%-*s  ", width[i], c)
		}
		fmt.Fprintln(out, strings.TrimRight(b.String(), " "))
	}
}

// runningModels names the loaded models. A server that cannot say has none to
// offer: the change was written, and the offer is only a convenience.
func runningModels(ctx context.Context, client *api.Client) []string {
	running, err := client.ListRunning(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, len(running.Models))
	for i, m := range running.Models {
		names[i] = m.Name
	}
	return names
}
