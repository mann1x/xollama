package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/auth"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
	"github.com/ollama/ollama/types/xollama"
)

// The server's Ollama Cloud accounts (plans/cloud-accounts.md): keys it
// makes, one per added ollama.com account, and the "cloud" section of its
// settings file that says which account is which.

const settingsCloud = "cloud"

// cloudCallTimeout bounds one call to ollama.com from the admin route.
const cloudCallTimeout = 15 * time.Second

// serverCloud is the cloud section, nil when there is none or it no longer
// validates.
func serverCloud() *xollama.CloudSettings {
	raw := envconfig.SettingsSection(settingsCloud)
	if len(raw) == 0 {
		return nil
	}
	var cs xollama.CloudSettings
	err := jsonUnmarshalStrict(raw, &cs)
	if err == nil {
		err = cs.Validate()
	}
	if err != nil {
		slog.Warn("the server's cloud accounts are ignored", "error", err)
		return nil
	}
	return &cs
}

// cloudKeyPath is an account's private key.
func cloudKeyPath(id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ollama", "xollama-cloud", id, "id_ed25519"), nil
}

// cloudAPIKeyFile is the name of an account's API key file in its
// directory; a key being checked before it replaces the old one is
// cloudAPIKeyFile + ".new".
const cloudAPIKeyFile = "api_key"

// cloudCredPath is the file an account authenticates with: its API key, or
// its private key.
func cloudCredPath(a xollama.CloudAccount) (string, error) {
	p, err := cloudKeyPath(a.ID)
	if err != nil || !a.UsesAPIKey() {
		return p, err
	}
	return filepath.Join(filepath.Dir(p), cloudAPIKeyFile), nil
}

// cloudCredCtx makes requests on ctx authenticate with the credential at
// path: an API key file (by its name), else a private key. "" is the
// primary's, upstream's own signing.
func cloudCredCtx(ctx context.Context, path string) context.Context {
	switch {
	case path == "":
		return ctx
	case strings.HasPrefix(filepath.Base(path), cloudAPIKeyFile):
		return auth.WithAPIKeyFile(ctx, path)
	default:
		return auth.WithKeyFile(ctx, path)
	}
}

// writeCloudAPIKey stores key as the account's API key file, or beside it
// as the ".new" file being checked, mode 0600 in a 0700 directory, owned as
// the store's files are.
func writeCloudAPIKey(id, key string, checking bool) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 512 || strings.ContainsAny(key, " \t\r\n") {
		return "", cloudRefuse(http.StatusBadRequest, "an ollama.com API key is one word of at most 512 characters")
	}
	p, err := cloudCredPath(xollama.CloudAccount{ID: id, Auth: xollama.CloudAuthAPIKey})
	if err != nil {
		return "", err
	}
	if checking {
		p += ".new"
	}
	if err := fsowner.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	return p, fsowner.WriteFile(p, []byte(key+"\n"), 0o600)
}

// checkCloudAPIKey writes key beside the account's credential and asks
// ollama.com whose it is. A key ollama.com refuses is removed and refused;
// one it accepts replaces the account's API key file. The account's entry is
// the caller's: verifyCloudAccount records who it is.
func checkCloudAPIKey(ctx context.Context, id, label, key string) error {
	p, err := writeCloudAPIKey(id, key, true)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, cloudCallTimeout)
	defer cancel()
	u, err := cloudWhoami(cctx, p)
	if notConnected(u, err) {
		_ = os.Remove(p)
		return cloudRefuse(http.StatusBadRequest, "ollama.com does not accept this API key for %q (create one at https://ollama.com/settings/keys)", label)
	}
	if err != nil {
		_ = os.Remove(p)
		return cloudRefuse(http.StatusBadGateway, "ollama.com did not answer for %q: %v", label, err)
	}
	return os.Rename(p, strings.TrimSuffix(p, ".new"))
}

// newCloudKey makes the account's key, mode 0600, owned as the store's
// files are (store-ownership), and returns its public key.
func newCloudKey(id string) (string, error) {
	p, err := cloudKeyPath(id)
	if err != nil {
		return "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", err
	}
	if err := fsowner.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := fsowner.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// cloudPublicKey reads the public half of the key at path.
func cloudPublicKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	k, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.PublicKey()))), nil
}

// cloudConnectURL is upstream's connect page for the key, named after this
// host and the account's label so the key is recognisable in the
// account's list of keys at ollama.com.
func cloudConnectURL(label, pub string) string {
	h, _ := os.Hostname()
	return fmt.Sprintf(signinURLStr, url.PathEscape(h+"-xollama-"+label), base64.RawURLEncoding.EncodeToString([]byte(pub)))
}

// cloudOrigin is where accounts are verified: ollama.com.
var cloudOrigin = func() *url.URL { return &url.URL{Scheme: "https", Host: "ollama.com"} }

// cloudWhoami asks ollama.com who the key at keyPath belongs to; "" is the
// primary key.
var cloudWhoami = func(ctx context.Context, keyPath string) (*api.UserResponse, error) {
	return api.NewClient(cloudOrigin(), http.DefaultClient).Whoami(cloudCredCtx(ctx, keyPath))
}

// cloudDisconnect removes the key at keyPath from its ollama.com account,
// signed with that key.
var cloudDisconnect = func(ctx context.Context, keyPath, pub string) error {
	ctx = auth.WithKeyFile(ctx, keyPath)
	return api.NewClient(cloudOrigin(), http.DefaultClient).Disconnect(ctx, base64.RawURLEncoding.EncodeToString([]byte(pub)))
}

// cloudAccountHash tells accounts apart without keeping their id: the same
// ollama.com account added twice gets the same hash.
func cloudAccountHash(u *api.UserResponse) string {
	id := u.ID.String()
	if id == "00000000-0000-0000-0000-000000000000" {
		id = strings.ToLower(u.Email + "\x00" + u.Name)
	}
	sum := sha256.Sum256([]byte("xollama-cloud\x00" + id))
	return hex.EncodeToString(sum[:8])
}

// notConnected reports an answer meaning the key is not (or no longer)
// connected to an account.
func notConnected(u *api.UserResponse, err error) bool {
	var authErr api.AuthorizationError
	if errors.As(err, &authErr) {
		return authErr.StatusCode == http.StatusUnauthorized
	}
	return err == nil && (u == nil || u.Name == "")
}

func newCloudID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// updateCloud changes the cloud section under the settings writer's lock
// and writes it back, validated.
func updateCloud(change func(*xollama.CloudSettings) error) (*xollama.CloudSettings, error) {
	settingsWrite.Lock()
	defer settingsWrite.Unlock()

	cur, err := envconfig.ReadSettings()
	if err != nil {
		return nil, err
	}
	var cs xollama.CloudSettings
	if raw := cur.Rest[settingsCloud]; len(raw) > 0 {
		if err := jsonUnmarshalStrict(raw, &cs); err != nil {
			return nil, fmt.Errorf("the settings file's cloud section: %w", err)
		}
	}
	if err := change(&cs); err != nil {
		return nil, err
	}
	if err := cs.Validate(); err != nil {
		return nil, &cloudError{http.StatusBadRequest, err.Error()}
	}
	if cs.IsZero() {
		delete(cur.Rest, settingsCloud)
	} else {
		data, err := json.Marshal(cs)
		if err != nil {
			return nil, err
		}
		if cur.Rest == nil {
			cur.Rest = map[string]json.RawMessage{}
		}
		cur.Rest[settingsCloud] = data
	}
	if err := writeSettings(cur); err != nil {
		return nil, err
	}
	envconfig.ReloadSettings()
	return &cs, nil
}

// cloudError is a refusal with its status.
type cloudError struct {
	status int
	msg    string
}

func (e *cloudError) Error() string { return e.msg }

func cloudRefuse(status int, format string, args ...any) error {
	return &cloudError{status, fmt.Sprintf(format, args...)}
}

// CloudAccountsHandler runs one action on the cloud accounts. Only from
// this machine, never through a proxy: it makes and removes keys that act as
// ollama.com accounts.
func (s *Server) CloudAccountsHandler(c *gin.Context) {
	if !loopbackPeer(c) || proxied(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cloud accounts are managed only from the server's own machine, not through a proxy"})
		return
	}
	var req api.CloudAccountsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	resp, err := runCloudAction(c.Request.Context(), req)
	if err != nil {
		status := http.StatusInternalServerError
		var ce *cloudError
		if errors.As(err, &ce) {
			status = ce.status
		}
		c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func runCloudAction(ctx context.Context, req api.CloudAccountsRequest) (*api.CloudAccountsResponse, error) {
	var (
		cs      *xollama.CloudSettings
		err     error
		target  string
		connect string
	)
	named := func(cs *xollama.CloudSettings) (int, error) {
		i, ok := cs.Find(req.Account)
		if !ok {
			return -1, cloudRefuse(http.StatusNotFound, "no cloud account named %q", req.Account)
		}
		return i, nil
	}
	switch req.Action {
	case api.CloudList, "":
		cs = serverCloud()
	case api.CloudAdd:
		label := strings.TrimSpace(req.Label)
		if !xollama.ValidCloudLabel(label) {
			return nil, cloudRefuse(http.StatusBadRequest, "label %q must be 1 to 32 letters, digits, '.', '_' or '-', starting with a letter or digit", label)
		}
		var id, pub string
		withKey := strings.TrimSpace(req.APIKey) != ""
		cs, err = updateCloud(func(cs *xollama.CloudSettings) error {
			if len(cs.Accounts) >= xollama.MaxCloudAccounts {
				return cloudRefuse(http.StatusConflict, "%d accounts are already added; that is the most beside the primary", xollama.MaxCloudAccounts)
			}
			if _, dup := cs.Find(label); dup {
				return cloudRefuse(http.StatusConflict, "an account is already labelled %q", label)
			}
			var err error
			if id, err = newCloudID(); err != nil {
				return err
			}
			a := xollama.CloudAccount{ID: id, Label: label}
			if withKey {
				// Checked before the entry exists: a refused key adds nothing.
				if err := checkCloudAPIKey(ctx, id, label, req.APIKey); err != nil {
					removeCloudDir(id)
					return err
				}
				a.Auth = xollama.CloudAuthAPIKey
			} else if pub, err = newCloudKey(id); err != nil {
				return err
			}
			cs.Accounts = append(cs.Accounts, a)
			return nil
		})
		if err != nil {
			return nil, err
		}
		target = id
		if !withKey {
			connect = cloudConnectURL(label, pub)
			break
		}
		if cs, target, err = verifyCloudAccount(ctx, id); err != nil {
			// The same ollama.com account as another: the new entry goes.
			_, _ = updateCloud(func(cs *xollama.CloudSettings) error {
				if i, ok := cs.Find(id); ok {
					cs.Accounts = append(cs.Accounts[:i], cs.Accounts[i+1:]...)
				}
				return nil
			})
			removeCloudDir(id)
			return nil, err
		}
	case api.CloudSignin:
		if strings.TrimSpace(req.APIKey) != "" {
			cs, target, err = signinCloudAPIKey(ctx, req)
			break
		}
		cs, err = updateCloud(func(cs *xollama.CloudSettings) error {
			i, err := named(cs)
			if err != nil {
				return err
			}
			a := &cs.Accounts[i]
			if a.UsesAPIKey() {
				// Back to a connected key: the API key goes.
				if p, err := cloudCredPath(*a); err == nil {
					_ = os.Remove(p)
				}
				a.Auth = xollama.CloudAuthKey
			}
			p, err := cloudKeyPath(a.ID)
			if err != nil {
				return err
			}
			pub, err := cloudPublicKey(p)
			if errors.Is(err, os.ErrNotExist) {
				// The key went missing: a new one, connected afresh.
				pub, err = newCloudKey(a.ID)
			}
			if err != nil {
				return err
			}
			a.SignedOut, a.VerifiedAt, a.Account = false, "", ""
			target, connect = a.ID, cloudConnectURL(a.Label, pub)
			return nil
		})
	case api.CloudVerify:
		cs, target, err = verifyCloudAccount(ctx, req.Account)
	case api.CloudSignout, api.CloudRemove:
		cur := orEmpty(serverCloud())
		i, ferr := named(cur)
		if ferr != nil {
			return nil, ferr
		}
		a := cur.Accounts[i]
		if a.State() != xollama.CloudSignedOut {
			if serr := signoutCloudKey(ctx, a); serr != nil {
				if req.Action == api.CloudSignout {
					return nil, serr
				}
				slog.Warn("cloud account removed without signing it out at ollama.com; remove its key there by hand", "account", a.Label, "error", serr)
			}
		}
		cs, err = updateCloud(func(cs *xollama.CloudSettings) error {
			i, err := named(cs)
			if err != nil {
				return err
			}
			if req.Action == api.CloudSignout {
				cs.Accounts[i].SignedOut = true
				target = cs.Accounts[i].ID
				return nil
			}
			id := cs.Accounts[i].ID
			cs.Accounts = append(cs.Accounts[:i], cs.Accounts[i+1:]...)
			if p, err := cloudKeyPath(id); err == nil {
				if err := os.RemoveAll(filepath.Dir(p)); err != nil {
					return err
				}
			}
			return nil
		})
	case api.CloudSet:
		cs, err = updateCloud(func(cs *xollama.CloudSettings) error {
			i, err := named(cs)
			if err != nil {
				return err
			}
			a := &cs.Accounts[i]
			if l := strings.TrimSpace(req.Label); l != "" {
				if j, dup := cs.Find(l); dup && j != i {
					return cloudRefuse(http.StatusConflict, "an account is already labelled %q", l)
				}
				a.Label = l
			}
			if req.Parallel != nil {
				a.Parallel = *req.Parallel
			}
			if req.Disabled != nil {
				a.Disabled = *req.Disabled
			}
			target = a.ID
			return nil
		})
	case api.CloudPool:
		cs, err = updateCloud(func(cs *xollama.CloudSettings) error {
			if req.AccountsAtOnce != nil {
				cs.AccountsAtOnce = *req.AccountsAtOnce
			}
			if req.ParallelPerAccount != nil {
				cs.ParallelPerAccount = *req.ParallelPerAccount
			}
			if req.Primary != nil {
				cs.Primary = *req.Primary
			}
			return nil
		})
	default:
		return nil, cloudRefuse(http.StatusBadRequest, "action must be one of list, add, verify, signin, signout, set, remove, pool")
	}
	if err != nil {
		return nil, err
	}
	resp := cloudAccountsView(ctx, orEmpty(cs))
	resp.ConnectURL = connect
	for i := range resp.Accounts {
		if target != "" && resp.Accounts[i].ID == target {
			a := resp.Accounts[i]
			resp.Account = &a
		}
	}
	return resp, nil
}

func orEmpty(cs *xollama.CloudSettings) *xollama.CloudSettings {
	if cs == nil {
		return &xollama.CloudSettings{}
	}
	return cs
}

// signoutCloudKey removes the account's key at ollama.com. A key the
// account no longer knows is signed out already.
func signoutCloudKey(ctx context.Context, a xollama.CloudAccount) error {
	if a.UsesAPIKey() {
		// ollama.com has no route that revokes an API key: the server
		// forgets it, and the operator revokes it at ollama.com.
		p, err := cloudCredPath(a)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		slog.Info("cloud account's API key removed from this server; revoke it at https://ollama.com/settings/keys if it is no longer used anywhere", "account", a.Label)
		return nil
	}
	p, err := cloudKeyPath(a.ID)
	if err != nil {
		return err
	}
	pub, err := cloudPublicKey(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cloudCallTimeout)
	defer cancel()
	if err := cloudDisconnect(ctx, p, pub); err != nil {
		var authErr api.AuthorizationError
		if errors.As(err, &authErr) {
			return nil
		}
		return cloudRefuse(http.StatusBadGateway, "ollama.com did not sign %q out: %v", a.Label, err)
	}
	return nil
}

// removeCloudDir removes an account's directory, keys and all.
func removeCloudDir(id string) {
	if p, err := cloudKeyPath(id); err == nil {
		_ = os.RemoveAll(filepath.Dir(p))
	}
}

// signinCloudAPIKey gives an existing account an API key: a new one for an
// API-key account, or in place of its connected key, which is signed out at
// ollama.com first. The key is checked before anything is replaced.
func signinCloudAPIKey(ctx context.Context, req api.CloudAccountsRequest) (*xollama.CloudSettings, string, error) {
	cur := orEmpty(serverCloud())
	i, ok := cur.Find(req.Account)
	if !ok {
		return nil, "", cloudRefuse(http.StatusNotFound, "no cloud account named %q", req.Account)
	}
	a := cur.Accounts[i]
	if err := checkCloudAPIKey(ctx, a.ID, a.Label, req.APIKey); err != nil {
		return nil, "", err
	}
	if !a.UsesAPIKey() {
		if a.State() != xollama.CloudSignedOut {
			if err := signoutCloudKey(ctx, a); err != nil {
				slog.Warn("cloud account's connected key not signed out at ollama.com; remove it there by hand", "account", a.Label, "error", err)
			}
		}
		if p, err := cloudKeyPath(a.ID); err == nil {
			_ = os.Remove(p)
		}
	}
	if _, err := updateCloud(func(cs *xollama.CloudSettings) error {
		i, ok := cs.Find(a.ID)
		if !ok {
			return cloudRefuse(http.StatusNotFound, "no cloud account named %q", req.Account)
		}
		cs.Accounts[i].Auth = xollama.CloudAuthAPIKey
		cs.Accounts[i].SignedOut, cs.Accounts[i].VerifiedAt, cs.Accounts[i].Account = false, "", ""
		return nil
	}); err != nil {
		return nil, "", err
	}
	return verifyCloudAccount(ctx, a.ID)
}

// verifyCloudAccount asks ollama.com who the account's key belongs to. A key
// not connected yet keeps the account pending, not an error. A key
// connected to an account already in the pool (another added one, or the
// primary) is disconnected again and refused: it would count that account's
// requests twice.
func verifyCloudAccount(ctx context.Context, name string) (*xollama.CloudSettings, string, error) {
	cur := orEmpty(serverCloud())
	i, ok := cur.Find(name)
	if !ok {
		return nil, "", cloudRefuse(http.StatusNotFound, "no cloud account named %q", name)
	}
	a := cur.Accounts[i]
	p, err := cloudCredPath(a)
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, cloudCallTimeout)
	defer cancel()
	u, err := cloudWhoami(cctx, p)
	if notConnected(u, err) {
		return cur, a.ID, nil
	}
	if err != nil {
		return nil, "", cloudRefuse(http.StatusBadGateway, "ollama.com did not answer for %q: %v", a.Label, err)
	}
	hash := cloudAccountHash(u)
	dup := ""
	for j, o := range cur.Accounts {
		if j != i && o.Account == hash {
			dup = strconv.Quote(o.Label)
		}
	}
	if dup == "" {
		if pu, perr := cloudWhoami(cctx, ""); perr == nil && pu != nil && pu.Name != "" && cloudAccountHash(pu) == hash {
			dup = "the primary account"
		}
	}
	if dup != "" && a.UsesAPIKey() {
		_ = os.Remove(p)
		return nil, "", cloudRefuse(http.StatusConflict, "the API key for %q belongs to the same ollama.com account as %s; it was not kept. Use a key of another ollama.com account", a.Label, dup)
	}
	if dup != "" {
		if pub, perr := cloudPublicKey(p); perr == nil {
			_ = cloudDisconnect(cctx, p, pub)
		}
		return nil, "", cloudRefuse(http.StatusConflict, "%q is connected to the same ollama.com account as %s; its key was disconnected again. Sign the browser in to another ollama.com account, then `xollama tweak server cloud login %s`", a.Label, dup, a.Label)
	}
	plan := strings.TrimSpace(u.Plan)
	if plan == "" {
		plan = "free"
	}
	if _, known := xollama.PlanParallel(plan); !known {
		slog.Warn("cloud account plan not in the table; it takes the default requests at once", "account", a.Label, "plan", plan, "parallel", xollama.DefaultCloudParallel)
	}
	cs, err := updateCloud(func(cs *xollama.CloudSettings) error {
		i, ok := cs.Find(a.ID)
		if !ok {
			return cloudRefuse(http.StatusNotFound, "no cloud account named %q", name)
		}
		cs.Accounts[i].Account, cs.Accounts[i].Plan = hash, plan
		cs.Accounts[i].SignedOut = false
		cs.Accounts[i].VerifiedAt = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	return cs, a.ID, err
}

// cloudAccountsView is the accounts as the admin route reports them, the
// primary first. The primary's state is asked of ollama.com.
func cloudAccountsView(ctx context.Context, cs *xollama.CloudSettings) *api.CloudAccountsResponse {
	resp := &api.CloudAccountsResponse{
		Accounts:           []api.CloudAccountInfo{},
		AccountsAtOnce:     cs.AccountsAtOnce,
		ParallelPerAccount: cs.ParallelPerAccount,
		Primary:            cs.Primary,
	}
	if resp.Primary == "" {
		resp.Primary = xollama.CloudPrimaryInclude
	}
	primary := api.CloudAccountInfo{Label: "primary", Primary: true, Excluded: !cs.PrimaryIncluded(), State: xollama.CloudSignedOut}
	cctx, cancel := context.WithTimeout(ctx, cloudCallTimeout)
	u, err := cloudWhoami(cctx, "")
	cancel()
	switch {
	case notConnected(u, err):
	case err != nil:
		primary.State = "unreachable"
	default:
		primary.State, primary.Plan = xollama.CloudSignedIn, strings.TrimSpace(u.Plan)
		if primary.Plan == "" {
			primary.Plan = "free"
		}
	}
	primary.Parallel = cs.AccountParallel(xollama.CloudAccount{Plan: primary.Plan})
	resp.Accounts = append(resp.Accounts, primary)
	for _, a := range cs.Accounts {
		resp.Accounts = append(resp.Accounts, api.CloudAccountInfo{
			ID: a.ID, Label: a.Label, Plan: a.Plan,
			Parallel: cs.AccountParallel(a), ParallelSet: a.Parallel,
			State: a.State(), Auth: a.Auth, Disabled: a.Disabled, VerifiedAt: a.VerifiedAt,
		})
	}
	resp.Capacity = cloudCapacity(cs, resp.Accounts)
	return resp
}

// cloudCapacity is the requests at once the usable accounts give, taking
// AccountsAtOnce of them (all when 0) in the order listed.
func cloudCapacity(cs *xollama.CloudSettings, accounts []api.CloudAccountInfo) int {
	total, used := 0, 0
	for _, a := range accounts {
		if a.State != xollama.CloudSignedIn || a.Disabled || a.Excluded {
			continue
		}
		if cs.AccountsAtOnce > 0 && used == cs.AccountsAtOnce {
			break
		}
		total += a.Parallel
		used++
	}
	return total
}
