package kernel

import (
	"net/http"
)

// LimitInFlight 限制在途请求数，超限返回 503。
func LimitInFlight(h http.Handler, n int) http.Handler {
	if n <= 0 {
		n = EdgeInFlight
	}
	sem := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			h.ServeHTTP(w, r)
		default:
			http.Error(w, "too many requests", http.StatusServiceUnavailable)
		}
	})
}

// NewProductionHTTPServer 按架构预算设置读写超时与请求头上限。
func NewProductionHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           LimitInFlight(h, EdgeInFlight),
		ReadHeaderTimeout: HTTPReadHeaderTimeout,
		ReadTimeout:       HTTPReadTimeout,
		WriteTimeout:      HTTPWriteTimeout,
		IdleTimeout:       HTTPIdleTimeout,
		MaxHeaderBytes:    HTTPMaxHeaderBytes,
	}
}
