package processor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
