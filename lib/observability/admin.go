package observability

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
)

// Handler 返回御锋管理面探针（独立监听端口，不得挂在业务监听器上）。
// ready 非空时 /readyz 调用它；失败返回 503。
func Handler(ready func(context.Context) error, version, contract string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"version":%q,"contractVersion":%q}`+"\n", version, contract)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		_, _ = fmt.Fprintf(w, "# HELP yufeng_build_info 构建信息\n# TYPE yufeng_build_info gauge\nyufeng_build_info{version=%q} 1\n", version)
		_, _ = fmt.Fprintf(w, "# HELP yufeng_go_goroutines 当前 goroutine 数\n# TYPE yufeng_go_goroutines gauge\nyufeng_go_goroutines %d\n", runtime.NumGoroutine())
		_, _ = fmt.Fprintf(w, "# HELP yufeng_go_memstats_alloc_bytes 已分配堆内存字节\n# TYPE yufeng_go_memstats_alloc_bytes gauge\nyufeng_go_memstats_alloc_bytes %d\n", mem.Alloc)
		_, _ = w.Write([]byte(Default().Prometheus()))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			if err := ready(r.Context()); err != nil {
				http.Error(w, `{"code":"unavailable","message":"not ready"}`, http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	return mux
}
