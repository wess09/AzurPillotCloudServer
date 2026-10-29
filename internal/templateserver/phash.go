package templateserver

import (
	"fmt"
	"image"
	"math/bits"
	"strconv"
)

// 感知哈希参数：9×8 灰度采样、相邻列比较，得到 64 位 dHash。
const (
	dhashCols = 9
	dhashRows = 8
	// phashMatchThreshold 是两个 dHash 视为同一图标的最大汉明距离。
	phashMatchThreshold = 8
	// phashMargin 要求最佳匹配比次佳匹配至少好这么多，避免把边界样本误并。
	phashMargin = 2
)

// dHash 计算图像的 64 位差分哈希。透明像素按白色合成。
// 图像小于 9×8 时返回 0（调用方应先行校验尺寸）。
func dHash(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < dhashCols || h < dhashRows {
		return 0
	}
	var sum [dhashRows][dhashCols]float64
	var count [dhashRows][dhashCols]int
	for y := 0; y < h; y++ {
		gy := y * dhashRows / h
		for x := 0; x < w; x++ {
			gx := x * dhashCols / w
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			alpha := float64(a) / 65535
			lum := (0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8))
			lum = lum*alpha + 255*(1-alpha)
			sum[gy][gx] += lum
			count[gy][gx]++
		}
	}
	var hash uint64
	var bit uint
	for y := 0; y < dhashRows; y++ {
		for x := 0; x < dhashCols-1; x++ {
			left, right := 0.0, 0.0
			if count[y][x] > 0 {
				left = sum[y][x] / float64(count[y][x])
			}
			if count[y][x+1] > 0 {
				right = sum[y][x+1] / float64(count[y][x+1])
			}
			if left > right {
				hash |= 1 << bit
			}
			bit++
		}
	}
	return hash
}

func formatPHash(h uint64) string {
	return fmt.Sprintf("%016x", h)
}

func parsePHash(hex string) (uint64, error) {
	if len(hex) != 16 {
		return 0, fmt.Errorf("invalid phash length")
	}
	return strconv.ParseUint(hex, 16, 64)
}

func hamming64(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// bestDistance 返回最小距离与次小距离，用于判定匹配是否足够明确。
func bestDistance(h uint64, candidates []uint64) (best, second int) {
	best, second = 65, 65
	for _, c := range candidates {
		d := hamming64(h, c)
		if d < best {
			second = best
			best = d
			continue
		}
		if d < second {
			second = d
		}
	}
	return best, second
}
