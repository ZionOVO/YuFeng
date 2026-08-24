package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	evidencev1 "yufeng/proto/gen/evidencev1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func openTrafficReview(dataDir, spoolDir, unitID string) (*edgecore.EvidenceVault, *edgeclient.ReviewSpool, error) {
	spool, err := edgeclient.NewReviewSpool(filepath.Join(spoolDir, "traffic-review-"+unitID))
	if err != nil {
		return nil, nil, err
	}
	key, err := loadOrCreateEvidenceKey(filepath.Join(dataDir, "evidence-vault.key"))
	if err != nil {
		log.Printf("流量证据功能失败关闭，继续保留统计窗: %v", err)
		return nil, spool, nil
	}
	vault, err := edgecore.NewEvidenceVault(filepath.Join(dataDir, "evidence-vault"), key)
	if err != nil {
		log.Printf("流量证据功能失败关闭，继续保留统计窗: %v", err)
		return nil, spool, nil
	}
	return vault, spool, nil
}

func loadOrCreateEvidenceKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) != 32 {
			return nil, errors.New("evidence vault key must be exactly 32 bytes")
		}
		if info, statErr := os.Stat(path); statErr != nil {
			return nil, statErr
		} else if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("evidence vault key permissions are too broad")
		}
		return raw, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw = make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := atomicWritePrivate(path, ".evidence-key-*", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func reviewDrainLoop(ctx context.Context, runtime *edgeRuntime) {
	for {
		select {
		case <-ctx.Done():
			runtime.drainTrafficReview(time.Now().Add(5 * time.Minute))
			return
		case <-time.After(time.Second):
			runtime.drainTrafficReview(time.Now())
		}
	}
}

func reviewUploadLoop(ctx context.Context, client *edgeclient.Client, session *edgeclient.Session, spool *edgeclient.ReviewSpool) {
	reviewUploadLoopWithInterval(ctx, client, session, spool, uploadScanInterval)
}

func reviewUploadLoopWithInterval(ctx context.Context, client *edgeclient.Client, session *edgeclient.Session, spool *edgeclient.ReviewSpool, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := uploadReviewWindows(ctx, client, session, spool); err != nil {
			log.Printf("流量统计窗上传失败: %v", err)
			continue
		}
		if err := uploadReviewCandidates(ctx, client, session, spool); err != nil {
			log.Printf("流量审查候选上传失败: %v", err)
		}
	}
}

func evidenceRequestLoop(ctx context.Context, client *edgeclient.Client, session *edgeclient.Session, vault *edgecore.EvidenceVault) {
	for {
		resp, err := client.PollEvidenceRequests(ctx, session, 20)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("证据请求轮询失败: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, request := range resp.GetRequests() {
			bundle, err := buildEvidenceBundle(vault, request, time.Now())
			if err != nil {
				log.Printf("证据请求 %s 失败关闭: %v", request.GetRequestId(), err)
				continue
			}
			if _, err := client.SubmitEvidenceBundle(ctx, session, bundle); err != nil {
				log.Printf("证据请求 %s 提交失败: %v", request.GetRequestId(), err)
			}
		}
	}
}

func evidenceVaultCleanupLoop(ctx context.Context, vault *edgecore.EvidenceVault) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := vault.GC(now); err != nil {
				log.Printf("流量证据过期清理失败: %v", err)
			}
		}
	}
}

type evidenceDocument struct {
	Method        string          `json:"method"`
	RouteTemplate string          `json:"route_template"`
	ContentType   string          `json:"content_type"`
	ContentLength int             `json:"content_length"`
	Fields        []evidenceField `json:"fields"`
}

type evidenceField struct {
	Selector string `json:"selector"`
	Surface  string `json:"surface"`
	Length   int    `json:"length"`
	Charset  string `json:"charset"`
	Digest   string `json:"digest"`
	Value    string `json:"value,omitempty"`
}

func buildEvidenceBundle(vault *edgecore.EvidenceVault, request *evidencev1.EvidenceRequest, now time.Time) (*evidencev1.SubmitEvidenceBundleRequest, error) {
	if request == nil || request.GetExpiresAt() == nil || !request.GetExpiresAt().IsValid() || !request.GetExpiresAt().AsTime().After(now) {
		return nil, errors.New("evidence request is expired")
	}
	if request.GetLeaseDeadline() == nil || !request.GetLeaseDeadline().IsValid() || !request.GetLeaseDeadline().AsTime().After(now) {
		return nil, errors.New("evidence request lease is expired")
	}
	if vault == nil {
		return nil, errors.New("evidence vault is unavailable")
	}
	seenHandles := make(map[string]bool, len(request.GetEvidenceHandles()))
	for _, handle := range request.GetEvidenceHandles() {
		if strings.TrimSpace(handle) == "" || seenHandles[handle] {
			return nil, errors.New("approved evidence handle identity is invalid")
		}
		seenHandles[handle] = true
	}
	allowed := map[string]bool{}
	for _, field := range request.GetAllowedFields() {
		if !supportedEvidenceBundleField(field) || allowed[field] {
			return nil, errors.New("approved evidence field is invalid")
		}
		allowed[field] = true
	}
	bundle := &evidencev1.SubmitEvidenceBundleRequest{
		RequestId: request.GetRequestId(), ApprovalId: request.GetApprovalId(), CaseId: request.GetCaseId(),
		LeaseId: request.GetLeaseId(), LeaseEpoch: request.GetLeaseEpoch(),
	}
	modelInputBytes := request.GetMaxBytes()
	if modelInputBytes > kernel.TrafficReviewModelEvidenceBytes {
		modelInputBytes = kernel.TrafficReviewModelEvidenceBytes
	}
	if modelInputBytes < int64(len(request.GetEvidenceHandles())) {
		return nil, errors.New("evidence byte budget cannot cover every approved handle")
	}
	var total int64
	for handleIndex, handle := range request.GetEvidenceHandles() {
		raw, _, ok, err := vault.Get(handle, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("approved evidence handle is unavailable")
		}
		var document evidenceDocument
		if err := decodeEvidenceDocument(raw, &document); err != nil {
			return nil, err
		}
		if err := validateEvidenceDocument(document); err != nil {
			return nil, err
		}
		fields := []struct {
			name    string
			content []byte
		}{{"method", []byte(document.Method)}, {"path", []byte(document.RouteTemplate)}}
		var queryFields, bodyFields []evidenceField
		for _, projected := range document.Fields {
			switch projected.Surface {
			case "query":
				queryFields = append(queryFields, projected)
			case "body":
				bodyFields = append(bodyFields, projected)
			}
		}
		if len(queryFields) > 0 {
			content, err := json.Marshal(queryFields)
			if err != nil {
				return nil, err
			}
			fields = append(fields, struct {
				name    string
				content []byte
			}{name: "query", content: content})
		}
		if document.ContentType != "" || document.ContentLength > 0 || len(bodyFields) > 0 {
			content, err := json.Marshal(struct {
				ContentType   string          `json:"content_type,omitempty"`
				ContentLength int             `json:"content_length"`
				Fields        []evidenceField `json:"fields,omitempty"`
			}{ContentType: document.ContentType, ContentLength: document.ContentLength, Fields: bodyFields})
			if err != nil {
				return nil, err
			}
			fields = append(fields, struct {
				name    string
				content []byte
			}{name: "body", content: content})
		}
		remainingHandles := int64(len(request.GetEvidenceHandles()) - handleIndex)
		handleBudget := (modelInputBytes - total) / remainingHandles
		var handleBytes int64
		for _, field := range fields {
			if !allowed[field.name] || len(field.content) == 0 || handleBytes >= handleBudget {
				continue
			}
			content := field.content
			if remaining := handleBudget - handleBytes; int64(len(content)) > remaining {
				var err error
				switch field.name {
				case "query":
					content, err = fitStructuredEvidence(queryFields, int(remaining), false, func(fields []evidenceField) ([]byte, error) {
						return json.Marshal(fields)
					})
				case "body":
					content, err = fitStructuredEvidence(bodyFields, int(remaining), true, func(fields []evidenceField) ([]byte, error) {
						return json.Marshal(struct {
							ContentType   string          `json:"content_type,omitempty"`
							ContentLength int             `json:"content_length"`
							Fields        []evidenceField `json:"fields,omitempty"`
						}{ContentType: document.ContentType, ContentLength: document.ContentLength, Fields: fields})
					})
				default:
					content = nil
				}
				if err != nil {
					return nil, err
				}
				if len(content) == 0 {
					continue
				}
			}
			sum := sha256.Sum256(content)
			bundle.Fragments = append(bundle.Fragments, &evidencev1.EvidenceFragment{EvidenceHandle: handle, Field: field.name,
				Content: append([]byte(nil), content...), ContentDigest: "sha256:" + hex.EncodeToString(sum[:])})
			handleBytes += int64(len(content))
			total += int64(len(content))
		}
		if handleBytes == 0 {
			return nil, errors.New("approved evidence handle contains no available allowed field")
		}
	}
	if len(bundle.GetFragments()) == 0 {
		return nil, errors.New("approved evidence is unavailable or contains no allowed fields")
	}
	bundle.BundleDigest = evidenceBundleDigest(bundle.GetFragments())
	return bundle, nil
}

func supportedEvidenceBundleField(field string) bool {
	switch field {
	case "method", "path", "query", "body":
		return true
	default:
		return false
	}
}

func decodeEvidenceDocument(raw []byte, document *evidenceDocument) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(document); err != nil {
		return fmt.Errorf("decode approved evidence: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode approved evidence trailing data: %w", err)
	}
	return nil
}

func validateEvidenceDocument(document evidenceDocument) error {
	if strings.TrimSpace(document.Method) == "" || strings.TrimSpace(document.RouteTemplate) == "" || document.ContentLength < 0 {
		return errors.New("approved evidence document is incomplete")
	}
	seen := make(map[string]bool, len(document.Fields))
	for _, field := range document.Fields {
		selector := strings.ToLower(strings.TrimSpace(field.Selector))
		kind, name, ok := strings.Cut(selector, ".")
		if !ok || name == "" || field.Length < 0 || seen[selector] {
			return errors.New("approved evidence document field is invalid")
		}
		seen[selector] = true
		switch kind {
		case "query":
			if field.Surface != "query" {
				return errors.New("approved evidence document surface is invalid")
			}
		case "json", "body", "arg":
			if field.Surface != "body" {
				return errors.New("approved evidence document surface is invalid")
			}
		default:
			return errors.New("approved evidence document field is unsupported")
		}
	}
	return nil
}

func fitStructuredEvidence(fields []evidenceField, limit int, allowEmpty bool, marshal func([]evidenceField) ([]byte, error)) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	bounded := append([]evidenceField(nil), fields...)
	fit := func() ([]byte, bool, error) {
		if !allowEmpty && len(bounded) == 0 {
			return nil, false, nil
		}
		raw, err := marshal(bounded)
		return raw, err == nil && len(raw) <= limit, err
	}
	if raw, ok, err := fit(); err != nil || ok {
		return raw, err
	}
	for index := len(bounded) - 1; index >= 0; index-- {
		if bounded[index].Value == "" {
			continue
		}
		bounded[index].Value = ""
		if raw, ok, err := fit(); err != nil || ok {
			return raw, err
		}
	}
	for len(bounded) > 0 {
		bounded = bounded[:len(bounded)-1]
		if raw, ok, err := fit(); err != nil || ok {
			return raw, err
		}
	}
	return nil, nil
}

func evidenceBundleDigest(fragments []*evidencev1.EvidenceFragment) string {
	hash := sha256.New()
	for _, fragment := range fragments {
		if fragment == nil {
			continue
		}
		hash.Write([]byte(fragment.GetEvidenceHandle()))
		hash.Write([]byte{0})
		hash.Write([]byte(fragment.GetField()))
		hash.Write([]byte{0})
		hash.Write([]byte(fragment.GetContentDigest()))
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func uploadReviewWindows(ctx context.Context, client *edgeclient.Client, session *edgeclient.Session, spool *edgeclient.ReviewSpool) error {
	files, err := spool.WindowFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		values, err := spool.ReadWindows(file)
		if err != nil {
			return quarantineCorruptReview(spool, file, err)
		}
		if len(values) == 0 {
			if err := spool.Remove(file); err != nil {
				return err
			}
			continue
		}
		resp, err := client.UploadTrafficWindows(ctx, session, values)
		if err != nil {
			return err
		}
		if int(resp.GetAccepted()+resp.GetDeduped())+len(resp.GetRejected()) != len(values) {
			return fmt.Errorf("traffic window acknowledgement count mismatch")
		}
		retryable, permanent, err := splitRejectedWindows(values, resp.GetRejected())
		if err != nil {
			return err
		}
		if err := spool.QuarantineWindows(permanent); err != nil {
			return err
		}
		if err := spool.ReplaceWindows(file, retryable); err != nil {
			return err
		}
		if len(retryable) > 0 {
			return fmt.Errorf("server temporarily rejected %d traffic windows", len(retryable))
		}
	}
	return nil
}

func uploadReviewCandidates(ctx context.Context, client *edgeclient.Client, session *edgeclient.Session, spool *edgeclient.ReviewSpool) error {
	files, err := spool.CandidateFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		values, err := spool.ReadCandidates(file)
		if err != nil {
			return quarantineCorruptReview(spool, file, err)
		}
		if len(values) == 0 {
			if err := spool.Remove(file); err != nil {
				return err
			}
			continue
		}
		resp, err := client.UploadReviewCandidates(ctx, session, values)
		if err != nil {
			return err
		}
		if int(resp.GetAccepted()+resp.GetDeduped())+len(resp.GetRejected()) != len(values) {
			return fmt.Errorf("review candidate acknowledgement count mismatch")
		}
		retryable, permanent, err := splitRejectedCandidates(values, resp.GetRejected())
		if err != nil {
			return err
		}
		if err := spool.QuarantineCandidates(permanent); err != nil {
			return err
		}
		if err := spool.ReplaceCandidates(file, retryable); err != nil {
			return err
		}
		if len(retryable) > 0 {
			return fmt.Errorf("server temporarily rejected %d review candidates", len(retryable))
		}
	}
	return nil
}

func quarantineCorruptReview(spool *edgeclient.ReviewSpool, path string, cause error) error {
	if err := spool.QuarantineCorrupt(path); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func splitRejectedWindows(values []*telemetryv1.TrafficWindow, rejected []*telemetryv1.RejectedEvent) ([]*telemetryv1.TrafficWindow, []*telemetryv1.TrafficWindow, error) {
	seenValues := make(map[string]bool, len(values))
	for _, value := range values {
		if value == nil || strings.TrimSpace(value.GetWindowId()) == "" || seenValues[value.GetWindowId()] {
			return nil, nil, errors.New("traffic window source identity is invalid")
		}
		seenValues[value.GetWindowId()] = true
	}
	byID := make(map[string]*telemetryv1.RejectedEvent, len(rejected))
	for _, item := range rejected {
		if item == nil || item.GetEventId() == "" || byID[item.GetEventId()] != nil {
			return nil, nil, errors.New("traffic window rejection identity is invalid")
		}
		byID[item.GetEventId()] = item
	}
	var retryable, permanent []*telemetryv1.TrafficWindow
	for _, value := range values {
		item := byID[value.GetWindowId()]
		if item == nil {
			continue
		}
		delete(byID, value.GetWindowId())
		if item.GetRetryable() {
			retryable = append(retryable, value)
		} else {
			permanent = append(permanent, value)
		}
	}
	if len(byID) != 0 {
		return nil, nil, errors.New("traffic window rejection references an unknown item")
	}
	return retryable, permanent, nil
}

func splitRejectedCandidates(values []*telemetryv1.ReviewCandidate, rejected []*telemetryv1.RejectedEvent) ([]*telemetryv1.ReviewCandidate, []*telemetryv1.ReviewCandidate, error) {
	seenValues := make(map[string]bool, len(values))
	for _, value := range values {
		if value == nil || strings.TrimSpace(value.GetCandidateId()) == "" || seenValues[value.GetCandidateId()] {
			return nil, nil, errors.New("review candidate source identity is invalid")
		}
		seenValues[value.GetCandidateId()] = true
	}
	byID := make(map[string]*telemetryv1.RejectedEvent, len(rejected))
	for _, item := range rejected {
		if item == nil || item.GetEventId() == "" || byID[item.GetEventId()] != nil {
			return nil, nil, errors.New("review candidate rejection identity is invalid")
		}
		byID[item.GetEventId()] = item
	}
	var retryable, permanent []*telemetryv1.ReviewCandidate
	for _, value := range values {
		item := byID[value.GetCandidateId()]
		if item == nil {
			continue
		}
		delete(byID, value.GetCandidateId())
		if item.GetRetryable() {
			retryable = append(retryable, value)
		} else {
			permanent = append(permanent, value)
		}
	}
	if len(byID) != 0 {
		return nil, nil, errors.New("review candidate rejection references an unknown item")
	}
	return retryable, permanent, nil
}
