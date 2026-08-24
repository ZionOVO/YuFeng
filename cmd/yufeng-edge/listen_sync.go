package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log"
	"time"

	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

func catchUpListenPlans(ctx context.Context, client *edgeclient.Client, sess *edgeclient.Session, set *edgecore.ReleaseSet, runtime *edgeRuntime, pub ed25519.PublicKey, cachePath string) error {
	current := set.CurrentListenPlan()
	var since uint64
	if current != nil {
		since = current.GetVersion()
	}
	plans, err := client.ListUnitListenPlans(ctx, sess, since)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if runtime != nil {
			if err := runtime.applyPlan(plan, pub, cachePath); err != nil {
				return err
			}
			continue
		}
		if err := set.ApplyListenPlan(plan, pub, sess.UnitID, func(previous, current *artifactv1.UnitListenPlan) error {
			return saveListenPlanCache(cachePath, previous, current)
		}); err != nil {
			return err
		}
	}
	return nil
}

func waitForListenPlan(ctx context.Context, client *edgeclient.Client, sess *edgeclient.Session, set *edgecore.ReleaseSet, pub ed25519.PublicKey, cachePath string) error {
	for set.CurrentListenPlan() == nil {
		if err := client.EnsureAccess(ctx, sess); err != nil {
			return err
		}
		if err := catchUpListenPlans(ctx, client, sess, set, nil, pub, cachePath); err != nil {
			return err
		}
		if set.CurrentListenPlan() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

func listenPlanLoop(ctx context.Context, client *edgeclient.Client, sess *edgeclient.Session, set *edgecore.ReleaseSet, runtime *edgeRuntime, pub ed25519.PublicKey, cachePath, sessionPath string, failures chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(releasePollInterval):
		}
		if err := client.EnsureAccess(ctx, sess); err != nil {
			continue
		}
		if err := saveSession(sessionPath, sess); err != nil {
			continue
		}
		if err := catchUpListenPlans(ctx, client, sess, set, runtime, pub, cachePath); err != nil {
			if errors.Is(err, errListenAddressChanged) {
				select {
				case failures <- err:
				case <-ctx.Done():
				}
				return
			}
			log.Printf("监听计划追赶失败: %v", err)
		}
	}
}
