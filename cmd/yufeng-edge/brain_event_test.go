package main

import (
	"testing"

	"yufeng/lib/edgecore"

	eventv1 "yufeng/proto/gen/eventv1"
)

func TestObservationEventIncludesAllow(t *testing.T) {
	ev := observationEvent("u1", "a1", "rid", edgecore.Request{Method: "GET", Path: "/api/items", Query: "id=1"}, edgecore.Decision{Action: edgecore.ActionAllow}, edgecore.SourcePseudonymizer{})
	if ev.Verdict != eventv1.Verdict_VERDICT_ALLOW {
		t.Fatalf("漏拦必须上报 allow，实际 %s", ev.Verdict)
	}
	if ev.GetHttp() == nil || ev.GetHttp().Path != "/api/items" {
		t.Fatalf("事件缺少 HTTP 容器: %+v", ev)
	}
}

func TestObservationEventCopiesCRSKey(t *testing.T) {
	ev := observationEvent("u1", "a1", "rid", edgecore.Request{Method: "GET", Path: "/api/items"}, edgecore.Decision{
		Action: edgecore.ActionAllow,
		Detections: []edgecore.Detection{{
			RuleID: "942100", Class: 1,
		}},
	}, edgecore.SourcePseudonymizer{})
	if len(ev.Detections) != 1 || ev.Detections[0].GetKey() == nil || ev.Detections[0].GetKey().RuleId != "942100" {
		t.Fatalf("生产事件必须带检测键: %+v", ev.Detections)
	}
}

func TestObservationEventKeepsReleaseTraces(t *testing.T) {
	ev := observationEvent("u1", "a1", "rid", edgecore.Request{Method: "GET", Path: "/x"}, edgecore.Decision{
		Action:       edgecore.ActionBlock,
		Observations: []edgecore.ReleaseObservation{{ReleaseID: "rel-1", Matched: true}},
	}, edgecore.SourcePseudonymizer{})
	if ev.Verdict != eventv1.Verdict_VERDICT_BLOCK || len(ev.ReleaseTraces) != 1 || ev.ReleaseTraces[0].ReleaseId != "rel-1" {
		t.Fatalf("拦截事件应带 release 轨迹: %+v", ev)
	}
}
