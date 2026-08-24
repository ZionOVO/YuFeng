package eventbus

import "testing"

// 未连接的总线发布必须显式报错而非静默丢弃（占位测试；真实收发
// 随生产队列装配落地后补端到端用例）。
func TestPublishWithoutConnectionFails(t *testing.T) {
	var b *Bus
	if err := b.Publish("yufeng.test", []byte("{}")); err == nil {
		t.Fatal("nil 总线发布应报错")
	}
	if err := (&Bus{}).Publish("yufeng.test", []byte("{}")); err == nil {
		t.Fatal("未连接总线发布应报错")
	}
}
