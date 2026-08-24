package kernel

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func netListen(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}

func TestNewProductionHTTPServerTimeouts(t *testing.T) {
	srv := NewProductionHTTPServer(":0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if srv.ReadHeaderTimeout != HTTPReadHeaderTimeout || srv.ReadTimeout != HTTPReadTimeout {
		t.Fatalf("timeouts %+v", srv)
	}
	if srv.MaxHeaderBytes != HTTPMaxHeaderBytes {
		t.Fatal("header limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProductionHTTPServerDrainsInFlight(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	srv := NewProductionHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		close(done)
	}))
	ln, err := netListen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	go func() {
		resp, err := http.Get("http://" + srv.Addr + "/")
		if err == nil {
			resp.Body.Close() //nolint:errcheck // 辅助协程只用于触发优雅关闭。
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("in-flight request must complete during shutdown")
	}
}
