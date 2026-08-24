package brain

import "testing"

func TestShouldEnqueueTriage(t *testing.T) {
	ok := triageFacts{
		Accepted: true, VerdictAllow: true, HasHTTP: true, JarvisHasPubkey: true,
	}
	cases := []struct {
		name string
		f    triageFacts
		want bool
	}{
		{name: "漏拦", f: ok, want: true},
		{name: "未接受", f: with(ok, func(f *triageFacts) { f.Accepted = false }), want: false},
		{name: "非 allow", f: with(ok, func(f *triageFacts) { f.VerdictAllow = false }), want: false},
		{name: "无 HTTP", f: with(ok, func(f *triageFacts) { f.HasHTTP = false }), want: false},
		{name: "事件已入队", f: with(ok, func(f *triageFacts) { f.EventAlreadyQueued = true }), want: false},
		{name: "路径已有规则", f: with(ok, func(f *triageFacts) { f.OpenRuleOnPath = true }), want: false},
		{name: "同路径待处理", f: with(ok, func(f *triageFacts) { f.PendingSamePath = true }), want: false},
		{name: "贾维斯无公钥", f: with(ok, func(f *triageFacts) { f.JarvisHasPubkey = false }), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEnqueueTriage(tc.f); got != tc.want {
				t.Fatalf("shouldEnqueueTriage(%+v)=%v want %v", tc.f, got, tc.want)
			}
		})
	}
}

func with(base triageFacts, edit func(*triageFacts)) triageFacts {
	edit(&base)
	return base
}

func TestShouldEnqueueProduction(t *testing.T) {
	if shouldEnqueueProduction(true, true, 0, false) {
		t.Fatal("ordinary no-detection must not enqueue")
	}
	if !shouldEnqueueProduction(true, true, 1, false) { // DETECTED_UNMITIGATED
		t.Fatal("unmitigated must enqueue")
	}
	if shouldEnqueueProduction(true, true, 1, true) {
		t.Fatal("same identity pending must not split")
	}
}
