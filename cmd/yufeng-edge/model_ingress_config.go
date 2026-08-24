package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

type modelIngressEnvironmentDefaults struct {
	maxItems uint64
	maxBytes uint64
	maxAge   time.Duration
}

func modelIngressDefaultsFromEnvironment() (modelIngressEnvironmentDefaults, error) {
	items, err := modelIngressUnsignedEnvironment("YUFENG_MODEL_INGRESS_WINDOW_MAX_ITEMS", kernel.ModelIngressLocalMaxItems)
	if err != nil {
		return modelIngressEnvironmentDefaults{}, err
	}
	bytes, err := modelIngressUnsignedEnvironment("YUFENG_MODEL_INGRESS_WINDOW_MAX_BYTES", kernel.ModelIngressLocalMaxBytes)
	if err != nil {
		return modelIngressEnvironmentDefaults{}, err
	}
	age := kernel.ModelIngressLocalMaxAge
	if raw := strings.TrimSpace(os.Getenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE")); raw != "" {
		age, err = time.ParseDuration(raw)
		if err != nil {
			return modelIngressEnvironmentDefaults{}, fmt.Errorf("parse YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE: %w", err)
		}
	}
	return modelIngressEnvironmentDefaults{maxItems: items, maxBytes: bytes, maxAge: age}, nil
}

func modelIngressUnsignedEnvironment(name string, fallback uint64) (uint64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func modelIngressHardLimit(items, retainedBytes uint64, age time.Duration) (*artifactv1.ModelIngressWindow, error) {
	if items > uint64(^uint32(0)) {
		return nil, fmt.Errorf("model ingress max_items is too large")
	}
	return kernel.NormalizeModelIngressWindow(&artifactv1.ModelIngressWindow{
		MaxItems: uint32(items), MaxRetainedBytes: retainedBytes, MaxQueueAge: durationpb.New(age),
	})
}
