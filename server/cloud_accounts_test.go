package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// fakeOllamaCom stands in for ollama.com: which key file belongs to which
// account, and which keys were disconnected.
type fakeOllamaCom struct {
	users        map[string]*api.UserResponse // by key file; "" is the primary
	disconnected []string
}

func withFakeOllamaCom(t *testing.T) *fakeOllamaCom {
	t.Helper()
	f := &fakeOllamaCom{users: map[string]*api.UserResponse{}}
	whoami, disconnect := cloudWhoami, cloudDisconnect
	cloudWhoami = func(_ context.Context, keyPath string) (*api.UserResponse, error) {
		if u, ok := f.users[keyPath]; ok {
			return u, nil
		}
		return nil, api.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}
	cloudDisconnect = func(_ context.Context, keyPath, _ string) error {
		f.disconnected = append(f.disconnected, keyPath)
		delete(f.users, keyPath)
		return nil
	}
	t.Cleanup(func() { cloudWhoami, cloudDisconnect = whoami, disconnect })
	return f
}

func user(name, plan string) *api.UserResponse {
	return &api.UserResponse{ID: uuid.New(), Name: name, Email: name + "@example.com", Plan: plan}
}

func cloudCall(t *testing.T, remote string, req api.CloudAccountsRequest) (int, api.CloudAccountsResponse, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s := &Server{}
	r.POST(api.XollamaCloudAccountsPath, s.CloudAccountsHandler)
	b, _ := json.Marshal(req)
	hr := httptest.NewRequest(http.MethodPost, api.XollamaCloudAccountsPath, strings.NewReader(string(b)))
	hr.RemoteAddr = remote
	w := httptest.NewRecorder()
	r.ServeHTTP(w, hr)
	var resp api.CloudAccountsResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, resp, w.Body.String()
}

const local = "127.0.0.1:5000"

func addAccount(t *testing.T, label string) api.CloudAccountsResponse {
	t.Helper()
	code, resp, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudAdd, Label: label})
	if code != http.StatusOK {
		t.Fatalf("add %s: %d %s", label, code, body)
	}
	return resp
}

func keyFileOf(t *testing.T, id string) string {
	t.Helper()
	p, err := cloudKeyPath(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCloudAccountsAreManagedOnlyFromThisMachine(t *testing.T) {
	p := settingsHome(t)
	withFakeOllamaCom(t)
	if code, _, _ := cloudCall(t, "192.0.2.10:4000", api.CloudAccountsRequest{Action: api.CloudAdd, Label: "a"}); code != http.StatusForbidden {
		t.Fatalf("remote add: status %d, want 403", code)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("a refused add left a settings file: %v", err)
	}
}

func TestAnAddedAccountGetsItsOwnKeyAndWaitsForTheConnectPage(t *testing.T) {
	settingsHome(t)
	withFakeOllamaCom(t)
	resp := addAccount(t, "work-2")
	if resp.Account == nil || resp.Account.State != xollama.CloudPending {
		t.Fatalf("account = %+v, want pending", resp.Account)
	}
	if !strings.HasPrefix(resp.ConnectURL, "https://ollama.com/connect?name=") || !strings.Contains(resp.ConnectURL, "-xollama-work-2") {
		t.Fatalf("connect URL = %q", resp.ConnectURL)
	}
	key := keyFileOf(t, resp.Account.ID)
	st, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, want 0600", st.Mode().Perm())
	}
	home, _ := os.UserHomeDir()
	if filepath.Dir(filepath.Dir(key)) != filepath.Join(home, ".ollama", "xollama-cloud") {
		t.Fatalf("key at %s, not under ~/.ollama/xollama-cloud", key)
	}
	// Not connected yet: verify keeps it pending, without an error.
	code, v, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "work-2"})
	if code != http.StatusOK || v.Account.State != xollama.CloudPending {
		t.Fatalf("verify before connect: %d %s", code, body)
	}
}

func TestAVerifiedAccountKeepsItsPlanButNoEmail(t *testing.T) {
	sp := settingsHome(t)
	f := withFakeOllamaCom(t)
	f.users[""] = user("me", "pro")
	a := addAccount(t, "second")
	f.users[keyFileOf(t, a.Account.ID)] = user("other", "max")
	code, v, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "second"})
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, body)
	}
	if v.Account.State != xollama.CloudSignedIn || v.Account.Plan != "max" || v.Account.Parallel != 10 {
		t.Fatalf("account = %+v, want signed in, max, 10 at once", v.Account)
	}
	// primary pro (3) + max (10)
	if v.Capacity != 13 {
		t.Fatalf("capacity = %d, want 13", v.Capacity)
	}
	data, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"@example.com", "other", "PRIVATE KEY"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("settings file holds %q:\n%s", leak, data)
		}
	}
}

func TestTheSameOllamaComAccountIsNotAddedTwice(t *testing.T) {
	settingsHome(t)
	f := withFakeOllamaCom(t)
	primary := user("me", "pro")
	f.users[""] = primary
	a := addAccount(t, "again")
	k := keyFileOf(t, a.Account.ID)
	f.users[k] = primary
	code, _, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "again"})
	if code != http.StatusConflict || !strings.Contains(body, "primary") {
		t.Fatalf("verify of the primary's account: %d %s, want 409 naming the primary", code, body)
	}
	if len(f.disconnected) != 1 || f.disconnected[0] != k {
		t.Fatalf("disconnected = %v, want the duplicate key disconnected again", f.disconnected)
	}

	b := addAccount(t, "b")
	c := addAccount(t, "c")
	same := user("shared", "pro")
	f.users[keyFileOf(t, b.Account.ID)] = same
	if code, _, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "b"}); code != http.StatusOK {
		t.Fatalf("verify b: %d %s", code, body)
	}
	f.users[keyFileOf(t, c.Account.ID)] = same
	if code, _, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "c"}); code != http.StatusConflict || !strings.Contains(body, `as \"b\"`) {
		t.Fatalf("verify c on b's account: %d %s, want 409 naming b", code, body)
	}
}

func TestTheTwentySixthAccountIsRefused(t *testing.T) {
	settingsHome(t)
	withFakeOllamaCom(t)
	for i := range xollama.MaxCloudAccounts {
		addAccount(t, "acct"+string(rune('a'+i)))
	}
	code, _, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudAdd, Label: "one-more"})
	if code != http.StatusConflict {
		t.Fatalf("26th add: %d %s, want 409", code, body)
	}
}

func TestRemoveSignsOutAndDeletesTheKey(t *testing.T) {
	sp := settingsHome(t)
	f := withFakeOllamaCom(t)
	a := addAccount(t, "gone")
	k := keyFileOf(t, a.Account.ID)
	f.users[k] = user("gone", "pro")
	cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "gone"})
	code, resp, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudRemove, Account: "gone"})
	if code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, body)
	}
	if len(f.disconnected) != 1 || f.disconnected[0] != k {
		t.Fatalf("disconnected = %v, want the key signed out at ollama.com", f.disconnected)
	}
	if _, err := os.Stat(filepath.Dir(k)); !os.IsNotExist(err) {
		t.Fatalf("key directory still there: %v", err)
	}
	if len(resp.Accounts) != 1 || !resp.Accounts[0].Primary {
		t.Fatalf("accounts = %+v, want only the primary", resp.Accounts)
	}
	if _, err := os.Stat(sp); !os.IsNotExist(err) {
		t.Fatalf("an empty cloud section left a settings file: %v", err)
	}
}

func TestSignoutKeepsTheEntryAndSigninConnectsAfresh(t *testing.T) {
	settingsHome(t)
	f := withFakeOllamaCom(t)
	a := addAccount(t, "x")
	k := keyFileOf(t, a.Account.ID)
	f.users[k] = user("x", "pro")
	cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: "x"})
	_, out, _ := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudSignout, Account: "x"})
	if out.Account == nil || out.Account.State != xollama.CloudSignedOut {
		t.Fatalf("after signout: %+v", out.Account)
	}
	_, in, _ := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudSignin, Account: "x"})
	if in.Account == nil || in.Account.State != xollama.CloudPending || in.ConnectURL == "" {
		t.Fatalf("after signin: %+v %q", in.Account, in.ConnectURL)
	}
}

func TestPoolSettingsAndAccountsAtOnceSetTheCapacity(t *testing.T) {
	settingsHome(t)
	f := withFakeOllamaCom(t)
	f.users[""] = user("me", "pro")
	for _, l := range []string{"a", "b", "c"} {
		r := addAccount(t, l)
		f.users[keyFileOf(t, r.Account.ID)] = user(l, "pro")
		cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudVerify, Account: l})
	}
	three, exclude := 3, xollama.CloudPrimaryExclude
	code, resp, body := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudPool, AccountsAtOnce: &three, Primary: &exclude})
	if code != http.StatusOK {
		t.Fatalf("pool: %d %s", code, body)
	}
	if resp.Capacity != 9 {
		t.Fatalf("3 Pro accounts at once, primary excluded: capacity %d, want 9", resp.Capacity)
	}
	off := true
	cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudSet, Account: "b", Disabled: &off})
	two := 2
	_, resp, _ = cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudSet, Account: "c", Parallel: &two})
	if resp.Capacity != 5 {
		t.Fatalf("a (3) + c (2), b disabled: capacity %d, want 5", resp.Capacity)
	}
	bad := -1
	if code, _, _ := cloudCall(t, local, api.CloudAccountsRequest{Action: api.CloudPool, AccountsAtOnce: &bad}); code != http.StatusBadRequest {
		t.Fatalf("negative accounts_at_once: %d, want 400", code)
	}
}
