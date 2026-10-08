// Package icons provides embedded icon data for the system tray.
//
// Icons are multi-resolution ICO files (16x16, 20x20, 24x24, 32x32) with a
// shield/check status glyph shared conceptually with the Tauri tray. The app
// launcher icon intentionally uses the public WireZTNA W mark instead.
//
// Three states shared with the Tauri shell on macOS/Linux:
//
//	Connected    — system green shield (healthy connection)
//	Attention    — system orange shield (transition or actionable problem)
//	Disconnected — system gray shield (stable disconnected state)
package icons

// Connected is the tray icon shown when the tunnel is confirmed healthy.
var Connected = generateMultiResICO(0x34, 0xC7, 0x59, 0x24, 0x8C, 0x3C) // System green

// Attention is shown while connecting or whenever service/auth/health needs attention.
var Attention = generateMultiResICO(0xFF, 0x95, 0x00, 0xC9, 0x6F, 0x00) // System orange

// Disconnected is shown only for a stable disconnected state.
var Disconnected = generateMultiResICO(0x8E, 0x8E, 0x93, 0x63, 0x63, 0x66) // System gray

// generateMultiResICO creates a multi-resolution ICO with shield shapes at
// 16x16, 20x20, 24x24, and 32x32. Windows picks the best size for the
// current DPI. High-DPI displays (125%, 150%, 200%) benefit from the larger
// sizes where the shield detail is preserved.
func generateMultiResICO(fillR, fillG, fillB, darkR, darkG, darkB byte) []byte {
	sizes := []int{16, 20, 24, 32}
	imageCount := len(sizes)

	// ICO header (6 bytes)
	ico := []byte{
		0x00, 0x00, // Reserved
		0x01, 0x00, // Type: ICO
		byte(imageCount), 0x00, // Count
	}

	// Calculate sizes and offsets for directory entries
	type imgInfo struct {
		size    int
		dataLen int
		offset  int
	}
	images := make([]imgInfo, imageCount)
	// Directory starts after header (6 bytes), each entry is 16 bytes
	dataOffset := 6 + (imageCount * 16)

	for i, size := range sizes {
		// BMP header (40) + pixel data (size*size*4) + AND mask (rows of padded bytes)
		andRowBytes := ((size + 31) / 32) * 4
		dataLen := 40 + (size * size * 4) + (size * andRowBytes)
		images[i] = imgInfo{size: size, dataLen: dataLen, offset: dataOffset}
		dataOffset += dataLen
	}

	// Write directory entries
	for _, img := range images {
		sz := byte(img.size)
		if img.size == 256 {
			sz = 0 // 0 means 256 in ICO format
		}
		ico = append(ico,
			sz, sz, // Width, Height
			0x00, 0x00, // Color palette, Reserved
			0x01, 0x00, // Color planes
			0x20, 0x00, // Bits per pixel (32)
			byte(img.dataLen), byte(img.dataLen>>8), byte(img.dataLen>>16), byte(img.dataLen>>24),
			byte(img.offset), byte(img.offset>>8), byte(img.offset>>16), byte(img.offset>>24),
		)
	}

	// Write image data for each size
	for _, img := range images {
		ico = append(ico, generateShieldBMP(img.size, fillR, fillG, fillB, darkR, darkG, darkB)...)
	}

	return ico
}

// generateShieldBMP creates a BITMAPINFOHEADER + pixel data + AND mask for a
// shield icon at the given size. The shield has a pointed bottom, curved top
// edges, a subtle inner highlight, and a small checkmark in the center.
func generateShieldBMP(size int, fillR, fillG, fillB, darkR, darkG, darkB byte) []byte {
	// BITMAPINFOHEADER (40 bytes)
	bmp := []byte{
		0x28, 0x00, 0x00, 0x00, // biSize = 40
		byte(size), 0x00, 0x00, 0x00, // biWidth
		byte(size * 2), 0x00, 0x00, 0x00, // biHeight (doubled for ICO: XOR + AND)
		0x01, 0x00, // biPlanes
		0x20, 0x00, // biBitCount = 32
		0x00, 0x00, 0x00, 0x00, // biCompression
		0x00, 0x00, 0x00, 0x00, // biSizeImage
		0x00, 0x00, 0x00, 0x00, // biXPelsPerMeter
		0x00, 0x00, 0x00, 0x00, // biYPelsPerMeter
		0x00, 0x00, 0x00, 0x00, // biClrUsed
		0x00, 0x00, 0x00, 0x00, // biClrImportant
	}

	// Generate the shield bitmap
	pixels := renderShield(size, fillR, fillG, fillB, darkR, darkG, darkB)

	// Write pixel data bottom-to-top (BMP format)
	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			p := pixels[y*size+x]
			bmp = append(bmp, p.b, p.g, p.r, p.a)
		}
	}

	// AND mask (all zeros = fully opaque where alpha says so)
	andRowBytes := ((size + 31) / 32) * 4
	for y := 0; y < size; y++ {
		for i := 0; i < andRowBytes; i++ {
			bmp = append(bmp, 0x00)
		}
	}

	return bmp
}

type pixel struct {
	r, g, b, a byte
}

// renderShield generates a shield shape at the given size with:
// - Outer edge (dark color, 1px)
// - Fill body (main color)
// - Inner highlight (lighter, top portion)
// - Small checkmark in center (white)
func renderShield(size int, fillR, fillG, fillB, darkR, darkG, darkB byte) []pixel {
	pixels := make([]pixel, size*size)

	// Shield geometry ratios (normalized 0..1)
	// The shield is wider at the top and comes to a point at the bottom
	centerX := float64(size) / 2.0
	topY := float64(size) * 0.08      // top edge
	shoulderY := float64(size) * 0.30 // widest point
	waistY := float64(size) * 0.65    // start narrowing
	bottomY := float64(size) * 0.92   // point

	maxHalfW := float64(size) * 0.42 // half-width at widest

	for y := 0; y < size; y++ {
		fy := float64(y)
		var halfWidth float64

		if fy < topY {
			halfWidth = 0
		} else if fy < shoulderY {
			// Curve from narrow top to full width
			t := (fy - topY) / (shoulderY - topY)
			// Ease-out curve for rounded top
			t = 1 - (1-t)*(1-t)
			halfWidth = maxHalfW * t
		} else if fy < waistY {
			// Full width section
			halfWidth = maxHalfW
		} else if fy < bottomY {
			// Taper to point
			t := (fy - waistY) / (bottomY - waistY)
			halfWidth = maxHalfW * (1 - t*t) // Quadratic taper
		} else {
			halfWidth = 0
		}

		for x := 0; x < size; x++ {
			fx := float64(x)
			dist := abs64(fx - centerX)

			if halfWidth <= 0.5 || dist > halfWidth+0.5 {
				// Outside shield — transparent
				pixels[y*size+x] = pixel{0, 0, 0, 0}
			} else if dist > halfWidth-1.0 {
				// Edge pixel (dark outline)
				alpha := byte(255)
				if dist > halfWidth-0.5 {
					// Anti-alias the outer edge
					alpha = byte(clamp255(int((halfWidth + 0.5 - dist) * 255)))
				}
				pixels[y*size+x] = pixel{darkR, darkG, darkB, alpha}
			} else {
				// Inside the shield
				// Top 40% gets a subtle highlight (10% lighter)
				verticalT := (fy - topY) / (bottomY - topY)
				if verticalT < 0.4 {
					hr := clampByte(int(fillR) + 20)
					hg := clampByte(int(fillG) + 20)
					hb := clampByte(int(fillB) + 20)
					pixels[y*size+x] = pixel{hr, hg, hb, 255}
				} else {
					pixels[y*size+x] = pixel{fillR, fillG, fillB, 255}
				}
			}
		}
	}

	// Draw a small checkmark in the center (white, 60% opacity for subtlety)
	drawCheckmark(pixels, size)

	return pixels
}

// drawCheckmark renders a small checkmark in the center of the shield.
func drawCheckmark(pixels []pixel, size int) {
	// Checkmark geometry scaled to icon size
	cx := float64(size) / 2.0
	cy := float64(size) * 0.55
	scale := float64(size) / 32.0

	// Two line segments forming a checkmark: short down-right, then long up-right
	// Segment 1: from (-3, 0) to (-1, 2) (short leg)
	// Segment 2: from (-1, 2) to (4, -3) (long leg)
	type point struct{ x, y float64 }
	seg1 := [2]point{{-3, 0}, {-1, 2}}
	seg2 := [2]point{{-1, 2}, {4, -3}}

	thickness := 1.4 * scale
	if thickness < 1.2 {
		thickness = 1.2
	}

	drawLine(pixels, size, cx, cy, seg1[0], seg1[1], scale, thickness)
	drawLine(pixels, size, cx, cy, seg2[0], seg2[1], scale, thickness)
}

// drawLine draws an anti-aliased line segment on the pixel buffer.
func drawLine(pixels []pixel, size int, cx, cy float64, p1, p2 struct{ x, y float64 }, scale, thickness float64) {
	x1 := cx + p1.x*scale
	y1 := cy + p1.y*scale
	x2 := cx + p2.x*scale
	y2 := cy + p2.y*scale

	dx := x2 - x1
	dy := y2 - y1
	length := sqrt64(dx*dx + dy*dy)
	if length < 0.1 {
		return
	}

	// Normal vector for thickness
	nx := -dy / length
	ny := dx / length

	steps := int(length*2) + 1
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px := x1 + dx*t
		py := y1 + dy*t

		// Draw perpendicular to the line for thickness
		thickSteps := int(thickness*2) + 1
		for j := 0; j <= thickSteps; j++ {
			tt := float64(j)/float64(thickSteps)*2 - 1 // -1 to 1
			sx := px + nx*tt*thickness/2
			sy := py + ny*tt*thickness/2

			ix := int(sx + 0.5)
			iy := int(sy + 0.5)
			if ix >= 0 && ix < size && iy >= 0 && iy < size {
				idx := iy*size + ix
				// Only draw on non-transparent pixels (inside the shield)
				if pixels[idx].a > 0 {
					// White with distance-based alpha for anti-aliasing
					dist := abs64(tt)
					alpha := byte(clamp255(int((1.0 - dist) * 200)))
					if alpha > 40 {
						pixels[idx] = blendPixel(pixels[idx], pixel{255, 255, 255, alpha})
					}
				}
			}
		}
	}
}

// blendPixel blends a foreground pixel over a background pixel.
func blendPixel(bg, fg pixel) pixel {
	fa := float64(fg.a) / 255.0
	ba := float64(bg.a) / 255.0
	oa := fa + ba*(1-fa)
	if oa < 0.01 {
		return pixel{0, 0, 0, 0}
	}
	or := (float64(fg.r)*fa + float64(bg.r)*ba*(1-fa)) / oa
	og := (float64(fg.g)*fa + float64(bg.g)*ba*(1-fa)) / oa
	ob := (float64(fg.b)*fa + float64(bg.b)*ba*(1-fa)) / oa
	return pixel{byte(or), byte(og), byte(ob), byte(oa * 255)}
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func sqrt64(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton's method — sufficient precision for pixel work
	z := x / 2
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func clamp255(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func clampByte(v int) byte {
	return byte(clamp255(v))
}
