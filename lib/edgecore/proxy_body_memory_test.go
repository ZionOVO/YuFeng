package edgecore

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestReadInspectionBodyAllocatesProportionallyForSmallRequests(t *testing.T) {
	empty := &http.Request{Body: http.NoBody}
	body, oversize, total := readInspectionBody(empty)
	if body != nil || oversize || total != 0 {
		t.Fatalf("empty body=%v oversize=%v total=%d", body, oversize, total)
	}

	payload := bytes.Repeat([]byte("x"), 32)
	request := &http.Request{Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload))}
	body, oversize, total = readInspectionBody(request)
	if oversize || total != int64(len(payload)) || !bytes.Equal(body, payload) {
		t.Fatalf("small body len=%d cap=%d oversize=%v total=%d", len(body), cap(body), oversize, total)
	}
	if cap(body) >= EngineBodyAllocationRegressionLimit {
		t.Fatalf("small body retained a near-engine-limit allocation: len=%d cap=%d", len(body), cap(body))
	}
	replayed, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed, payload) {
		t.Fatal("inspection read must restore the request body for the upstream")
	}
}

// EngineBodyAllocationRegressionLimit 防止小正文重新退化为每请求预分配整个 64 KiB 检查缓冲。
const EngineBodyAllocationRegressionLimit = 8 << 10
