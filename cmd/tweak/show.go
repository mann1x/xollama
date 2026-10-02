package tweak

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// showCommand is `xollama tweak show`: what is set, and where it comes from
// (plans/system-settings.md).
func showCommand(opts Options) *cobra.Command {
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show what is set on the server, its environment overrides, or a model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	envs := &cobra.Command{
		Use:   "envs",
		Short: "The server's environment variables: the value used and where it comes from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := settings(cmd, opts)
			if err != nil {
				return err
			}
			all, _ := cmd.Flags().GetBool("all")
			printEnvTable(cmd.OutOrStdout(), resp.Envs, all)
			return nil
		},
	}
	envs.Flags().Bool("all", false, "Also list the variables nothing sets, at their defaults")

	server := &cobra.Command{
		Use:   "server",
		Short: "The server's own settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := settings(cmd, opts)
			if err != nil {
				return err
			}
			client, err := api.ClientFromEnvironment()
			if err != nil {
				return err
			}
			key, err := client.APIKey(cmd.Context(), &api.APIKeyRequest{Action: "status"})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "API key: %s\n", describeKey(key))
			fmt.Fprintf(out, "settings file: %s\n", resp.Path)
			fmt.Fprintf(out, "\n── defaults for every model's settings\n")
			if rows := SettingRows(resp.Defaults); len(rows) > 0 {
				table := [][]string{{"SETTING", "VALUE"}}
				for _, r := range rows {
					table = append(table, []string{r[0], r[1]})
				}
				printRows(out, table)
			} else {
				fmt.Fprintln(out, "none: a model that states nothing runs on the environment and the built-in defaults.")
			}
			fmt.Fprintf(out, "\n── GPUs\n")
			printGPUTable(out, resp.GPU, gpuPolicy(cmd.Context(), client, resp))
			fmt.Fprintf(out, "\n── environment variables\n")
			printEnvTable(out, resp.Envs, false)
			return nil
		},
	}

	model := &cobra.Command{
		Use:   "model MODEL",
		Short: "A model's own xollama settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Heartbeat != nil {
				if err := opts.Heartbeat(cmd, args); err != nil {
					return err
				}
			}
			client, err := api.ClientFromEnvironment()
			if err != nil {
				return err
			}
			cfg, err := showConfig(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// The server's defaults are read only from its own machine; from
			// elsewhere, the model's own settings are all there is to show.
			var defaults *xollama.Config
			if resp, err := client.Settings(cmd.Context(), nil); err == nil {
				defaults = resp.Defaults
			} else {
				fmt.Fprintf(out, "(the server's defaults are not shown: %v)\n", err)
			}
			table := modelSourceRows(cfg, defaults)
			if len(table) == 1 {
				fmt.Fprintf(out, "%s states no xollama setting, and the server has no defaults: the environment and the built-in defaults apply.\n", args[0])
				return nil
			}
			printRows(out, table)
			return nil
		},
	}

	showCmd.AddCommand(server, envs, model)
	return showCmd
}

func settings(cmd *cobra.Command, opts Options) (*api.SettingsResponse, error) {
	if opts.Heartbeat != nil {
		if err := opts.Heartbeat(cmd, nil); err != nil {
			return nil, err
		}
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, err
	}
	return client.Settings(cmd.Context(), nil)
}

// modelSourceRows lists the settings a model runs with, each with its source:
// "model" when the model states it, "server" when the server's default fills
// it in. A default this model cannot act on is listed as not applied.
func modelSourceRows(own, defaults *xollama.Config) [][]string {
	ownRows := SettingRows(own)
	stated := make(map[string]bool, len(ownRows))
	for _, r := range ownRows {
		stated[r[0]] = true
	}
	merged, skipped := own.WithDefaults(defaults)
	table := [][]string{{"SETTING", "VALUE", "SOURCE"}}
	for _, r := range SettingRows(merged) {
		source := "server"
		if stated[r[0]] {
			source = "model"
		}
		table = append(table, []string{r[0], r[1], source})
	}
	for _, s := range skipped {
		table = append(table, []string{"(server default)", s, "not applied"})
	}
	return table
}
