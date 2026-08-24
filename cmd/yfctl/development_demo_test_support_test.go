//go:build !yufeng_dev

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

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo-test", flag.ContinueOnError)
	dir := fs.String("out", ".demo", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pubPath, keyPath := filepath.Join(*dir, "pubkey.hex"), filepath.Join(*dir, "dev.key.hex")
	if public, err := os.ReadFile(pubPath); err == nil && len(bytes.TrimSpace(public)) > 0 {
		if private, err := os.ReadFile(keyPath); err == nil && len(bytes.TrimSpace(private)) > 0 {
			return nil
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*dir, "artifacts"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(public)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(private)), 0o600); err != nil {
		return err
	}
	payload, err := edgecore.MarshalRules(demoRules)
	if err != nil {
		return err
	}
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
		Ttl: durationpb.New(24 * time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test"}
	if err := kernel.SignArtifact(artifact, private); err != nil {
		return err
	}
	raw, err := protojson.Marshal(artifact)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, "artifacts", "demo-rules.json"), raw, 0o644)
}

var demoRules = []edgecore.Rule{{ID: "sql", Pattern: `union\s+select`}, {ID: "path", Pattern: `\.\./etc/passwd`}, {ID: "xss", Pattern: `<script`}}
