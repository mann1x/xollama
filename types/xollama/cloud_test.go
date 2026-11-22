package xollama

import (
	"fmt"
	"strings"
	"testing"
)

func TestTheCloudSectionIsValidated(t *testing.T) {
	ok := CloudAccount{ID: "0a1b2c3d", Label: "work-2"}
	cases := []struct {
		name string
		cs   CloudSettings
		want string
	}{
		{"fine", CloudSettings{Accounts: []CloudAccount{ok}, AccountsAtOnce: 3, Primary: CloudPrimaryExclude}, ""},
		{"bad primary", CloudSettings{Primary: "maybe"}, "primary"},
		{"bad id", CloudSettings{Accounts: []CloudAccount{{ID: "../x", Label: "a"}}}, "eight hex"},
		{"bad label", CloudSettings{Accounts: []CloudAccount{{ID: "0a1b2c3d", Label: "has space"}}}, "label"},
		{"same label", CloudSettings{Accounts: []CloudAccount{ok, {ID: "1a1b2c3d", Label: "WORK-2"}}}, "used twice"},
		{"same account", CloudSettings{Accounts: []CloudAccount{
			{ID: "0a1b2c3d", Label: "a", Account: "h"}, {ID: "1a1b2c3d", Label: "b", Account: "h"},
		}}, "same ollama.com account"},
		{"negative at once", CloudSettings{AccountsAtOnce: -1}, "accounts_at_once"},
		{"too parallel", CloudSettings{ParallelPerAccount: 65}, "parallel_per_account"},
	}
	for _, c := range cases {
		err := c.cs.Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}

	var many CloudSettings
	for i := range MaxCloudAccounts + 1 {
		many.Accounts = append(many.Accounts, CloudAccount{ID: fmt.Sprintf("%08x", i), Label: fmt.Sprintf("a%d", i)})
	}
	if err := many.Validate(); err == nil {
		t.Fatal("26 added accounts were accepted")
	}
}

func TestAnAccountsParallelIsItsOwnThenThePoolsThenItsPlans(t *testing.T) {
	cs := &CloudSettings{}
	if n := cs.AccountParallel(CloudAccount{Plan: "Pro"}); n != 3 {
		t.Fatalf("pro = %d, want 3", n)
	}
	if n := cs.AccountParallel(CloudAccount{Plan: "max"}); n != 10 {
		t.Fatalf("max = %d, want 10", n)
	}
	if n, known := PlanParallel("enterprise-2030"); known || n != DefaultCloudParallel {
		t.Fatalf("unknown plan = %d known=%v", n, known)
	}
	cs.ParallelPerAccount = 5
	if n := cs.AccountParallel(CloudAccount{Plan: "max"}); n != 5 {
		t.Fatalf("pool override = %d, want 5", n)
	}
	if n := cs.AccountParallel(CloudAccount{Plan: "max", Parallel: 2}); n != 2 {
		t.Fatalf("account's own = %d, want 2", n)
	}
}
