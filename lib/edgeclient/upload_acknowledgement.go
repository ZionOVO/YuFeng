package edgeclient

import (
	"strings"

	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

// ApplyUploadAck 按事件级确认处置分段：accepted/deduped 删除，永久非法隔离，暂时错误保留重试。
func ApplyUploadAck(s *Spool, path string, events []*eventv1.Event, resp *telemetryv1.UploadEventsResponse, uploadErr error) error {
	if uploadErr != nil {
		if isPermanentUploadErr(uploadErr) {
			return s.Quarantine(path)
		}
		return uploadErr
	}
	if resp == nil {
		return s.Remove(path)
	}
	retry, permanent := PartitionUploadAck(events, resp)
	return s.ResolveUpload(path, retry, permanent)
}

// PartitionUploadAck 把响应中的事件分成暂时拒绝与永久拒绝；未列出的事件视为已确认。
func PartitionUploadAck(events []*eventv1.Event, resp *telemetryv1.UploadEventsResponse) (retry, permanent []*eventv1.Event) {
	if resp == nil {
		return nil, nil
	}
	rejected := map[string]string{}
	for _, r := range resp.Rejected {
		if r == nil {
			continue
		}
		rejected[r.EventId] = r.Code
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		code, ok := rejected[e.Id]
		if !ok {
			continue
		}
		if isPermanentCode(code) {
			permanent = append(permanent, e)
			continue
		}
		retry = append(retry, e)
	}
	return retry, permanent
}

// isPermanentCode 判断服务端拒绝代码是否表示重试无法修复的事件错误。
func isPermanentCode(code string) bool {
	switch strings.ToLower(code) {
	case "invalid_event", "unknown_asset", "invalid_argument", "permission_denied":
		return true
	default:
		return false
	}
}

// isPermanentUploadErr 判断整批上传错误是否应把对应分段移入隔离区。
func isPermanentUploadErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "invalid_argument") || strings.Contains(s, "permission_denied")
}
