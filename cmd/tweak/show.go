package tweak

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
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
			fmt.Fprintf(out, "settings file: %s\n\n", resp.Path)
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
			rows := SettingRows(cfg)
			if len(rows) == 0 {
				fmt.Fprintf(out, "%s states no xollama setting: the server's settings apply.\n", args[0])
				return nil
			}
			table := [][]string{{"SETTING", "VALUE"}}
			for _, r := range rows {
				table = append(table, []string{r[0], r[1]})
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
