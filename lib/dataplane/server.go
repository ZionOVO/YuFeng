package dataplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"yufeng/lib/kernel"
)

const (
	probeHTTPTimeout  = 2 * time.Second
	defaultProbeURL   = "http://127.0.0.1:19092/ready"
	defaultListenHost = "127.0.0.1"
)

// BinaryName 是由技术人员显式启动的可选本机 Edge 健康监督器进程名。
const BinaryName = "yufeng-dataplane"

// EdgeState 是 Edge 独立管理口返回的活动状态。
type EdgeState = kernel.EdgeReadyState

// Supervisor 只观察一个已由技术人员安装和启动的本机 Edge。
// 它不持有 Docker、安装、创建、重建、升级或卸载权限。
type Supervisor struct {
	Addr     string
	ProbeURL string
	Client   *http.Client
}

// Handler 返回监督器存活探针和只读 Edge 就绪投影。
func (s *Supervisor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/ready", s.handleReady)
	return mux
}

// ListenAddr 返回只绑定回环地址的默认管理口。
func (s *Supervisor) ListenAddr() string {
	if strings.TrimSpace(s.Addr) != "" {
		return s.Addr
	}
	return fmt.Sprintf("%s:%d", defaultListenHost, kernel.DataplaneControlPort)
}

func (s *Supervisor) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, state, err := probeEdgeState(r.Context(), s.httpClient(), s.probeURL())
	if err != nil || status != http.StatusOK || !state.Ready {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func (s *Supervisor) probeURL() string {
	if strings.TrimSpace(s.ProbeURL) != "" {
		return s.ProbeURL
	}
	return defaultProbeURL
}

func (s *Supervisor) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: probeHTTPTimeout}
}

// ProbeEdge 读取 Edge 独立管理口；它不会改变 Edge 或容器状态。
func ProbeEdge(ctx context.Context, client *http.Client, probeURL string) (int, error) {
	status, _, err := probeEdgeState(ctx, client, probeURL)
	return status, err
}

func probeEdgeState(ctx context.Context, client *http.Client, probeURL string) (int, EdgeState, error) {
	if client == nil {
		client = &http.Client{Timeout: probeHTTPTimeout}
	}
	if strings.TrimSpace(probeURL) == "" {
		probeURL = defaultProbeURL
	}
	pctx, cancel := context.WithTimeout(ctx, probeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return 0, EdgeState{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, EdgeState{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读探针返回后没有可恢复动作。
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, kernel.ControlPlaneBodyLimit))
		return resp.StatusCode, EdgeState{}, nil
	}
	var state EdgeState
	dec := json.NewDecoder(io.LimitReader(resp.Body, kernel.ControlPlaneBodyLimit+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return resp.StatusCode, EdgeState{}, fmt.Errorf("decode edge ready state: %w", err)
	}
	return resp.StatusCode, state, nil
}
