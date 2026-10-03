package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// flakyEngine is a live engine whose first fail dials do not connect, as the
// loopback dial on eleven2go did once (a0968aea, council run 3).
func flakyEngine(t *testing.T, opencoti bool, fail int32) (*llamaServerRunner, *atomic.Int32) {
	t.Helper()
	was, wasBackoff := healthRetries, healthBackoff
	healthBackoff = time.Millisecond
	t.Cleanup(func() { healthRetries, healthBackoff = was, wasBackoff })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	var dials atomic.Int32
	var d net.Dialer
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dials.Add(1) <= fail {
				return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connectex: A connection attempt failed because the connected party did not properly respond")}
			}
			return d.DialContext(ctx, network, addr)
		},
	}}
	return &llamaServerRunner{port: port, client: client, usedOpencoti: opencoti, cmd: &exec.Cmd{}}, &dials
}

func TestAHealthCheckThatCouldNotReachALiveOpencotiIsTriedAgain(t *testing.T) {
	s, dials := flakyEngine(t, true, 2)
	status, err := s.getServerStatusRetry(t.Context())
	if err != nil || status != ServerStatusReady || dials.Load() != 3 {
		t.Fatalf("status %v err %v after %d dials, want ready on the third", status, err, dials.Load())
	}
}

func TestAHealthRetryGivesUpAfterItsTries(t *testing.T) {
	s, dials := flakyEngine(t, true, 100)
	if _, err := s.getServerStatusRetry(t.Context()); err == nil || dials.Load() != int32(1+healthRetries) {
		t.Fatalf("err %v after %d dials, want a failure after %d", err, dials.Load(), 1+healthRetries)
	}
}

// Off means off: stock llama.cpp fails on the first dial, as upstream does,
// and so does an engine that has exited.
func TestAHealthCheckIsNotRetriedOffOpencotiOrOnAnExitedEngine(t *testing.T) {
	s, dials := flakyEngine(t, false, 1)
	if _, err := s.getServerStatusRetry(t.Context()); err == nil || dials.Load() != 1 {
		t.Errorf("stock: err %v after %d dials, want upstream's single failure", err, dials.Load())
	}
	s, dials = flakyEngine(t, true, 1)
	exited := exec.Command(os.Args[0], "-test.run=^$")
	if err := exited.Run(); err != nil {
		t.Skip("cannot run a process to exit:", err)
	}
	s.cmd = exited
	if _, err := s.getServerStatusRetry(t.Context()); err == nil || dials.Load() != 0 {
		t.Errorf("exited: err %v after %d dials, want the exit reported without a dial", err, dials.Load())
	}
}
