package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	"yufeng/proto/gen/authv1/authv1connect"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	"yufeng/proto/gen/governv1/governv1connect"
)

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

func runLogin(args []string) error {
	fs := newFlagSet("login")
	brain := fs.String("brain", "http://127.0.0.1:9050", "中台地址")
	username := fs.String("username", "", "用户名")
	password := fs.String("password", "", "密码")
	tlsCA, tlsCert, tlsKey := addTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		return errors.New("username and password are required")
	}
	hc, err := brainHTTP(*tlsCA, *tlsCert, *tlsKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := authv1connect.NewAuthServiceClient(hc, *brain)
	resp, err := client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: *username, Password: *password}))
	if err != nil {
		return err
	}
	fmt.Println(resp.Msg.Token)
	return nil
}

func runPublish(args []string) error {
	fs := newFlagSet("publish")
	brain := fs.String("brain", "http://127.0.0.1:9050", "中台地址")
	token := fs.String("token", "", "操作令牌；缺省读环境变量 YUFENG_TOKEN")
	assetID := fs.String("asset", "", "目标资产 id")
	payloadFile := fs.String("payload", "", "规则载荷 JSON 文件")
	canary := fs.Int("canary", 25, "canary 百分比 1-25")
	tlsCA, tlsCert, tlsKey := addTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		*token = os.Getenv("YUFENG_TOKEN")
	}
	if *token == "" || *assetID == "" || *payloadFile == "" {
		return errors.New("token, asset and payload are required")
	}
	if *canary < 1 || *canary > 25 {
		return fmt.Errorf("canary percent %d out of range [1,25]", *canary)
	}
	payload, err := publishPayload(*payloadFile)
	if err != nil {
		return err
	}
	hc, err := brainHTTP(*tlsCA, *tlsCert, *tlsKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := governv1connect.NewGovernServiceClient(hc, *brain)

	prop := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Kind:          artifactv1.Kind_KIND_RULE,
		Payload:       payload,
		PayloadSchema: "rules/v1",
		Scope:         &artifactv1.Scope{AssetIds: []string{*assetID}},
		Ttl:           durationpb.New(24 * time.Hour),
	})
	prop.Header().Set("Authorization", "Bearer "+*token)
	proposed, err := client.ProposeArtifact(ctx, prop)
	if err != nil {
		return err
	}
	releaseID := proposed.Msg.ReleaseId
	fmt.Printf("proposed release=%s\n", releaseID)

	gate := connect.NewRequest(&governv1.GateArtifactRequest{ReleaseId: releaseID})
	gate.Header().Set("Authorization", "Bearer "+*token)
	gated, err := client.GateArtifact(ctx, gate)
	if err != nil {
		return err
	}
	if gated.Msg.State != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		return fmt.Errorf("gate not passed: %v", gated.Msg.ReplayReport)
	}
	shadow := connect.NewRequest(&governv1.StartShadowRequest{ReleaseId: releaseID})
	shadow.Header().Set("Authorization", "Bearer "+*token)
	if _, err := client.StartShadow(ctx, shadow); err != nil {
		return err
	}
	canaryReq := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: releaseID, CanaryPercent: int32(*canary)})
	canaryReq.Header().Set("Authorization", "Bearer "+*token)
	if _, err := client.PromoteCanary(ctx, canaryReq); err != nil {
		return err
	}
	enforceReq := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: releaseID})
	enforceReq.Header().Set("Authorization", "Bearer "+*token)
	if _, err := client.PromoteEnforce(ctx, enforceReq); err != nil {
		return err
	}
	fmt.Printf("published release=%s state=ENFORCE asset=%s\n", releaseID, *assetID)
	return nil
}

func publishPayload(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("payload file is required")
	}
	return os.ReadFile(path)
}
