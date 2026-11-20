package main

import "testing"

func TestPickQRCkPrefersGeneratedValue(t *testing.T) {
	ck, same := gfPickQRCk("generated-key", "hsiz-key")
	if ck != "generated-key" {
		t.Fatalf("轮询必须使用接口返回的 ck，实际 %q", ck)
	}
	if same {
		t.Fatal("不同的 ck 不应被报告为与 hsiz 相同")
	}
}

func TestPickQRCkFallsBackToHsiz(t *testing.T) {
	ck, same := gfPickQRCk("", "hsiz-key")
	if ck != "hsiz-key" || !same {
		t.Fatalf("ck 缺失时应回退 hsiz，实际 ck=%q same=%t", ck, same)
	}
}
