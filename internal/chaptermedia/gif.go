package chaptermedia

import (
	"encoding/binary"
	"fmt"
)

const maxGIFFrames = 200

// validateGIFFrameBudget scans GIF block boundaries before gif.DecodeAll can
// allocate image buffers. The input is already limited to MaxImageBytes. The
// standard decoder subsequently validates LZW data, extensions and all frames.
func validateGIFFrameBudget(data []byte) error {
	invalid := func() error { return fmt.Errorf("%w：GIF 动画内容不完整", ErrInvalid) }
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return invalid()
	}
	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 << ((data[10] & 7) + 1)
	}
	if offset > len(data) {
		return invalid()
	}
	skipBlocks := func() error {
		for {
			if offset >= len(data) {
				return invalid()
			}
			size := int(data[offset])
			offset++
			if size == 0 {
				return nil
			}
			if size > len(data)-offset {
				return invalid()
			}
			offset += size
		}
	}
	var pixels int64
	frames := 0
	for offset < len(data) {
		marker := data[offset]
		offset++
		switch marker {
		case 0x21: // Extension label followed by length-prefixed sub-blocks.
			if offset >= len(data) {
				return invalid()
			}
			offset++
			if err := skipBlocks(); err != nil {
				return err
			}
		case 0x2c: // Image descriptor, optional local color table, LZW data.
			if len(data)-offset < 9 {
				return invalid()
			}
			width := int64(binary.LittleEndian.Uint16(data[offset+4 : offset+6]))
			height := int64(binary.LittleEndian.Uint16(data[offset+6 : offset+8]))
			packed := data[offset+8]
			if width <= 0 || height <= 0 {
				return invalid()
			}
			frames++
			pixels += width * height
			if frames > maxGIFFrames || pixels > MaxImagePixels {
				return fmt.Errorf("%w：GIF 最多支持 200 帧，所有帧总像素不能超过 4000 万", ErrInvalid)
			}
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 << ((packed & 7) + 1)
			}
			if offset >= len(data) {
				return invalid()
			}
			offset++ // LZW minimum code size; DecodeAll verifies its value.
			if err := skipBlocks(); err != nil {
				return err
			}
		case 0x3b:
			if frames == 0 {
				return invalid()
			}
			return nil
		default:
			return invalid()
		}
	}
	return invalid() // Missing trailer, even if the first frame was complete.
}
