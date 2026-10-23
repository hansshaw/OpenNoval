package chaptermedia

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"strings"
	"testing"
)

func animation(t *testing.T, frames int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	a := &gif.GIF{LoopCount: 0}
	for i := 0; i < frames; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
		frame.Pix[i%4] = 1
		a.Image = append(a.Image, frame)
		a.Delay = append(a.Delay, 5)
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, a); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestPrepareValidatesAllGIFFramesAndPreservesAnimation(t *testing.T) {
	data := animation(t, 2)
	upload, err := Prepare(data, "animated.gif")
	if err != nil {
		t.Fatal(err)
	}
	if upload.mime != "image/gif" || !bytes.Equal(upload.data, data) {
		t.Fatal("valid animation was changed or flattened")
	}
	if _, err := Prepare(data[:len(data)-10], "truncated.gif"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("truncated second frame accepted: %v", err)
	}
	if _, err := Prepare(data[:len(data)-1], "no-trailer.gif"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing GIF trailer accepted: %v", err)
	}
}

func TestGIFFrameBudgetRejectsAnimationBeforeDecodingAllFrames(t *testing.T) {
	if _, err := Prepare(animation(t, maxGIFFrames+1), "many-frames.gif"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "200 帧") {
		t.Fatalf("frame allocation limit not enforced: %v", err)
	}
	// The descriptors request 50 million decoded pixels while the compressed
	// payload remains tiny. A budget error must happen before LZW decoding or
	// allocation (the deliberately tiny LZW block cannot fill these frames).
	data := []byte{'G', 'I', 'F', '8', '9', 'a', 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(data[6:8], 5000)
	binary.LittleEndian.PutUint16(data[8:10], 5000)
	for i := 0; i < 2; i++ {
		descriptor := []byte{0x2c, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.LittleEndian.PutUint16(descriptor[5:7], 5000)
		binary.LittleEndian.PutUint16(descriptor[7:9], 5000)
		data = append(data, descriptor...)
		data = append(data, 2, 2, 0x44, 0x01, 0)
	}
	data = append(data, 0x3b)
	if _, err := Prepare(data, "oversized-animation.gif"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "所有帧总像素") {
		t.Fatalf("aggregate allocation limit did not run before decoding: %v", err)
	}
}
