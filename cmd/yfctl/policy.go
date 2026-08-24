package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/edgecore"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	"yufeng/proto/gen/authv1/authv1connect"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	"yufeng/proto/gen/governv1/governv1connect"
	grantv1 "yufeng/proto/gen/grantv1"
	"yufeng/proto/gen/grantv1/grantv1connect"
	userv1 "yufeng/proto/gen/userv1"
	"yufeng/proto/gen/userv1/userv1connect"
)

func runPolicyEnforce(args []string) error {
	fs := newFlagSet("policy-enforce")
	brain := fs.String("brain", "http://127.0.0.1:9050", "中台地址")
	username := fs.String("username", "", "具备 grant.write 的管理员")
	password := fs.String("password", "", "管理员密码")
	assetID := fs.String("asset", "", "目标资产 id")
	tlsCA, tlsCert, tlsKey := addTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" || *assetID == "" {
		return errors.New("username, password and asset are required")
	}
	hc, err := brainHTTP(*tlsCA, *tlsCert, *tlsKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	adminTok, err := loginToken(ctx, hc, *brain, *username, *password)
	if err != nil {
		return err
	}
	users := userv1connect.NewUserServiceClient(hc, *brain)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	opName := "yf-op-" + suffix
	offName := "yf-off-" + suffix
	opID, err := createUser(ctx, users, adminTok, opName, "Operator123", commonv1.UserRole_USER_ROLE_OPERATOR)
	if err != nil {
		return fmt.Errorf("create operator: %w", err)
	}
	offID, err := createUser(ctx, users, adminTok, offName, "Officer123", commonv1.UserRole_USER_ROLE_ADMIN)
	if err != nil {
		return fmt.Errorf("create officer: %w", err)
	}
	grants := grantv1connect.NewGrantServiceClient(hc, *brain)
	if err := putGrant(ctx, grants, adminTok, opID, *assetID, []string{"govern.propose", "govern.gate", "govern.start_shadow"}); err != nil {
		return fmt.Errorf("grant operator: %w", err)
	}
	if err := putGrant(ctx, grants, adminTok, offID, *assetID, []string{"govern.promote_canary", "govern.promote_enforce", "govern.retire"}); err != nil {
		return fmt.Errorf("grant officer: %w", err)
	}
	opTok, err := loginToken(ctx, hc, *brain, opName, "Operator123")
	if err != nil {
		return err
	}
	offTok, err := loginToken(ctx, hc, *brain, offName, "Officer123")
	if err != nil {
		return err
	}
	keys, err := corpusAttackKeys()
	if err != nil {
		return err
	}
	gov := governv1connect.NewGovernServiceClient(hc, *brain)
	prop := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Intent: &governv1.ProposalIntent{
			Kind:          commonv1.ProposalKind_PROPOSAL_KIND_POLICY,
			DetectionKeys: keys,
		},
		Scope: &artifactv1.Scope{AssetIds: []string{*assetID}},
		Ttl:   durationpb.New(time.Hour),
	})
	prop.Header().Set("Authorization", "Bearer "+opTok)
	proposed, err := gov.ProposeArtifact(ctx, prop)
	if err != nil {
		return fmt.Errorf("propose: %w", err)
	}
	relID := proposed.Msg.ReleaseId
	gate := connect.NewRequest(&governv1.GateArtifactRequest{ReleaseId: relID})
	gate.Header().Set("Authorization", "Bearer "+opTok)
	gated, err := gov.GateArtifact(ctx, gate)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	if gated.Msg.State != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		return fmt.Errorf("gate not passed: %v", gated.Msg.ReplayReport)
	}
	shadow := connect.NewRequest(&governv1.StartShadowRequest{ReleaseId: relID})
	shadow.Header().Set("Authorization", "Bearer "+opTok)
	if _, err := gov.StartShadow(ctx, shadow); err != nil {
		return fmt.Errorf("shadow: %w", err)
	}
	canary := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: relID, CanaryPercent: 25})
	canary.Header().Set("Authorization", "Bearer "+offTok)
	if _, err := gov.PromoteCanary(ctx, canary); err != nil {
		return fmt.Errorf("canary: %w", err)
	}
	enf := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: relID})
	enf.Header().Set("Authorization", "Bearer "+offTok)
	if _, err := gov.PromoteEnforce(ctx, enf); err != nil {
		return fmt.Errorf("enforce: %w", err)
	}
	fmt.Printf("release=%s state=ENFORCE asset=%s\n", relID, *assetID)
	return nil
}

func runRetire(args []string) error {
	fs := newFlagSet("retire")
	brain := fs.String("brain", "http://127.0.0.1:9050", "中台地址")
	username := fs.String("username", "", "具备 grant.write 的管理员")
	password := fs.String("password", "", "管理员密码")
	assetID := fs.String("asset", "", "目标资产 id")
	releaseID := fs.String("release", "", "要退休的 release")
	tlsCA, tlsCert, tlsKey := addTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" || *assetID == "" || *releaseID == "" {
		return errors.New("username, password, asset and release are required")
	}
	hc, err := brainHTTP(*tlsCA, *tlsCert, *tlsKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminTok, err := loginToken(ctx, hc, *brain, *username, *password)
	if err != nil {
		return err
	}
	users := userv1connect.NewUserServiceClient(hc, *brain)
	offName := "yf-ret-" + fmt.Sprintf("%d", time.Now().UnixNano())
	offID, err := createUser(ctx, users, adminTok, offName, "Officer123", commonv1.UserRole_USER_ROLE_ADMIN)
	if err != nil {
		return fmt.Errorf("create officer: %w", err)
	}
	grants := grantv1connect.NewGrantServiceClient(hc, *brain)
	if err := putGrant(ctx, grants, adminTok, offID, *assetID, []string{"govern.retire"}); err != nil {
		return fmt.Errorf("grant officer: %w", err)
	}
	offTok, err := loginToken(ctx, hc, *brain, offName, "Officer123")
	if err != nil {
		return err
	}
	gov := governv1connect.NewGovernServiceClient(hc, *brain)
	ret := connect.NewRequest(&governv1.RetireReleaseRequest{ReleaseId: *releaseID, Reason: "manual"})
	ret.Header().Set("Authorization", "Bearer "+offTok)
	if _, err := gov.RetireRelease(ctx, ret); err != nil {
		return fmt.Errorf("retire: %w", err)
	}
	fmt.Printf("release=%s state=RETIRED\n", *releaseID)
	return nil
}

func loginToken(ctx context.Context, hc *http.Client, brain, username, password string) (string, error) {
	client := authv1connect.NewAuthServiceClient(hc, brain)
	resp, err := client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: password}))
	if err != nil {
		return "", err
	}
	return resp.Msg.Token, nil
}

func createUser(ctx context.Context, users userv1connect.UserServiceClient, adminTok, name, pass string, role commonv1.UserRole) (string, error) {
	req := connect.NewRequest(&userv1.CreateUserRequest{Username: name, Password: pass, Role: role})
	req.Header().Set("Authorization", "Bearer "+adminTok)
	resp, err := users.CreateUser(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Msg.User.UserId, nil
}

func putGrant(ctx context.Context, grants grantv1connect.GrantServiceClient, adminTok, subject, assetID string, tools []string) error {
	req := connect.NewRequest(&grantv1.PutGrantRequest{
		SubjectUserId: subject,
		Tools:         tools,
		Bindings:      []*grantv1.BindingRef{{Kind: "asset", Id: assetID}},
	})
	req.Header().Set("Authorization", "Bearer "+adminTok)
	_, err := grants.PutGrant(ctx, req)
	return err
}

func corpusAttackKeys() ([]*commonv1.DetectionKey, error) {
	crs, err := edgecore.SharedCoraza()
	if err != nil {
		return nil, err
	}
	var keys []*commonv1.DetectionKey
	seen := map[string]bool{}
	for _, req := range []edgecore.Request{
		{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"},
		{Method: "POST", Path: "/api/items", Query: "q=<script>alert(1)</script>"},
		{Method: "GET", Path: "/download/../../etc/passwd"},
	} {
		dets, err := crs.Detect(req)
		if err != nil {
			return nil, err
		}
		got := false
		for _, d := range dets {
			if !edgecore.CRSAutoGovernRule(d.RuleID) || seen[d.RuleID] {
				continue
			}
			seen[d.RuleID] = true
			got = true
			keys = append(keys, &commonv1.DetectionKey{DetectorId: "crs", RuleId: d.RuleID, TargetLocation: d.Location})
		}
		if !got {
			return nil, fmt.Errorf("need attack-class crs key for %s %s", req.Method, req.Path)
		}
	}
	return keys, nil
}
