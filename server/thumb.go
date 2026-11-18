package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ============================================================
// 附件缩略图
//
// 手机截图原图普遍 1MB 上下（1080×2400）。列表里只需要 40px 的小图，
// 直接下发原图意味着一屏 20 条就是 20MB —— 这是移动端最主要的卡顿来源。
// 这里按需生成缩略图并落盘缓存，原图只在「查看大图」时下载。
//
// 只用标准库：image/png、image/jpeg、image/gif 都能解，
// 缩小用面积平均（比最近邻干净得多，且只遍历源图一遍）。
// webp / heic 没有标准库解码器，此时返回空串，调用方回落原图。
// ============================================================

const (
	thumbDefaultW = 160
	thumbMinW     = 48
	thumbMaxW     = 1600
	thumbQuality  = 82
)

func thumbsDir() string { return filepath.Join(attachmentsDir(), ".thumbs") }

// thumbFileFor 缩略图文件名：<原文件名去扩展名>_w<宽>.jpg
func thumbFileFor(name string, w int) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return fmt.Sprintf("%s_w%d.jpg", base, w)
}

func clampThumbW(w int) int {
	if w <= 0 {
		return thumbDefaultW
	}
	if w < thumbMinW {
		return thumbMinW
	}
	if w > thumbMaxW {
		return thumbMaxW
	}
	return w
}

// decodeImage 解常见格式；不支持的返回 nil（调用方回落原图）。
func decodeImage(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		img, err := png.Decode(f)
		if err != nil {
			return nil
		}
		return img
	case ".jpg", ".jpeg":
		img, err := jpeg.Decode(f)
		if err != nil {
			return nil
		}
		return img
	case ".gif":
		img, err := gif.Decode(f)
		if err != nil {
			return nil
		}
		return img
	}
	return nil
}

// scaleArea 面积平均缩小：每个目标像素取源图对应矩形内所有像素的均值。
// 源图只遍历一遍，1080×2400 缩到 720 宽约 20~30ms。
func scaleArea(src image.Image, dstW int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if dstW >= sw {
		dstW = sw
	}
	dstH := int(float64(sh)*float64(dstW)/float64(sw) + 0.5)
	if dstH < 1 {
		dstH = 1
	}
	// 先整块拷进 RGBA，后续按索引取值，避免逐像素走 At() 的接口开销
	srcRGBA := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(srcRGBA, srcRGBA.Bounds(), src, b.Min, draw.Src)

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		y0 := y * sh / dstH
		y1 := (y + 1) * sh / dstH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > sh {
			y1 = sh
		}
		for x := 0; x < dstW; x++ {
			x0 := x * sw / dstW
			x1 := (x + 1) * sw / dstW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > sw {
				x1 = sw
			}
			var sr, sg, sb, sa, n uint32
			for sy := y0; sy < y1; sy++ {
				row := srcRGBA.Pix[sy*srcRGBA.Stride:]
				for sx := x0; sx < x1; sx++ {
					i := sx * 4
					a := uint32(row[i+3])
					// 预乘 alpha 后再平均，半透明边缘不会发黑
					sr += uint32(row[i]) * a
					sg += uint32(row[i+1]) * a
					sb += uint32(row[i+2]) * a
					sa += a
					n++
				}
			}
			if n == 0 {
				continue
			}
			o := y*dst.Stride + x*4
			if sa > 0 {
				dst.Pix[o] = uint8(sr / sa)
				dst.Pix[o+1] = uint8(sg / sa)
				dst.Pix[o+2] = uint8(sb / sa)
				dst.Pix[o+3] = uint8(sa / n)
			} else {
				dst.Pix[o+3] = 0
			}
		}
	}
	return dst
}

// ensureThumb 返回缩略图路径；格式不支持或解码失败时返回空串。
// 生成失败的标记文件（.fail）避免每次请求都重试解码——一张坏图不该反复消耗 CPU。
func ensureThumb(name string, w int) string {
	if !validAttachmentName(name) {
		return ""
	}
	w = clampThumbW(w)
	dir := thumbsDir()
	out := filepath.Join(dir, thumbFileFor(name, w))
	if st, err := os.Stat(out); err == nil && st.Size() > 0 {
		return out
	}
	src := filepath.Join(attachmentsDir(), name)
	if _, err := os.Stat(src); err != nil {
		return ""
	}
	if _, err := os.Stat(out + ".fail"); err == nil {
		return ""
	}
	img := decodeImage(src)
	if img == nil {
		markThumbFail(out)
		return ""
	}
	scaled := scaleArea(img, w)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: thumbQuality}); err != nil {
		markThumbFail(out)
		return ""
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	// 先写临时文件再原子改名：并发请求不会读到写了一半的 jpg
	tmp := out + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0644); err != nil {
		return ""
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return ""
	}
	return out
}

func markThumbFail(out string) {
	if err := os.MkdirAll(thumbsDir(), 0755); err == nil {
		_ = os.WriteFile(out+".fail", nil, 0644)
	}
}

// removeThumbs 删除某张附件对应的所有尺寸缩略图（附件被替换或删除时调用）。
func removeThumbs(name string) {
	if !validAttachmentName(name) {
		return
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	matches, _ := filepath.Glob(filepath.Join(thumbsDir(), base+"_w*.jpg*"))
	for _, m := range matches {
		// 再确认一次落点就在 thumbs 目录里，防止任何拼接意外
		if filepath.Dir(m) == filepath.Clean(thumbsDir()) {
			_ = os.Remove(m)
		}
	}
}
