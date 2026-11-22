package xollama

import (
	"fmt"
	"regexp"
	"strings"
)

// Ollama Cloud accounts beside the server's own (plans/cloud-accounts.md).
//
// An account is an ed25519 key the server keeps for it, registered with that
// ollama.com account through the connect page. The settings hold only what
// says which account is which and how much it may take: never an email, never
// key material.

// MaxCloudAccounts is how many accounts may be added beside the primary
// (owner, 2026-11-22).
const MaxCloudAccounts = 25

// maxCloudParallel bounds a stated number of requests at once, per account.
const maxCloudParallel = 64

// The primary account's place in the pool: the key `xollama signin` sets.
const (
	CloudPrimaryInclude = "include"
	CloudPrimaryExclude = "exclude"
)

// The states of an account, as the admin route and `tweak` report them.
const (
	// CloudPending: the key is made and waits for the connect page.
	CloudPending = "pending"
	// CloudSignedIn: ollama.com answered /api/me for the key.
	CloudSignedIn = "signed_in"
	// CloudSignedOut: the key was removed at ollama.com; the entry stays.
	CloudSignedOut = "signed_out"
)

// CloudSettings is the settings file's "cloud" section.
type CloudSettings struct {
	Accounts []CloudAccount `json:"accounts,omitempty"`
	// AccountsAtOnce is how many accounts take requests at the same time;
	// 0 is every usable one.
	AccountsAtOnce int `json:"accounts_at_once,omitempty"`
	// ParallelPerAccount overrides every account's plan; 0 is the plan's.
	ParallelPerAccount int `json:"parallel_per_account,omitempty"`
	// Primary is "", "include" or "exclude": whether the primary account
	// serves cloud models too. "" is include.
	Primary string `json:"primary,omitempty"`
}

// CloudAccount is one added account.
type CloudAccount struct {
	// ID names the account's key directory: eight hex digits, made here.
	ID string `json:"id"`
	// Label is the operator's name for it.
	Label string `json:"label"`
	// Account is a hash of the ollama.com account id, so the same account
	// added twice is refused; empty until verified.
	Account string `json:"account,omitempty"`
	// Plan is what /api/me said at the last verification.
	Plan string `json:"plan,omitempty"`
	// Parallel overrides the plan's requests at once; 0 is the plan's.
	Parallel   int    `json:"parallel,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
	SignedOut  bool   `json:"signed_out,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	// Auth is how the account authenticates: "" (CloudAuthKey) an ed25519
	// key connected through the connect page, CloudAuthAPIKey an ollama.com
	// API key. Neither is kept here: both live in the account's directory.
	Auth string `json:"auth,omitempty"`
}

// How an account authenticates.
const (
	CloudAuthKey    = ""
	CloudAuthAPIKey = "api_key"
)

// UsesAPIKey reports an account that authenticates with an API key.
func (a CloudAccount) UsesAPIKey() bool { return a.Auth == CloudAuthAPIKey }

// planParallel is how many requests an ollama.com plan runs at once. The
// same table as Cerebriline's OLLAMA_PLAN_CONCURRENCY
// (sdk/packages/llms/src/providers/vendors/ollama-account-slots.ts, from
// ollama.com/pricing, 2026-11): an account with no subscription is
// pay-as-you-go, which the server reports as "free".
var planParallel = map[string]int{
	"free": 3, "payg": 3, "pay-as-you-go": 3, "pay_as_you_go": 3,
	"pro": 3, "max": 10, "team": 10,
}

// DefaultCloudParallel is what an account whose plan is not in the table
// is given.
const DefaultCloudParallel = 3

// PlanParallel is the plan's requests at once, and whether the plan is known.
func PlanParallel(plan string) (int, bool) {
	n, ok := planParallel[strings.ToLower(strings.TrimSpace(plan))]
	if !ok {
		return DefaultCloudParallel, false
	}
	return n, true
}

// AccountParallel is the requests at once an account takes: its own number,
// else the section's, else its plan's.
func (c *CloudSettings) AccountParallel(a CloudAccount) int {
	if a.Parallel > 0 {
		return a.Parallel
	}
	if c != nil && c.ParallelPerAccount > 0 {
		return c.ParallelPerAccount
	}
	n, _ := PlanParallel(a.Plan)
	return n
}

// PrimaryIncluded says whether the primary account serves cloud models.
func (c *CloudSettings) PrimaryIncluded() bool {
	return c == nil || c.Primary != CloudPrimaryExclude
}

// State is the account's sign-in state.
func (a CloudAccount) State() string {
	switch {
	case a.SignedOut:
		return CloudSignedOut
	case a.VerifiedAt == "":
		return CloudPending
	default:
		return CloudSignedIn
	}
}

// Usable says whether the account can take requests: signed in, enabled.
func (a CloudAccount) Usable() bool {
	return !a.Disabled && a.State() == CloudSignedIn
}

// IsZero reports a section that states nothing.
func (c *CloudSettings) IsZero() bool {
	return c == nil || (len(c.Accounts) == 0 && c.AccountsAtOnce == 0 && c.ParallelPerAccount == 0 && c.Primary == "")
}

var (
	cloudIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}$`)
	cloudLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// ValidCloudLabel reports a label the section accepts.
func ValidCloudLabel(label string) bool { return cloudLabelPattern.MatchString(label) }

// Validate refuses a section the server could not act on.
func (c *CloudSettings) Validate() error {
	if c == nil {
		return nil
	}
	if len(c.Accounts) > MaxCloudAccounts {
		return fmt.Errorf("cloud: %d accounts; at most %d can be added beside the primary", len(c.Accounts), MaxCloudAccounts)
	}
	switch c.Primary {
	case "", CloudPrimaryInclude, CloudPrimaryExclude:
	default:
		return fmt.Errorf("cloud: primary must be %q or %q, not %q", CloudPrimaryInclude, CloudPrimaryExclude, c.Primary)
	}
	if c.AccountsAtOnce < 0 || c.AccountsAtOnce > MaxCloudAccounts+1 {
		return fmt.Errorf("cloud: accounts_at_once must be 0 (all) to %d, not %d", MaxCloudAccounts+1, c.AccountsAtOnce)
	}
	if c.ParallelPerAccount < 0 || c.ParallelPerAccount > maxCloudParallel {
		return fmt.Errorf("cloud: parallel_per_account must be 0 (the plan's) to %d, not %d", maxCloudParallel, c.ParallelPerAccount)
	}
	ids, labels, accounts := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, a := range c.Accounts {
		if !cloudIDPattern.MatchString(a.ID) {
			return fmt.Errorf("cloud: account id %q is not eight hex digits", a.ID)
		}
		if a.Auth != CloudAuthKey && a.Auth != CloudAuthAPIKey {
			return fmt.Errorf("cloud: account %q: auth must be empty (a connected key) or %q, not %q", a.Label, CloudAuthAPIKey, a.Auth)
		}
		if !ValidCloudLabel(a.Label) {
			return fmt.Errorf("cloud: label %q must be 1 to 32 letters, digits, '.', '_' or '-', starting with a letter or digit", a.Label)
		}
		if ids[a.ID] {
			return fmt.Errorf("cloud: account id %q is listed twice", a.ID)
		}
		if labels[strings.ToLower(a.Label)] {
			return fmt.Errorf("cloud: label %q is used twice", a.Label)
		}
		if a.Account != "" {
			if other, ok := accounts[a.Account]; ok {
				return fmt.Errorf("cloud: %q and %q are the same ollama.com account", other, a.Label)
			}
			accounts[a.Account] = a.Label
		}
		if a.Parallel < 0 || a.Parallel > maxCloudParallel {
			return fmt.Errorf("cloud: %s: parallel must be 0 (the plan's) to %d, not %d", a.Label, maxCloudParallel, a.Parallel)
		}
		ids[a.ID], labels[strings.ToLower(a.Label)] = true, true
	}
	return nil
}

// Find is the account named by its id or its label, case-insensitively.
func (c *CloudSettings) Find(name string) (int, bool) {
	if c == nil {
		return -1, false
	}
	for i, a := range c.Accounts {
		if a.ID == name || strings.EqualFold(a.Label, name) {
			return i, true
		}
	}
	return -1, false
}
