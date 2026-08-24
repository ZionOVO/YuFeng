package kernel

import (
	"errors"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// DefaultModelIngressWindow 返回 Brain 写入签名监听计划的默认期望窗口。
func DefaultModelIngressWindow() *artifactv1.ModelIngressWindow {
	return &artifactv1.ModelIngressWindow{
		MaxItems:         ModelIngressDefaultItems,
		MaxRetainedBytes: ModelIngressDefaultBytes,
		MaxQueueAge:      durationpb.New(ModelIngressDefaultAge),
	}
}

// DefaultModelIngressHardLimit 返回 Edge 未显式收窄时使用的本机硬上限。
func DefaultModelIngressHardLimit() *artifactv1.ModelIngressWindow {
	return &artifactv1.ModelIngressWindow{
		MaxItems:         ModelIngressLocalMaxItems,
		MaxRetainedBytes: ModelIngressLocalMaxBytes,
		MaxQueueAge:      durationpb.New(ModelIngressLocalMaxAge),
	}
}

// ModelIngressWindowOrDefault 规范化窗口；缺省输入只供 Brain 签发前补齐平台默认值。
func ModelIngressWindowOrDefault(in *artifactv1.ModelIngressWindow) (*artifactv1.ModelIngressWindow, error) {
	if in == nil {
		in = DefaultModelIngressWindow()
	}
	return NormalizeModelIngressWindow(in)
}

// NormalizeModelIngressWindow 校验平台绝对边界并返回不共享调用方内存的副本。
func NormalizeModelIngressWindow(in *artifactv1.ModelIngressWindow) (*artifactv1.ModelIngressWindow, error) {
	if in == nil {
		return nil, errors.New("model ingress window is required")
	}
	if in.GetMaxItems() == 0 || in.GetMaxItems() > ModelIngressAbsoluteMaxItems {
		return nil, errors.New("model ingress max_items is outside allowed range")
	}
	if in.GetMaxRetainedBytes() < ModelIngressAbsoluteMinBytes || in.GetMaxRetainedBytes() > ModelIngressAbsoluteMaxBytes {
		return nil, errors.New("model ingress max_retained_bytes is outside allowed range")
	}
	age := in.GetMaxQueueAge()
	if age == nil || age.CheckValid() != nil {
		return nil, errors.New("model ingress max_queue_age is invalid")
	}
	duration := age.AsDuration()
	if duration < ModelIngressAbsoluteMinAge || duration > ModelIngressAbsoluteMaxAge {
		return nil, errors.New("model ingress max_queue_age is outside allowed range")
	}
	out := proto.Clone(in).(*artifactv1.ModelIngressWindow)
	// durationpb.New 把合法但非规范的秒/纳秒组合收敛为稳定签名字节。
	out.MaxQueueAge = durationpb.New(time.Duration(duration))
	return out, nil
}

// EqualModelIngressWindow 比较规范语义，不依赖调用方是否复用了消息指针。
func EqualModelIngressWindow(left, right *artifactv1.ModelIngressWindow) bool {
	return proto.Equal(left, right)
}
