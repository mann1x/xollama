package tweak

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// serverCommand is `xollama tweak server`: the server's own settings, as
// opposed to a model's. Today that is the local API key
// (docs/xollama/api-key.mdx).
func serverCommand(opts Options) *cobra.Command {
	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Set the server's own settings: the local API key",
		Long: `Set the server's own settings.

--api-key manages the key every client must send to this server's endpoints
(Authorization: Bearer <key>, or x-api-key). It is a local key for incoming
connections only: pulls, pushes and cloud models keep their ollama.com sign-in.

    xollama tweak server --api-key=generate   make a 256-bit key; shown once
    xollama tweak server --api-key=set        read a key from stdin
    xollama tweak server --api-key=remove     no key: the server is open again
    xollama tweak server --api-key=status     say whether a key is required
    xollama tweak server                      ask

The key is not typed on the command line, so it stays out of shell history.
After generate or set this user's copy is saved to ~/.ollama/xollama-api-key
(mode 0600), which the CLI sends from then on; other machines need
XOLLAMA_API_KEY or that file.

Only a client on the server's own machine can change the key, never through a
proxy, and while a key is set only with the current key. Plain HTTP carries the
key in clear text: across a network, put the server behind TLS.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd, opts)
		},
	}
	serverCmd.Flags().String("api-key", "", "generate|set|remove|status; bare asks")
	serverCmd.Flags().Lookup("api-key").NoOptDefVal = askSentinel
	return serverCmd
}

func runServer(cmd *cobra.Command, opts Options) error {
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

	status, err := client.APIKey(cmd.Context(), &api.APIKeyRequest{Action: "status"})
	if err != nil {
		return err
	}
	action, _ := cmd.Flags().GetString("api-key")
	if action == "" || action == askSentinel {
		fmt.Fprintf(out, "API key: %s\n", describeKey(status))
		options := [][2]string{{"keep", "keep it as it is"}, {"generate", "generate a new key"}, {"set", "set a key you supply"}}
		if status.Required {
			options = append(options, [2]string{"remove", "remove it: the server is open again"})
		}
		if action, err = a.menu("api-key", "the local API key", options, 0); err != nil {
			if errors.Is(err, errQuit) {
				fmt.Fprintf(out, "\nnothing changed.\n")
				return nil
			}
			return err
		}
	}

	req := &api.APIKeyRequest{Action: action}
	switch action {
	case "keep":
		return nil
	case "status":
		fmt.Fprintf(out, "API key: %s\n", describeKey(status))
		return nil
	case "set":
		fmt.Fprintf(out, "key> ")
		if req.Key, err = a.readLine(); err != nil {
			return errors.New("no key given")
		}
	case "generate", "remove":
	default:
		return fmt.Errorf("--api-key must be generate, set, remove or status (got %q)", action)
	}

	resp, err := client.APIKey(cmd.Context(), req)
	if err != nil {
		return err
	}
	switch action {
	case "generate", "set":
		key := req.Key
		if action == "generate" {
			key = resp.Key
		}
		p, err := saveClientKey(key)
		if action == "generate" {
			fmt.Fprintf(out, "\nnew API key (shown only now):\n\n    %s\n\n", key)
		}
		if err != nil {
			fmt.Fprintf(out, "the server has the key, but this user's copy could not be saved: %v\n", err)
		} else {
			fmt.Fprintf(out, "saved for this user in %s\n", p)
		}
		fmt.Fprintf(out, "clients send it as 'Authorization: Bearer <key>' or 'x-api-key'; over a network, only through TLS.\n")
	case "remove":
		if p, err := envconfig.ClientKeyPath(); err == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(out, "could not remove %s: %v\n", p, err)
			}
		}
		fmt.Fprintf(out, "API key removed: the server answers anyone who can reach it.\n")
	}
	return nil
}

func describeKey(s *api.APIKeyResponse) string {
	switch {
	case !s.Required:
		return "none (every client is answered)"
	case s.Source == "env":
		return "required, set by XOLLAMA_API_KEY in the server's environment (change it there)"
	default:
		return "required"
	}
}

// saveClientKey writes this user's copy of the key, mode 0600. Deliberately
// os and not internal/fsowner: this is the caller's own secret in the
// caller's own home, not a file of the model store, and handing it to the
// service account (fsowner's fallback when root runs this) would give it away.
func saveClientKey(key string) (string, error) {
	p, err := envconfig.ClientKeyPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".xollama-api-key-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := io.WriteString(f, strings.TrimSpace(key)+"\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}
