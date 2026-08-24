//go:build yufeng_dev

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func launchEdgeDevelopmentMode(flags edgeDevelopmentFlags, adminAddr string, pub ed25519.PublicKey) {
	if err := runEdgeDevelopmentMode(flags, adminAddr, pub); err != nil {
		log.Fatal(err)
	}
}

func runEdgeDevelopmentMode(flags edgeDevelopmentFlags, adminAddr string, pub ed25519.PublicKey) error {
	if strings.TrimSpace(*flags.upstream) == "" || strings.TrimSpace(*flags.artifacts) == "" || strings.TrimSpace(*flags.telemetry) == "" || strings.TrimSpace(*flags.assetID) == "" {
		return errors.New("upstream, artifacts, telemetry and asset are required")
	}
	mode, err := parseMode(*flags.mode)
	if err != nil {
		return err
	}
	detectors, err := loadDetectors(*flags.artifacts, pub, *flags.assetID)
	if err != nil {
		return err
	}
	if len(detectors) == 0 {
		return errors.New("no usable signed rule artifact")
	}
	upstream, cleanup, err := resolveUpstream(*flags.upstream)
	if err != nil {
		return err
	}
	defer cleanup()
	telFile, err := os.OpenFile(*flags.telemetry, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer telFile.Close() //nolint:errcheck // 开发进程退出时尽力关闭测试遥测文件。

	proxy := edgecore.NewProxy(edgecore.NewEngine(detectors...), mode, edgecore.NewTelemetry(telFile), upstream, *flags.assetID)
	proxy.SetPosture(parsePosture(*flags.posture))
	server := &http.Server{Addr: *flags.addr, Handler: proxy, ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout,
		ReadTimeout: kernel.HTTPReadTimeout, WriteTimeout: kernel.HTTPWriteTimeout, IdleTimeout: kernel.HTTPIdleTimeout,
		MaxHeaderBytes: kernel.HTTPMaxHeaderBytes}
	admin := &http.Server{Addr: adminAddr, Handler: observability.Handler(nil, "edge", "v1"), ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout}
	errCh := make(chan error, 2)
	go func() { errCh <- server.ListenAndServe() }()
	go func() { errCh <- admin.ListenAndServe() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return admin.Shutdown(shutdownCtx)
	}
	return nil
}

func loadDetectors(dir string, pub ed25519.PublicKey, assetID string) ([]edgecore.Detector, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var detectors []edgecore.Detector
	for _, path := range entries {
		artifact, err := edgecore.LoadArtifact(path, pub)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if artifact.Kind != artifactv1.Kind_KIND_RULE || artifact.PayloadSchema != edgecore.RulePayloadSchema {
			return nil, fmt.Errorf("%s: unsupported rule artifact", path)
		}
		if artifact.Ttl == nil || artifact.Ttl.AsDuration() <= 0 || artifact.CreatedAt == nil {
			return nil, fmt.Errorf("%s: invalid artifact lifetime", path)
		}
		if artifact.CreatedAt.AsTime().Add(artifact.Ttl.AsDuration()).Before(now) {
			continue
		}
		if !edgecore.ScopeCoversAsset(artifact.Scope, assetID) {
			continue
		}
		rules, err := edgecore.ParseRules(artifact.Payload)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		detector, err := edgecore.NewRuleDetector(artifact.Id, rules)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		detectors = append(detectors, edgecore.NewScoped(detector, edgecore.ScopePrefix(artifact.Scope)))
	}
	return detectors, nil
}

func resolveUpstream(spec string) (*url.URL, func(), error) {
	if spec == "builtin" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := fmt.Fprintf(w, "upstream ok: %s %s\n", r.Method, r.URL.Path); err != nil {
				log.Printf("development upstream response: %v", err)
			}
		}), ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout}
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("development upstream: %v", err)
			}
		}()
		return &url.URL{Scheme: "http", Host: listener.Addr().String()}, func() {
			ctx, cancel := context.WithTimeout(context.Background(), kernel.HTTPWriteTimeout)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				log.Printf("development upstream shutdown: %v", err)
			}
		}, nil
	}
	parsed, err := url.Parse(spec)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, nil, fmt.Errorf("upstream %q must use http or https", spec)
	}
	return parsed, func() {}, nil
}

func parsePosture(value string) commonv1.IngressPosture {
	switch strings.TrimSpace(value) {
	case "ext_authz", "ext-authz":
		return commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ
	case "tap", "tap_alert":
		return commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT
	case "mirror", "span":
		return commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE
	default:
		return commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY
	}
}

func parseMode(value string) (commonv1.ReleaseMode, error) {
	switch value {
	case "shadow":
		return commonv1.ReleaseMode_RELEASE_MODE_SHADOW, nil
	case "enforce":
		return commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil
	default:
		return 0, fmt.Errorf("unknown release mode %q", value)
	}
}
