package tools

import (
	"testing"

	toolv1 "yufeng/proto/gen/toolv1"
)

func TestRegistryRejectsUnknownAndDuplicateImplementations(t *testing.T) {
	item := Implementation{Name: "event.get", Effect: toolv1.ToolEffect_TOOL_EFFECT_SAFE, Replay: toolv1.ToolReplay_TOOL_REPLAY_SAFE}
	registry, err := NewRegistry([]Implementation{item})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("event.get"); !ok {
		t.Fatal("registered implementation missing")
	}
	if _, ok := registry.Lookup("payload.supplied"); ok {
		t.Fatal("payload supplied implementation must not register itself")
	}
	if _, err := NewRegistry([]Implementation{item, item}); err == nil {
		t.Fatal("duplicate implementation must fail")
	}
}
