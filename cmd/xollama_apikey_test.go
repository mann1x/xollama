package cmd

import "testing"

func TestTheKeyInClearWarningOnlyForPlainHTTPToAnotherMachine(t *testing.T) {
	for _, c := range []struct {
		scheme, host string
		want         bool
	}{
		{"http", "192.168.1.5", true},
		{"http", "gpu-box.lan", true},
		{"https", "gpu-box.lan", false},
		{"http", "127.0.0.1", false},
		{"http", "::1", false},
		{"http", "localhost", false},
		{"HTTP", "10.0.0.1", true},
	} {
		if got := keyInClear(c.scheme, c.host); got != c.want {
			t.Errorf("keyInClear(%s, %s) = %v, want %v", c.scheme, c.host, got, c.want)
		}
	}
}
