package tweak

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// cloudServer answers the cloud accounts route; verify reports the account
// connected from its third call on.
func cloudServer(t *testing.T) (*[]api.CloudAccountsRequest, func()) {
	t.Helper()
	var seen []api.CloudAccountsRequest
	verifies := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != api.XollamaCloudAccountsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req api.CloudAccountsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		seen = append(seen, req)
		acct := api.CloudAccountInfo{ID: "0a1b2c3d", Label: "work-2", State: xollama.CloudPending, Parallel: 3}
		resp := api.CloudAccountsResponse{Accounts: []api.CloudAccountInfo{{Label: "primary", Primary: true, State: xollama.CloudSignedIn, Plan: "pro", Parallel: 3}, acct}}
		switch req.Action {
		case api.CloudAdd:
			resp.ConnectURL = "https://ollama.com/connect?name=h-xollama-work-2&key=k"
		case api.CloudVerify:
			if verifies++; verifies >= 3 {
				acct.State, acct.Plan = xollama.CloudSignedIn, "pro"
			}
		}
		resp.Account = &acct
		json.NewEncoder(w).Encode(resp)
	}))
	t.Setenv("XOLLAMA_HOST", srv.URL)
	t.Setenv("XOLLAMA_API_KEY", "")
	return &seen, srv.Close
}

func TestAddPrintsTheConnectPageAndWaitsForTheAccount(t *testing.T) {
	seen, stop := cloudServer(t)
	defer stop()
	old := cloudPollEvery
	cloudPollEvery = time.Millisecond
	defer func() { cloudPollEvery = old }()

	cmd := cloudCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"add", "work-2"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://ollama.com/connect?name=h-xollama-work-2") || !strings.Contains(out.String(), "Connected") {
		t.Fatalf("output:\n%s", out.String())
	}
	if (*seen)[0].Action != api.CloudAdd || (*seen)[0].Label != "work-2" {
		t.Fatalf("first call = %+v, want add with the label", (*seen)[0])
	}
	if n := len(*seen); n != 4 || (*seen)[n-1].Action != api.CloudVerify || (*seen)[n-1].Account != "work-2" {
		t.Fatalf("calls = %+v, want add then verify until connected", *seen)
	}
}

func TestPoolAndSetFlagsBecomeOneAction(t *testing.T) {
	seen, stop := cloudServer(t)
	defer stop()

	cmd := cloudCommand(Options{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--accounts-at-once", "3", "--parallel-per-account", "plan", "--primary", "exclude"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := (*seen)[0]
	if got.Action != api.CloudPool || *got.AccountsAtOnce != 3 || *got.ParallelPerAccount != 0 || *got.Primary != "exclude" {
		t.Fatalf("pool request = %+v", got)
	}

	cmd = cloudCommand(Options{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"set", "work-2", "--parallel", "2", "--disable"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got = (*seen)[1]
	if got.Action != api.CloudSet || got.Account != "work-2" || *got.Parallel != 2 || !*got.Disabled {
		t.Fatalf("set request = %+v", got)
	}

	cmd = cloudCommand(Options{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--primary", "sometimes"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("--primary sometimes was accepted")
	}
}
