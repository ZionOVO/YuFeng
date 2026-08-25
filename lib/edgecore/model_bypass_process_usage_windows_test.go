package edgecore

import "testing"

func modelBypassReadProcessUsage(t *testing.T) modelBypassProcessUsage {
	t.Helper()
	t.Skip("model bypass performance qualification requires Unix process usage counters")
	return modelBypassProcessUsage{}
}
