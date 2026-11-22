package tweak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// cloudCommand is `xollama tweak server cloud`: the Ollama Cloud accounts
// the server serves cloud models through (plans/cloud-accounts.md).
func cloudCommand(opts Options) *cobra.Command {
	cloudCmd := &cobra.Command{
		Use:   "cloud",
		Short: "Add, sign in, sign out, change and remove Ollama Cloud accounts",
		Long: `Manage the Ollama Cloud accounts the server serves cloud models through.

    xollama tweak server cloud                          list, then ask
    xollama tweak server cloud list
    xollama tweak server cloud add work-2               make a key, print its connect page, wait
    xollama tweak server cloud login work-2             connect an account again
    xollama tweak server cloud logout work-2            remove its key at ollama.com, keep it listed
    xollama tweak server cloud set work-2 --parallel 2  change one account
    xollama tweak server cloud set work-2 --disable
    xollama tweak server cloud remove work-2            sign out, delete its key and entry
    xollama tweak server cloud --accounts-at-once 3     how many accounts take requests at once
    xollama tweak server cloud --parallel-per-account 3 requests at once per account (0: the plan's)
    xollama tweak server cloud --primary exclude        keep the primary account off cloud models

Each account is a key the server makes for it. "add" prints that key's connect
page: open it in a browser signed in to the ollama.com account you are adding
(switch the browser's account, or use a private window, between accounts).
The server checks the key with ollama.com and records the account's plan.

The primary account is the one ` + "`xollama signin`" + ` sets. It is account 0 and does not
count toward the 25 accounts that can be added. Registry pulls and pushes, web
search and /api/me always use it; --primary exclude only keeps it off cloud
models.

An account takes as many requests at once as its plan allows (Pro 3, Max 10),
unless --parallel says otherwise. With --accounts-at-once 3 and Pro accounts,
the server serves up to 9 cloud requests at once. Only from the server's own
machine.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCloud(cmd, opts)
		},
	}
	f := cloudCmd.Flags()
	f.String("accounts-at-once", "", "How many accounts take requests at the same time (N, 0 or all)")
	f.String("parallel-per-account", "", "Requests at once per account (N, 0 or plan)")
	f.String("primary", "", "include|exclude: whether the primary account serves cloud models")

	wait := func(c *cobra.Command) {
		c.Flags().Duration("wait", 10*time.Minute, "How long to wait for the connect page (0: do not wait)")
	}
	add := &cobra.Command{
		Use:   "add LABEL",
		Short: "Add an account: make its key and print its connect page",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudSignin(cmd, opts, api.CloudAdd, args[0])
		},
	}
	wait(add)
	login := &cobra.Command{
		Use:   "login ACCOUNT",
		Short: "Connect an account's key again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudSignin(cmd, opts, api.CloudSignin, args[0])
		},
	}
	wait(login)
	logout := &cobra.Command{
		Use:   "logout ACCOUNT",
		Short: "Remove an account's key at ollama.com; it stays listed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudAction(cmd, opts, &api.CloudAccountsRequest{Action: api.CloudSignout, Account: args[0]})
		},
	}
	set := &cobra.Command{
		Use:   "set ACCOUNT",
		Short: "Change an account's label, requests at once, or disable it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudSet(cmd, opts, args[0])
		},
	}
	set.Flags().String("label", "", "New label")
	set.Flags().String("parallel", "", "Requests at once (N, or plan)")
	set.Flags().Bool("disable", false, "Keep the account off cloud models")
	set.Flags().Bool("enable", false, "Let the account serve cloud models again")
	remove := &cobra.Command{
		Use:   "remove ACCOUNT",
		Short: "Sign an account out, then delete its key and entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudRemove(cmd, opts, args[0])
		},
	}
	remove.Flags().BoolP("yes", "y", false, "Do not ask for confirmation")
	list := &cobra.Command{
		Use:   "list",
		Short: "List the accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudAction(cmd, opts, &api.CloudAccountsRequest{Action: api.CloudList})
		},
	}
	cloudCmd.AddCommand(list, add, login, logout, set, remove)
	return cloudCmd
}

func cloudClient(cmd *cobra.Command, opts Options) (*api.Client, error) {
	if opts.Heartbeat != nil {
		if err := opts.Heartbeat(cmd, nil); err != nil {
			return nil, err
		}
	}
	return api.ClientFromEnvironment()
}

// cloudAction runs one action and prints the accounts.
func cloudAction(cmd *cobra.Command, opts Options, req *api.CloudAccountsRequest) error {
	client, err := cloudClient(cmd, opts)
	if err != nil {
		return err
	}
	resp, err := client.CloudAccounts(cmd.Context(), req)
	if err != nil {
		return err
	}
	printCloud(cmd.OutOrStdout(), resp)
	return nil
}

// countArg reads a count flag: N, or a word that means 0.
func countArg(v string, zero ...string) (int, error) {
	for _, z := range zero {
		if strings.EqualFold(v, z) {
			return 0, nil
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("want a number of 0 or more, or %s; got %q", strings.Join(zero, "/"), v)
	}
	return n, nil
}

func runCloud(cmd *cobra.Command, opts Options) error {
	f := cmd.Flags()
	req := &api.CloudAccountsRequest{Action: api.CloudPool}
	changed := false
	if v, _ := f.GetString("accounts-at-once"); v != "" {
		n, err := countArg(v, "all")
		if err != nil {
			return fmt.Errorf("--accounts-at-once: %w", err)
		}
		req.AccountsAtOnce, changed = &n, true
	}
	if v, _ := f.GetString("parallel-per-account"); v != "" {
		n, err := countArg(v, "plan")
		if err != nil {
			return fmt.Errorf("--parallel-per-account: %w", err)
		}
		req.ParallelPerAccount, changed = &n, true
	}
	if v, _ := f.GetString("primary"); v != "" {
		v = strings.ToLower(v)
		if v != xollama.CloudPrimaryInclude && v != xollama.CloudPrimaryExclude {
			return fmt.Errorf("--primary is include or exclude, not %q", v)
		}
		req.Primary, changed = &v, true
	}
	if changed {
		return cloudAction(cmd, opts, req)
	}
	return cloudMenu(cmd, opts)
}

// cloudMenu lists the accounts and asks what to do, until the operator is
// done.
func cloudMenu(cmd *cobra.Command, opts Options) error {
	client, err := cloudClient(cmd, opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	a := newAsker(cmd.InOrStdin(), out)
	resp, err := client.CloudAccounts(cmd.Context(), &api.CloudAccountsRequest{Action: api.CloudList})
	if err != nil {
		return err
	}
	for {
		printCloud(out, resp)
		choice, err := a.menu("action", "Cloud accounts", [][2]string{
			{"done", "done"},
			{"add", "add an account"},
			{"login", "connect an account again"},
			{"logout", "sign an account out"},
			{"set", "change an account (label, requests at once, disabled)"},
			{"remove", "remove an account"},
			{"pool", "accounts at once, requests per account, primary"},
		}, 0)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		ask := func(q string) (string, error) {
			a.printf("\n%s\n> ", q)
			return a.readLine()
		}
		switch choice {
		case "done":
			return nil
		case "add", "login":
			name, err := ask(map[string]string{"add": "Label for the new account (letters, digits, . _ -)", "login": "Account to connect again (label)"}[choice])
			if err != nil {
				return err
			}
			action := api.CloudAdd
			if choice == "login" {
				action = api.CloudSignin
			}
			if err := cloudSignin(cmd, opts, action, name); err != nil {
				a.printf("  ! %v\n", err)
			}
		case "logout", "remove":
			name, err := ask("Account (label)")
			if err != nil {
				return err
			}
			if choice == "remove" {
				ok, err := a.confirm("remove", fmt.Sprintf("Sign %q out at ollama.com and delete its key here?", name))
				if err != nil || !ok {
					continue
				}
			}
			action := map[string]string{"logout": api.CloudSignout, "remove": api.CloudRemove}[choice]
			if _, err := client.CloudAccounts(cmd.Context(), &api.CloudAccountsRequest{Action: action, Account: name}); err != nil {
				a.printf("  ! %v\n", err)
			}
		case "set":
			name, err := ask("Account (label)")
			if err != nil {
				return err
			}
			req := &api.CloudAccountsRequest{Action: api.CloudSet, Account: name}
			if v, err := ask("Requests at once (N, plan, or empty to keep)"); err == nil && v != "" {
				n, err := countArg(v, "plan")
				if err != nil {
					a.printf("  ! %v\n", err)
					continue
				}
				req.Parallel = &n
			}
			if v, err := ask("Disabled? (yes, no, or empty to keep)"); err == nil && v != "" {
				d := strings.HasPrefix(strings.ToLower(v), "y")
				req.Disabled = &d
			}
			if v, err := ask("New label (empty to keep)"); err == nil {
				req.Label = v
			}
			if _, err := client.CloudAccounts(cmd.Context(), req); err != nil {
				a.printf("  ! %v\n", err)
			}
		case "pool":
			req := &api.CloudAccountsRequest{Action: api.CloudPool}
			if v, err := ask("Accounts taking requests at once (N, all, or empty to keep)"); err == nil && v != "" {
				n, err := countArg(v, "all")
				if err != nil {
					a.printf("  ! %v\n", err)
					continue
				}
				req.AccountsAtOnce = &n
			}
			if v, err := ask("Requests at once per account (N, plan, or empty to keep)"); err == nil && v != "" {
				n, err := countArg(v, "plan")
				if err != nil {
					a.printf("  ! %v\n", err)
					continue
				}
				req.ParallelPerAccount = &n
			}
			if v, err := ask("Primary account serves cloud models? (include, exclude, or empty to keep)"); err == nil && v != "" {
				req.Primary = &v
			}
			if _, err := client.CloudAccounts(cmd.Context(), req); err != nil {
				a.printf("  ! %v\n", err)
			}
		}
		if resp, err = client.CloudAccounts(cmd.Context(), &api.CloudAccountsRequest{Action: api.CloudList}); err != nil {
			return err
		}
	}
}

// cloudSignin adds an account or connects one again: it prints the connect
// page and waits until ollama.com answers for the key.
func cloudSignin(cmd *cobra.Command, opts Options, action, name string) error {
	client, err := cloudClient(cmd, opts)
	if err != nil {
		return err
	}
	req := &api.CloudAccountsRequest{Action: action, Account: name}
	if action == api.CloudAdd {
		req.Label, req.Account = name, ""
	}
	resp, err := client.CloudAccounts(cmd.Context(), req)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	label := name
	if resp.Account != nil {
		label = resp.Account.Label
	}
	fmt.Fprintf(out, "\nOpen this page in a browser signed in to the ollama.com account for %q:\n\n    %s\n\n", label, resp.ConnectURL)
	wait, _ := cmd.Flags().GetDuration("wait")
	if wait <= 0 {
		fmt.Fprintf(out, "Then run `xollama tweak server cloud login %s` to check it.\n", label)
		return nil
	}
	return cloudWaitConnected(cmd.Context(), client, out, label, wait)
}

// cloudPollEvery is how often a waiting add asks the server again.
var cloudPollEvery = 3 * time.Second

func cloudWaitConnected(ctx context.Context, client *api.Client, out io.Writer, label string, wait time.Duration) error {
	fmt.Fprintf(out, "Waiting for the page (up to %s; Ctrl-C leaves %q pending)...\n", wait, label)
	deadline := time.Now().Add(wait)
	for {
		resp, err := client.CloudAccounts(ctx, &api.CloudAccountsRequest{Action: api.CloudVerify, Account: label})
		if err != nil {
			return err
		}
		if resp.Account != nil && resp.Account.State == xollama.CloudSignedIn {
			fmt.Fprintf(out, "Connected: %q, plan %s, %d requests at once.\n", label, resp.Account.Plan, resp.Account.Parallel)
			printCloud(out, resp)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%q is still waiting for its connect page; open it, then `xollama tweak server cloud login %s`", label, label)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cloudPollEvery):
		}
	}
}

func cloudSet(cmd *cobra.Command, opts Options, name string) error {
	f := cmd.Flags()
	req := &api.CloudAccountsRequest{Action: api.CloudSet, Account: name}
	req.Label, _ = f.GetString("label")
	if v, _ := f.GetString("parallel"); v != "" {
		n, err := countArg(v, "plan")
		if err != nil {
			return fmt.Errorf("--parallel: %w", err)
		}
		req.Parallel = &n
	}
	dis, _ := f.GetBool("disable")
	en, _ := f.GetBool("enable")
	switch {
	case dis && en:
		return errors.New("--disable and --enable together")
	case dis || en:
		req.Disabled = &dis
	}
	if req.Label == "" && req.Parallel == nil && req.Disabled == nil {
		return errors.New("nothing to change: give --label, --parallel, --disable or --enable")
	}
	return cloudAction(cmd, opts, req)
}

func cloudRemove(cmd *cobra.Command, opts Options, name string) error {
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		a := newAsker(cmd.InOrStdin(), cmd.OutOrStdout())
		ok, err := a.confirm("remove", fmt.Sprintf("Sign %q out at ollama.com and delete its key here?", name))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return cloudAction(cmd, opts, &api.CloudAccountsRequest{Action: api.CloudRemove, Account: name})
}

// printCloud is the accounts table.
func printCloud(w io.Writer, resp *api.CloudAccountsResponse) {
	at := "all"
	if resp.AccountsAtOnce > 0 {
		at = strconv.Itoa(resp.AccountsAtOnce)
	}
	per := "plan's"
	if resp.ParallelPerAccount > 0 {
		per = strconv.Itoa(resp.ParallelPerAccount)
	}
	fmt.Fprintf(w, "\nCloud accounts: %d added, %s at once, requests per account: %s; up to %d cloud requests at once\n\n",
		len(resp.Accounts)-1, at, per, resp.Capacity)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tPLAN\tAT ONCE\tSTATE\t")
	for _, a := range resp.Accounts {
		state := strings.ReplaceAll(a.State, "_", " ")
		switch {
		case a.Excluded:
			state += ", excluded from cloud models"
		case a.Disabled:
			state += ", disabled"
		}
		at := strconv.Itoa(a.Parallel)
		if a.ParallelSet > 0 {
			at += " (set)"
		}
		plan := a.Plan
		if plan == "" {
			plan = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t\n", a.Label, plan, at, state)
	}
	tw.Flush()
}
