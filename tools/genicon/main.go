// 生成卡通怪兽图标：build/appicon.png（Wails 用）+ assets/icon.ico（托盘用）。
// 只跑一次，产物入库。改图标就改这里再重新 go run。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type col struct{ r, g, b, a float64 }

var (
	body  = col{1.0, 0.541, 0.357, 1}   // #FF8A5B
	light = col{1.0, 0.757, 0.541, 1}   // #FFC18A
	ink   = col{0.169, 0.169, 0.169, 1} // #2B2B2B
	cream = col{1.0, 0.992, 0.969, 1}   // #FFFDF7
	clear = col{0, 0, 0, 0}
)

// squircle 超椭圆半径，n=4 得到方中带圆的手感
func squircle(x, y float64) float64 {
	const n = 4.0
	return math.Pow(math.Pow(math.Abs(x), n)+math.Pow(math.Abs(y), n), 1/n)
}

func dist(x, y, cx, cy float64) float64 {
	return math.Hypot(x-cx, y-cy)
}

// shade 返回归一化坐标处应有的颜色，全透明表示不画。
func shade(x, y float64) col {
	// 主体
	r := squircle(x, y+0.02)
	switch {
	case r > 0.80:
		return clear
	case r > 0.71:
		return ink // 粗描边
	}
	c := body
	// 左上柔光，用椭圆避开直角边
	hx, hy := (x+0.34)/0.62, (y+0.46)/0.42
	if hx*hx+hy*hy < 1 {
		c = light
	}

	// 头顶两只小角
	for _, sx := range []float64{-0.50, 0.50} {
		d := dist(x, y, sx, -0.62)
		if d <= 0.13 {
			if d > 0.10 {
				return ink
			}
			return body
		}
	}

	// 眼睛
	for _, sx := range []float64{-0.30, 0.30} {
		d := dist(x, y, sx, -0.08)
		if d <= 0.27 {
			if d > 0.235 {
				return ink
			}
			if dist(x, y, sx-0.045, -0.125) <= 0.05 {
				return cream // 高光
			}
			if d <= 0.115 {
				return ink // 瞳孔
			}
			return cream
		}
	}

	// 笑嘴：下半段椭圆环
	m := math.Pow(x/0.30, 2) + math.Pow((y-0.30)/0.20, 2)
	if m >= 0.72 && m <= 1.42 && y > 0.30 {
		return ink
	}

	return c
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var acc col
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					fy := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					c := shade(fx*2-1, fy*2-1)
					acc.r += c.r * c.a
					acc.g += c.g * c.a
					acc.b += c.b * c.a
					acc.a += c.a
				}
			}
			n := float64(ss * ss)
			i := img.PixOffset(px, py)
			if acc.a == 0 {
				continue
			}
			a := acc.a / n
			img.Pix[i+0] = uint8(acc.r / acc.a * 255)
			img.Pix[i+1] = uint8(acc.g / acc.a * 255)
			img.Pix[i+2] = uint8(acc.b / acc.a * 255)
			img.Pix[i+3] = uint8(a * 255)
		}
	}
	return img
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// writeICO 用 PNG 负载拼 ICO 容器（Vista+ 支持）。
func writeICO(path string, frames map[int][]byte) error {
	sizes := make([]int, 0, len(frames))
	for s := range frames {
		sizes = append(sizes, s)
	}
	for i := 0; i < len(sizes); i++ {
		for j := i + 1; j < len(sizes); j++ {
			if sizes[j] < sizes[i] {
				sizes[i], sizes[j] = sizes[j], sizes[i]
			}
		}
	}

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))          // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))          // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(sizes))) // count

	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		data := frames[s]
		var dim byte
		if s < 256 {
			dim = byte(s)
		}
		buf.WriteByte(dim)
		buf.WriteByte(dim)
		buf.WriteByte(0)                                    // palette
		buf.WriteByte(0)                                    // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(data)
	}
	for _, s := range sizes {
		buf.Write(frames[s])
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	master := render(256)
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)

	if err := os.WriteFile(filepath.Join(root, "build", "appicon.png"), encodePNG(master), 0o644); err != nil {
		panic(err)
	}
	// 预览用，方便人眼验收
	os.WriteFile(filepath.Join(root, "build", "icon-preview.png"), encodePNG(master), 0o644)

	frames := map[int][]byte{}
	for _, s := range []int{16, 32, 48, 256} {
		frames[s] = encodePNG(render(s))
	}
	if err := writeICO(filepath.Join(root, "assets", "icon.ico"), frames); err != nil {
		panic(err)
	}

	// 界面标题栏也用同一张脸，和 exe 图标保持一致
	os.MkdirAll(filepath.Join(root, "frontend", "dist"), 0o755)
	if err := os.WriteFile(filepath.Join(root, "frontend", "dist", "logo.png"), encodePNG(render(96)), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("生成完成: build/appicon.png, assets/icon.ico, frontend/dist/logo.png")
}
