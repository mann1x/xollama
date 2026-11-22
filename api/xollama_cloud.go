package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// XollamaCloudAccountsPath manages the server's Ollama Cloud accounts
// (plans/cloud-accounts.md). Like the settings, the server answers it only
// from its own machine, never through a proxy: it makes and removes keys
// that act as ollama.com accounts.
const XollamaCloudAccountsPath = "/api/xollama/cloud/accounts"

// The actions of a CloudAccountsRequest.
const (
	// CloudList reads the accounts and the pool settings.
	CloudList = "list"
	// CloudAdd makes a key for a new account and returns its connect URL.
	CloudAdd = "add"
	// CloudVerify asks ollama.com whether the account's key is connected,
	// and records its plan when it is. An account still waiting for the
	// connect page is not an error: its state stays "pending".
	CloudVerify = "verify"
	// CloudSignin returns a new connect URL for an existing account.
	CloudSignin = "signin"
	// CloudSignout removes the account's key at ollama.com; the entry stays.
	CloudSignout = "signout"
	// CloudSet changes an account's label, parallel or disabled flag.
	CloudSet = "set"
	// CloudRemove signs the account out, then deletes its key and entry.
	CloudRemove = "remove"
	// CloudPool changes accounts_at_once, parallel_per_account and primary.
	CloudPool = "pool"
)

// CloudAccountsRequest is one action. Account names an account by its id or
// label (add takes Label instead). A nil pointer leaves a setting as it is.
type CloudAccountsRequest struct {
	Action  string `json:"action"`
	Account string `json:"account,omitempty"`
	Label   string `json:"label,omitempty"`
	// APIKey, with add or signin, makes the account authenticate with this
	// ollama.com API key instead of a connected key. It is checked with
	// ollama.com before it is kept, stored in the account's directory (0600)
	// and never returned.
	APIKey string `json:"api_key,omitempty"`

	Parallel *int  `json:"parallel,omitempty"`
	Disabled *bool `json:"disabled,omitempty"`

	AccountsAtOnce     *int    `json:"accounts_at_once,omitempty"`
	ParallelPerAccount *int    `json:"parallel_per_account,omitempty"`
	Primary            *string `json:"primary,omitempty"`
}

// CloudAccountInfo is one account as the server sees it. It carries no
// email and no key.
type CloudAccountInfo struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
	Plan  string `json:"plan,omitempty"`
	// Parallel is the requests at once it takes; ParallelSet is the
	// operator's own number for it, 0 when the plan's applies.
	Parallel    int    `json:"parallel"`
	ParallelSet int    `json:"parallel_set,omitempty"`
	State       string `json:"state"`
	// Auth is "api_key" for an account on an API key, empty for a
	// connected key.
	Auth       string `json:"auth,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	// Primary marks the account `xollama signin` set.
	Primary bool `json:"primary,omitempty"`
	// Excluded marks a primary kept out of cloud-model use.
	Excluded bool `json:"excluded,omitempty"`
}

// CloudAccountsResponse is the accounts after the action.
type CloudAccountsResponse struct {
	Accounts           []CloudAccountInfo `json:"accounts"`
	AccountsAtOnce     int                `json:"accounts_at_once"`
	ParallelPerAccount int                `json:"parallel_per_account"`
	Primary            string             `json:"primary"`
	// Capacity is the requests at once the usable accounts give, with
	// AccountsAtOnce of them at a time.
	Capacity int `json:"capacity"`
	// Account is the one the action was about.
	Account *CloudAccountInfo `json:"account,omitempty"`
	// ConnectURL is the page that connects the account's key: open it in a
	// browser signed in to that ollama.com account.
	ConnectURL string `json:"connect_url,omitempty"`
}

// CloudAccounts runs one action on the server's cloud accounts.
func (c *Client) CloudAccounts(ctx context.Context, req *CloudAccountsRequest) (*CloudAccountsResponse, error) {
	var resp CloudAccountsResponse
	if err := c.do(ctx, http.MethodPost, XollamaCloudAccountsPath, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// XollamaCloudPath is the pool of cloud accounts as a client sees it, for
// sizing its own concurrency: how many cloud requests the server serves at
// once, and how many it can serve now. It names no email, id or key.
const XollamaCloudPath = "/api/xollama/cloud"

// XollamaCloudBalancePath is each pooled account's /api/balance, as last
// read (at most once a minute per account), so a client polling it never
// spends the accounts' own rate limit.
const XollamaCloudBalancePath = "/api/xollama/cloud/balance"

// FeatureCloudAccounts: the server reports its pool of Ollama Cloud accounts
// at XollamaCloudPath. A client capping its cloud requests per ollama.com
// account caps them per server at the pool's parallel_now instead, when
// accounts > 0.
const FeatureCloudAccounts = "cloud_accounts_v1"

// CloudPoolStatus is the pool now. With no pool configured only Accounts (0)
// is set: the server serves cloud models through its primary account alone,
// as upstream does, and a client keeps its own per-account cap.
type CloudPoolStatus struct {
	// Accounts is how many accounts the pool holds (the primary included
	// unless excluded); AccountsAtOnce how many serve at the same time (0:
	// all).
	Accounts       int `json:"accounts"`
	AccountsAtOnce int `json:"accounts_at_once,omitempty"`
	// Parallel is the requests at once the configuration gives, with every
	// account ready; ParallelNow leaves out the accounts that ran out.
	Parallel    int `json:"parallel"`
	ParallelNow int `json:"parallel_now"`
	InUse       int `json:"in_use"`
	Queued      int `json:"queued"`
	Exhausted   int `json:"exhausted"`
	// NextReset is when the first account that ran out comes back.
	NextReset *time.Time         `json:"next_reset,omitempty"`
	Pool      []CloudPoolAccount `json:"pool,omitempty"`
}

// CloudPoolAccount is one account of the pool.
type CloudPoolAccount struct {
	Label    string `json:"label"`
	Plan     string `json:"plan,omitempty"`
	Parallel int    `json:"parallel"`
	InUse    int    `json:"in_use"`
	// State is "active" (taking requests), "ready" (waiting its turn
	// beyond accounts_at_once), "exhausted" or "signed_out".
	State string     `json:"state"`
	Until *time.Time `json:"until,omitempty"`
}

// CloudBalance is one account's /api/balance as ollama.com answered it.
type CloudBalance struct {
	Label   string          `json:"label"`
	Balance json.RawMessage `json:"balance,omitempty"`
	Error   string          `json:"error,omitempty"`
	ReadAt  *time.Time      `json:"read_at,omitempty"`
}

// CloudPool reads the pool.
func (c *Client) CloudPool(ctx context.Context) (*CloudPoolStatus, error) {
	var resp CloudPoolStatus
	if err := c.do(ctx, http.MethodGet, XollamaCloudPath, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
