package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// The proxy must carry a protocol switch both ways, which an ordinary forward
// of the request does not: the fake engine echoes after a 101.
func TestARealtimeUpgradeReachesTheEngine(t *testing.T) {
	var path, query string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		rw.Flush()
		io.Copy(conn, rw)
	}))
	defer engine.Close()
	_, port, _ := net.SplitHostPort(engine.Listener.Addr().String())
	n, _ := strconv.Atoi(port)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/realtime", func(c *gin.Context) { proxyRealtime(c, n) })
	front := httptest.NewServer(r)
	defer front.Close()

	conn, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /v1/realtime?model=m HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	buf := make([]byte, 4096)
	got := ""
	for len(got) < 12 {
		k, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got += string(buf[:k])
	}
	if got[:12] != "HTTP/1.1 101" {
		t.Fatalf("reply %q", got)
	}
	io.WriteString(conn, "ping")
	for {
		k, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:k]) == "ping" {
			break
		}
	}
	if path != "/v1/realtime" || query != "model=m" {
		t.Errorf("engine saw %q ?%q", path, query)
	}
}
