package processor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// noisyJPEG is a photo-sized JPEG of random pixels, which compresses badly:
// like a full-size phone photo, it is well over Claude's 5 MB.
func noisyJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = uint8(random.UintN(256))
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// withOrientation adds an EXIF segment with the given orientation to a JPEG,
// as phones write it (big-endian TIFF, one IFD entry).
func withOrientation(t *testing.T, data []byte, orientation uint16) []byte {
	t.Helper()
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01")
	entry := make([]byte, 12)
	binary.BigEndian.PutUint16(entry[0:], 0x0112)
	binary.BigEndian.PutUint16(entry[2:], 3)
	binary.BigEndian.PutUint32(entry[4:], 1)
	binary.BigEndian.PutUint16(entry[8:], orientation)
	tiff = append(append(tiff, entry...), 0, 0, 0, 0)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	header := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(header[2:], uint16(len(segment)+2))
	out := append([]byte{}, data[:2]...)
	out = append(append(out, header...), segment...)
	return append(out, data[2:]...)
}

func decoded(t *testing.T, img Image) image.Image {
	t.Helper()
	out, format, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("result does not decode: %v", err)
	}
	if "image/"+format != img.ContentType {
		t.Fatalf("content type %s, but the data is %s", img.ContentType, format)
	}
	return out
}

func TestOversizedPhotoIsScaledDownForClaude(t *testing.T) {
	original := noisyJPEG(t, 6000, 4000)
	if len(original) <= 5<<20 {
		t.Fatalf("fixture is only %d bytes", len(original))
	}
	fitted, err := fitClaudeImages([]Image{{ContentType: "image/jpeg", Data: original}})
	if err != nil {
		t.Fatal(err)
	}
	got := fitted[0]
	if got.ContentType != "image/jpeg" || len(got.Data) > claudeMaxImageBytes {
		t.Fatalf("fitted image is %s of %d bytes", got.ContentType, len(got.Data))
	}
	// The API's limit is on the base64 data.
	if encoded := base64.StdEncoding.EncodedLen(len(got.Data)); encoded > 5<<20 {
		t.Fatalf("base64 data is %d bytes", encoded)
	}
	bounds := decoded(t, got).Bounds()
	if bounds.Dx() != claudeImageLongEdge || bounds.Dy() != 1717 {
		t.Fatalf("fitted image is %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestImagesWithinClaudesLimitsAreSentAsTheyAre(t *testing.T) {
	small := noisyJPEG(t, 400, 300)
	var flat bytes.Buffer
	if err := png.Encode(&flat, image.NewGray(image.Rect(0, 0, 3000, 2000))); err != nil {
		t.Fatal(err)
	}
	images := []Image{
		{ContentType: "image/jpeg", Data: small},
		{ContentType: "image/png", Data: flat.Bytes()},
		// A format only Claude reads is sent as it is while it is small.
		{ContentType: "image/webp", Data: []byte("RIFF....WEBPVP8 small")},
	}
	fitted, err := fitClaudeImages(images)
	if err != nil {
		t.Fatal(err)
	}
	for i := range images {
		if fitted[i].ContentType != images[i].ContentType || !bytes.Equal(fitted[i].Data, images[i].Data) {
			t.Fatalf("image %d was changed", i+1)
		}
	}
}

func TestImageOverClaudesSideLimitIsScaledDownOntoWhite(t *testing.T) {
	// Small in bytes but 9000 px wide, and transparent.
	wide := image.NewNRGBA(image.Rect(0, 0, 9000, 40))
	var out bytes.Buffer
	if err := png.Encode(&out, wide); err != nil {
		t.Fatal(err)
	}
	fitted, err := fitClaudeImages([]Image{{ContentType: "image/png", Data: out.Bytes()}})
	if err != nil {
		t.Fatal(err)
	}
	img := decoded(t, fitted[0])
	if img.Bounds().Dx() != claudeImageLongEdge || img.Bounds().Dy() != 11 {
		t.Fatalf("fitted image is %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// JPEG has no transparency: it is flattened onto white.
	if r, g, b, _ := img.At(100, 5).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("transparent pixel became %v", img.At(100, 5))
	}
}

func TestScaledDownPhotoKeepsItsExifOrientation(t *testing.T) {
	// A portrait photo stored landscape, with orientation 6 (turn it a
	// quarter clockwise): the left half is black, the right half white.
	stored := image.NewGray(image.Rect(0, 0, 9000, 60))
	for y := 0; y < 60; y++ {
		for x := 4500; x < 9000; x++ {
			stored.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, stored, nil); err != nil {
		t.Fatal(err)
	}
	data := withOrientation(t, out.Bytes(), 6)
	if got := jpegOrientation(data); got != 6 {
		t.Fatalf("orientation = %d", got)
	}
	fitted, err := fitClaudeImages([]Image{{ContentType: "image/jpeg", Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	img := decoded(t, fitted[0])
	if img.Bounds().Dx() != 17 || img.Bounds().Dy() != claudeImageLongEdge {
		t.Fatalf("fitted image is %dx%d, not portrait", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// Turned clockwise, the stored left (black) is now at the top.
	top, _, _, _ := img.At(8, 100).RGBA()
	bottom, _, _, _ := img.At(8, claudeImageLongEdge-100).RGBA()
	if top>>8 > 50 || bottom>>8 < 200 {
		t.Fatalf("top = %d, bottom = %d", top>>8, bottom>>8)
	}
}

func TestJPEGOrientationWithoutExifIsUpright(t *testing.T) {
	if got := jpegOrientation(noisyJPEG(t, 8, 8)); got != 1 {
		t.Fatalf("orientation = %d", got)
	}
	if got := jpegOrientation([]byte("not a jpeg")); got != 1 {
		t.Fatalf("orientation = %d", got)
	}
}

func TestOversizedImageThatCannotBeDecodedCannotBeSent(t *testing.T) {
	// A WebP over the limit: Go cannot decode it to make it smaller.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0x5a}, claudeMaxImageBytes)...)
	_, err := fitClaudeImages([]Image{{ContentType: "image/jpeg", Data: noisyJPEG(t, 8, 8)}, {ContentType: "image/webp", Data: webp}})
	if !errors.Is(err, errImageNotSendable) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "image 2 of 2") || !strings.Contains(err.Error(), "image/webp") {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeSendsAScaledDownPhotoOnStdin(t *testing.T) {
	dir := t.TempDir()
	claude := recordingClaude(t, dir)
	images := CaptureImages{Images: []Image{{ContentType: "image/jpeg", Data: noisyJPEG(t, 6000, 4000)}}}
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir}).Plan(WithImages(context.Background(), images), "Letter"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	var message struct {
		Message struct {
			Content []struct {
				Source struct {
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	source := message.Message.Content[0].Source
	if source.MediaType != "image/jpeg" || len(source.Data) == 0 || len(source.Data) > 5<<20 {
		t.Fatalf("image block is %s with %d bytes of base64", source.MediaType, len(source.Data))
	}
}

func TestClaudeDoesNotRunWithAnImageItCannotSend(t *testing.T) {
	dir := t.TempDir()
	claude := fakeClaude(t, "touch "+dir+"/ran")
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{1}, claudeMaxImageBytes)...)
	images := CaptureImages{Images: []Image{{ContentType: "image/webp", Data: webp}}}
	_, err := (ClaudeCLI{Command: claude, WorkDir: dir}).Plan(WithImages(context.Background(), images), "Photo")
	if !errors.Is(err, errImageNotSendable) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ran")); statErr == nil {
		t.Fatal("Claude Code ran")
	}
}

func TestCaptureWithAnImageThatCannotBeSentGoesToHumanReview(t *testing.T) {
	tools := &fakeTools{}
	planner := fakePlanner{err: imageNotSendable{index: 1, count: 1, reason: "image/webp, 9.0 MB that cannot be decoded here"}}
	monitor := NewMonitor("sonnet")
	_, err := (Runner{Planner: monitor.Monitored(planner), Tools: tools, Model: "sonnet"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := tools.records[len(tools.records)-1]
	if last["status"] != "human_review" {
		t.Fatalf("records = %#v", tools.records)
	}
	for _, record := range tools.records {
		if record["status"] == "retryable" || record["status"] == "failed" {
			t.Fatalf("the capture would be retried: %#v", record)
		}
	}
	// Nothing ran, so the runtime's own state is untouched.
	if status := monitor.Snapshot(); status.Codex.ConsecutiveFailures != 0 || status.Codex.LastError != "" {
		t.Fatalf("runtime state = %+v", status.Codex)
	}
}

// gradient16PNG is a 16-bit PNG with partial transparency (decoded as
// *image.NRGBA64), which compresses well: a few MB on disk, 8 bytes a pixel
// once decoded.
func gradient16PNG(tb testing.TB, width, height int) []byte {
	tb.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x * 7), G: uint16(y * 11), B: uint16(x + y), A: 0xff00 | uint16(x&0xff)})
		}
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		tb.Fatal(err)
	}
	return out.Bytes()
}

// peakHeap runs f and returns the most heap in use while it ran, sampled
// every millisecond.
func peakHeap(f func()) uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	base, peak := stats.HeapAlloc, stats.HeapAlloc
	done, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		var s runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
				runtime.ReadMemStats(&s)
				peak = max(peak, s.HeapAlloc)
			}
		}
	}()
	f()
	close(done)
	<-sampled
	return peak - base
}

// BenchmarkFitLarge16BitPNG shrinks a 30 MP 16-bit PNG over the side limit
// but within the decode budget (243 MB decoded), and reports the peak heap it took besides the
// allocations.
func BenchmarkFitLarge16BitPNG(b *testing.B) {
	data := gradient16PNG(b, 8200, 3700)
	b.Logf("PNG of %.1f MB", float64(len(data))/(1<<20))
	capture := Image{ContentType: "image/png", Data: data}
	b.ReportAllocs()
	b.ResetTimer()
	var peak uint64
	for range b.N {
		peak = max(peak, peakHeap(func() {
			if _, err := fitClaudeImage(capture); err != nil {
				b.Fatal(err)
			}
		}))
	}
	b.ReportMetric(float64(peak)/(1<<20), "peak-heap-MiB")
}

// pngHeader is the start of a PNG, its signature and IHDR, which is all
// image.DecodeConfig reads: the pixels are missing, so it does not decode.
func pngHeader(width, height uint32, depth, colorType, interlace byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], width)
	binary.BigEndian.PutUint32(ihdr[4:], height)
	ihdr[8], ihdr[9], ihdr[12] = depth, colorType, interlace
	return pngChunk([]byte("\x89PNG\r\n\x1a\n"), "IHDR", ihdr)
}

// pngChunk appends a PNG chunk to out.
func pngChunk(out []byte, kind string, data []byte) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	chunk := append([]byte(kind), data...)
	return binary.BigEndian.AppendUint32(append(out, chunk...), crc32.ChecksumIEEE(chunk))
}

func TestImageTooLargeToDecodeCannotBeSent(t *testing.T) {
	// 9000×4000 px at 16 bits a channel is 288 MB decoded, over the budget;
	// at 8 bits it is 144 MB, within it.
	over := pngHeader(9000, 4000, 16, 6, 0)
	_, err := fitClaudeImages([]Image{{ContentType: "image/png", Data: over}})
	if !errors.Is(err, errImageNotSendable) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "9000×4000 px, too large to make smaller here (about 274 MB to decode, more than 256 MB)") {
		t.Fatalf("err = %v", err)
	}
	// Within the budget, the same header is decoded, and fails only for
	// want of pixels.
	_, err = fitClaudeImages([]Image{{ContentType: "image/png", Data: pngHeader(9000, 4000, 8, 6, 0)}})
	if !errors.Is(err, errImageNotSendable) || !strings.Contains(err.Error(), "cannot be decoded here") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodedBytesAreEstimatedFromTheHeader(t *testing.T) {
	jpegOf := func(img image.Image) []byte {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, nil); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	// Go writes baseline JPEGs; the same header with SOF2 is progressive.
	progressive := func(data []byte) []byte {
		out := bytes.Clone(data)
		i := bytes.Index(out, []byte{0xff, 0xc0})
		if i < 0 {
			t.Fatal("no SOF0")
		}
		out[i+1] = 0xc2
		return out
	}
	colour := jpegOf(image.NewRGBA(image.Rect(0, 0, 100, 10)))
	grey := jpegOf(image.NewGray(image.Rect(0, 0, 100, 10)))
	cases := []struct {
		name string
		data []byte
		want uint64
	}{
		{"8-bit PNG", pngHeader(100, 10, 8, 6, 0), 4000},
		{"paletted PNG", pngChunk(pngChunk(pngHeader(100, 10, 8, 3, 0), "PLTE", make([]byte, 3*16)), "IDAT", nil), 4000},
		{"16-bit PNG", pngHeader(100, 10, 16, 6, 0), 8000},
		{"16-bit grey PNG", pngHeader(100, 10, 16, 0, 0), 8000},
		{"interlaced 8-bit PNG", pngHeader(100, 10, 8, 2, 1), 8000},
		{"baseline JPEG", colour, 4000},
		{"progressive JPEG", progressive(colour), 16000},
		{"progressive grey JPEG", progressive(grey), 8000},
	}
	for _, c := range cases {
		config, format, err := image.DecodeConfig(bytes.NewReader(c.data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := decodedBytes(c.data, format, config); got != c.want {
			t.Errorf("%s: %d bytes, want %d", c.name, got, c.want)
		}
	}
	// A size Go's PNG decoder refuses, but a decoder might not.
	absurd := image.Config{ColorModel: color.NRGBA64Model, Width: math.MaxInt32, Height: math.MaxInt32}
	if got := decodedBytes(nil, "png", absurd); got != math.MaxUint64 {
		t.Errorf("absurd image: %d bytes", got)
	}
	if jpegProgressive(colour) || !jpegProgressive(progressive(colour)) {
		t.Fatal("progressive JPEG not told apart")
	}
}

// fastPathImages are images of every type rowReader reads directly, with
// random pixels, each a sub-image so its bounds do not start at 0,0.
func fastPathImages() map[string]image.Image {
	random := rand.New(rand.NewPCG(3, 4))
	fill := func(pix []byte) {
		for i := range pix {
			pix[i] = uint8(random.UintN(256))
		}
	}
	r := image.Rect(-3, 5, 47, 36)
	sub := image.Rect(-1, 6, 45, 35)
	images := map[string]image.Image{}
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410} {
		img := image.NewYCbCr(r, ratio)
		fill(img.Y)
		fill(img.Cb)
		fill(img.Cr)
		images["YCbCr "+ratio.String()] = img.SubImage(sub)
	}
	gray := image.NewGray(r)
	fill(gray.Pix)
	images["Gray"] = gray.SubImage(sub)
	gray16 := image.NewGray16(r)
	fill(gray16.Pix)
	images["Gray16"] = gray16.SubImage(sub)
	rgba := image.NewRGBA(r)
	fill(rgba.Pix)
	// Premultiplied: no channel above alpha.
	for i := 0; i < len(rgba.Pix); i += 4 {
		a := rgba.Pix[i+3]
		rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2] = min(rgba.Pix[i], a), min(rgba.Pix[i+1], a), min(rgba.Pix[i+2], a)
	}
	images["RGBA"] = rgba.SubImage(sub)
	nrgba := image.NewNRGBA(r)
	fill(nrgba.Pix)
	images["NRGBA"] = nrgba.SubImage(sub)
	rgba64 := image.NewRGBA64(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := uint16(random.UintN(1 << 16))
			rgba64.SetRGBA64(x, y, color.RGBA64{R: uint16(random.UintN(uint(a) + 1)), G: uint16(random.UintN(uint(a) + 1)), B: uint16(random.UintN(uint(a) + 1)), A: a})
		}
	}
	images["RGBA64"] = rgba64.SubImage(sub)
	nrgba64 := image.NewNRGBA64(r)
	fill(nrgba64.Pix)
	images["NRGBA64"] = nrgba64.SubImage(sub)
	palette := color.Palette{color.White, color.NRGBA{R: 200, G: 10, B: 30, A: 128}, color.Transparent, color.Gray16{Y: 0x1234}}
	paletted := image.NewPaletted(r, palette)
	for i := range paletted.Pix {
		paletted.Pix[i] = uint8(random.UintN(uint(len(palette))))
	}
	images["Paletted"] = paletted.SubImage(sub)
	return images
}

// throughAt hides an image's type, so rowReader reads it through At.
type throughAt struct{ image.Image }

func TestFastPixelReadsMatchAt(t *testing.T) {
	for name, img := range fastPathImages() {
		bounds := img.Bounds()
		fast, generic := rowReader(img), rowReader(throughAt{img})
		fastRow, genericRow := make([]uint32, 4*bounds.Dx()), make([]uint32, 4*bounds.Dx())
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			fast(y, fastRow)
			generic(y, genericRow)
			if !slices.Equal(fastRow, genericRow) {
				t.Fatalf("%s: row %d is %v, through At %v", name, y, fastRow, genericRow)
			}
		}
		if got, want := downscale(img, 13, 7), downscale(throughAt{img}, 13, 7); !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("%s: scaled down to %v, through At %v", name, got.Pix, want.Pix)
		}
	}
}

func TestScalingDownDoesNotAllocatePerPixel(t *testing.T) {
	for name, img := range fastPathImages() {
		allocs := testing.AllocsPerRun(3, func() { downscale(img, 11, 7) })
		if allocs > 10 {
			t.Errorf("%s: %.0f allocations to scale down %d px", name, allocs, img.Bounds().Dx()*img.Bounds().Dy())
		}
	}
	// Through At, a 16-bit pixel is boxed: an allocation each.
	img := image.NewNRGBA64(image.Rect(0, 0, 64, 48))
	if allocs := testing.AllocsPerRun(3, func() { downscale(throughAt{img}, 11, 7) }); allocs < 64*48 {
		t.Fatalf("through At: %.0f allocations", allocs)
	}
}
