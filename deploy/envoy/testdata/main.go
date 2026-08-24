// envoyfixture 用真实外部授权处理器和回显应用验证 Envoy 接线；只用于部署验收。
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	"yufeng/lib/edgecore"
)

func main() {
	mode := flag.String("mode", "authz", "运行模式：authz 或 application")
	addr := flag.String("addr", ":18081", "监听地址")
	flag.Parse()

	var handler http.Handler
	switch *mode {
	case "authz":
		handler = authorizationHandler()
	case "application":
		handler = applicationHandler()
	default:
		log.Fatalf("unsupported mode %q", *mode)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func authorizationHandler() http.Handler {
	auth := edgecore.NewExtAuthz("envoy-fixture", fixtureGate)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", auth)
	return mux
}

func fixtureGate(view edgecore.CanonicalView, req edgecore.Request) edgecore.Action {
	switch req.Path {
	case "/deny":
		return edgecore.ActionBlock
	case "/slow":
		time.Sleep(80 * time.Millisecond)
		return edgecore.ActionBlock
	case "/body-required":
		if edgecore.ShouldSkipBodyPolicy(view) {
			return edgecore.ActionAllow
		}
		if strings.Contains(string(req.Body), "deny") {
			return edgecore.ActionBlock
		}
	case "/required-headers":
		if !requiredAuthorizationInput(req) {
			return edgecore.ActionBlock
		}
	}
	return edgecore.ActionAllow
}

func requiredAuthorizationInput(req edgecore.Request) bool {
	if req.Method != http.MethodPost || req.Query != "first=1&first=2" || string(req.Body) != "payload" {
		return false
	}
	for _, name := range []string{"Host", "Content-Type", "X-Forwarded-For", "X-Forwarded-Proto", "X-Request-Id"} {
		if strings.TrimSpace(req.Headers[name]) == "" {
			return false
		}
	}
	return req.Headers["Authorization"] == "" && req.Headers["Cookie"] == ""
}

func applicationHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Yufeng-Upstream", "reached")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reached": true,
			"method":  r.Method,
			"path":    r.URL.Path,
		})
	})
}
