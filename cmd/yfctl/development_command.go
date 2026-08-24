//go:build yufeng_dev

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

func runDevelopmentCommand(name string, args []string) (bool, error) {
	if name != "demo" {
		return false, nil
	}
	return true, runDemo(args)
}

func developmentUsage() string { return "\n  yfctl demo [目录]      生成本地开发演示制品" }

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	dir := fs.String("out", ".demo", "输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		*dir = fs.Arg(0)
	}
	pubPath, keyPath := filepath.Join(*dir, "pubkey.hex"), filepath.Join(*dir, "dev.key.hex")
	if rawPub, err := os.ReadFile(pubPath); err == nil && len(bytes.TrimSpace(rawPub)) > 0 {
		if rawKey, err := os.ReadFile(keyPath); err == nil && len(bytes.TrimSpace(rawKey)) > 0 {
			return nil
		}
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*dir, "artifacts"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(private)+"\n"), 0o600); err != nil {
		return err
	}
	artifact, err := buildDemoArtifact()
	if err != nil {
		return err
	}
	if err := kernel.SignArtifact(artifact, private); err != nil {
		return err
	}
	raw, err := protojson.MarshalOptions{Multiline: true}.Marshal(artifact)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, "artifacts", "demo-rules.json"), append(raw, '\n'), 0o644)
}

var demoRules = []edgecore.Rule{
	{ID: "sql-union", Pattern: `(?i)union\s+select`},
	{ID: "path-traversal", Pattern: `\.\./etc/passwd`},
	{ID: "xss-script", Pattern: `(?i)<script`},
}

func buildDemoArtifact() (*artifactv1.Artifact, error) {
	payload, err := edgecore.MarshalRules(demoRules)
	if err != nil {
		return nil, err
	}
	return &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, Ttl: durationpb.New(24 * time.Hour),
		CreatedAt: timestamppb.Now(), CreatedBy: "yfctl demo", PayloadSchema: edgecore.RulePayloadSchema,
	}, nil
}
