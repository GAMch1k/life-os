package storage

import (
	"bytes"
	"encoding/hex"
	"errors"
	"mime/multipart"
	"strings"
	"testing"
)

// Leading bytes of real recordings. The browser headers come from Chromium's
// and Firefox's MediaRecorder; the others from ffmpeg (its fragmented MP4 has
// the layout Safari's MediaRecorder writes).
var (
	chromeWebM  = mustHex("1a45dfa39f4286810142f7810142f2810442f381084282847765626d4287810442858102185380670100000000008eb1114d9b74b94dbb8b53ab841549a96653")
	firefoxOgg  = mustHex("4f67675300020000000000000000684bd26500000000c56902ed01134f707573486561640101380144ac00000000004f67675300000000000000000000684bd2")
	chromeMP4   = mustHex("000000246674797069736f6d0000020069736f6d69736f3669736f32767030396d7034310000025d6d6f6f76000000786d7668640100000000000000e6ef393f")
	ffmpegWebM  = mustHex("1a45dfa39f4286810142f7810142f2810442f381084282847765626d428781044285810218538067010000000000214b")
	ffmpegOgg   = mustHex("4f676753000200000000000000000000000000000000492af6a001134f7075734865616401013801803e00000000004f")
	ffmpegMP4   = mustHex("0000001c6674797069736f350000020069736f3569736f366d703431000002986d6f6f760000006c6d76686400000000")
	m4aFile     = append(box("ftyp", "M4A ", "\x00\x00\x02\x00", "M4A ", "isom", "iso2"), box("free")...)
	threeGPFile = append(box("ftyp", "3gp4", "\x00\x00\x00\x00", "isom", "3gp4"), box("free")...)
	wavFile     = append([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00"), make([]byte, 64)...)
	heicImage   = append(box("ftyp", "heic", "\x00\x00\x00\x00", "mif1", "heic"), box("meta")...)
	avifImage   = append(box("ftyp", "avif", "\x00\x00\x00\x00", "avif", "mif1", "miaf", "iso8"), box("meta")...)
	quickTime   = append(box("ftyp", "qt  ", "\x00\x00\x02\x00", "qt  "), box("wide")...)
)

func TestSniffAudioAcceptsBrowserRecordingContainers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		head []byte
		want string
	}{
		"Chromium WebM/Opus": {chromeWebM, ContentTypeWebM},
		"ffmpeg WebM/Opus":   {ffmpegWebM, ContentTypeWebM},
		"Matroska":           {[]byte("\x1a\x45\xdf\xa3\x01\x00\x00\x00\x00\x00\x00\x23\x42\x86\x81\x01\x42\x82\x88matroska"), ContentTypeWebM},
		"Firefox Ogg/Opus":   {firefoxOgg, ContentTypeOgg},
		"ffmpeg Ogg/Opus":    {ffmpegOgg, ContentTypeOgg},
		"Ogg/Vorbis":         {oggPage("\x01vorbis\x00\x00\x00\x00"), ContentTypeOgg},
		"Ogg/FLAC":           {oggPage("\x7fFLAC\x01\x00"), ContentTypeOgg},
		"fragmented MP4/AAC": {ffmpegMP4, ContentTypeMP4},
		"Chromium MP4/Opus":  {chromeMP4, ContentTypeMP4},
		"M4A":                {m4aFile, ContentTypeMP4},
		"3GP":                {threeGPFile, ContentTypeMP4},
		"WAV":                {wavFile, ContentTypeWAV},
	} {
		if got := SniffAudio(tc.head); got != tc.want {
			t.Errorf("%s: SniffAudio() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestSniffAudioRejectsNonAudio(t *testing.T) {
	t.Parallel()
	for name, head := range map[string][]byte{
		"empty":           nil,
		"text":            []byte("I already bought the chicken."),
		"PNG":             mustHex("89504e470d0a1a0a0000000d49484452"),
		"PDF":             []byte("%PDF-1.7\n"),
		"AVI":             []byte("RIFF\x24\x00\x00\x00AVI LIST"),
		"Ogg/Theora":      oggPage("\x80theora\x03\x02"),
		"Ogg without BOS": func() []byte { page := oggPage("OpusHead\x01"); page[5] = 0; return page }(),
		"truncated Ogg":   []byte("OggS\x00\x02\x00"),
		"EBML other doc":  []byte("\x1a\x45\xdf\xa3\x93\x42\x82\x88somedoc!"),
		"HEIC image":      heicImage,
		"AVIF image":      avifImage,
		"QuickTime":       quickTime,
		"bad ftyp size":   append([]byte("\x00\x00\x00\x13ftypisom\x00\x00\x00\x00isom"), make([]byte, 16)...),
		"HTML":            []byte("<!doctype html><title>x</title>"),
	} {
		if got := SniffAudio(head); got != "" {
			t.Errorf("%s: SniffAudio() = %q, want rejection", name, got)
		}
	}
}

func TestValidateAudioEnforcesSizeAndSniffsContent(t *testing.T) {
	t.Parallel()
	got, err := ValidateAudio(fileHeader(t, "recording.bin", firefoxOgg), 1024)
	if err != nil || got != ContentTypeOgg {
		t.Fatalf("Ogg upload: got %q, %v", got, err)
	}
	// The declared name and type do not matter, only the bytes do.
	if _, err := ValidateAudio(fileHeader(t, "voice.webm", []byte("not audio at all")), 1024); !errors.Is(err, ErrUnsupportedAudio) {
		t.Fatalf("text named .webm: err = %v, want ErrUnsupportedAudio", err)
	}
	if _, err := ValidateAudio(fileHeader(t, "big.wav", append(wavFile, make([]byte, 1024)...)), 1024); !errors.Is(err, ErrAudioTooLarge) {
		t.Fatalf("oversized upload: err = %v, want ErrAudioTooLarge", err)
	}
	// Shorter than the sniff window is fine as long as the header is complete.
	if got, err := ValidateAudio(fileHeader(t, "short.m4a", ffmpegMP4), 1024); err != nil || got != ContentTypeMP4 {
		t.Fatalf("short MP4 upload: got %q, %v", got, err)
	}
}

// fileHeader parses a one-file multipart form, as Fiber does for an upload.
func fileHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	return form.File["audio"][0]
}

// box builds an ISO BMFF box whose payload is the concatenated fields.
func box(kind string, fields ...string) []byte {
	payload := strings.Join(fields, "")
	size := 8 + len(payload)
	return append([]byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}, append([]byte(kind), payload...)...)
}

// oggPage builds a beginning-of-stream Ogg page holding one packet.
func oggPage(packet string) []byte {
	header := []byte("OggS\x00\x02")
	header = append(header, make([]byte, 20)...) // granule, serial, sequence, CRC
	header = append(header, 1, byte(len(packet)))
	return append(header, packet...)
}

func mustHex(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}
