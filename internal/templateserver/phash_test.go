package templateserver

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func patternPNG(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 49, 49))
	for y := 0; y < 49; y++ {
		for x := 0; x < 49; x++ {
			v := uint8((x*7 + y*13 + seed*29) % 256)
			img.SetRGBA(x, y, color.RGBA{R: v, G: uint8(int(v) * 3 % 256), B: uint8(int(v) * 5 % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成测试 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

func TestDHashDeterministic(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(patternPNG(t, 7)))
	if err != nil {
		t.Fatal(err)
	}
	a, b := dHash(img), dHash(img)
	if a != b {
		t.Fatalf("同一图像哈希不稳定: %x != %x", a, b)
	}
	if a == 0 {
		t.Fatal("测试图像不应得到全零哈希")
	}
}

func TestDHashDistinguishesPatterns(t *testing.T) {
	imgA, _ := png.Decode(bytes.NewReader(patternPNG(t, 1)))
	imgB, _ := png.Decode(bytes.NewReader(patternPNG(t, 2)))
	ha, hb := dHash(imgA), dHash(imgB)
	if ha == hb {
		t.Fatal("不同图案得到相同哈希")
	}
	if d := hamming64(ha, hb); d < phashMargin {
		t.Fatalf("不同图案距离过近: %d", d)
	}
}

func TestMatchKnownTemplateMargin(t *testing.T) {
	base := uint64(0x0f0f_0f0f_0f0f_0f0f)
	entries := []knownTemplate{
		{Key: "a", Scene: "all", Hash: base ^ 0b11111},
		{Key: "b", Scene: "all", Hash: base ^ 0b111111},
	}
	if _, ok := matchKnownTemplate(entries, base, "commission"); ok {
		t.Fatal("两个候选距离过近时不应匹配")
	}
	only := []knownTemplate{{Key: "a", Scene: "commission", Hash: base}}
	if got, ok := matchKnownTemplate(only, base^0b11111, "commission"); !ok || got.Key != "a" {
		t.Fatalf("单一候选近距离应匹配: ok=%v got=%q", ok, got.Key)
	}
	if _, ok := matchKnownTemplate(only, base, "research"); ok {
		t.Fatal("场景不兼容的模板不应匹配")
	}
}

func TestParsePHash(t *testing.T) {
	h := dHash(mustDecode(t, patternPNG(t, 9)))
	got, err := parsePHash(formatPHash(h))
	if err != nil || got != h {
		t.Fatalf("phash 往返失败: %v %x != %x", err, got, h)
	}
	if _, err := parsePHash("zz"); err == nil {
		t.Fatal("非法 phash 应报错")
	}
}

func mustDecode(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码 PNG 失败: %v", err)
	}
	return img
}
