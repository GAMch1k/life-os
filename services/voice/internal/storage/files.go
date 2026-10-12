package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
)

// Content types recorded on accepted jobs. They come from sniffing the
// payload; the caller-declared MIME type and file name are never trusted.
const (
	ContentTypeWAV  = "audio/wave"
	ContentTypeOgg  = "audio/ogg"
	ContentTypeWebM = "audio/webm"
	ContentTypeMP4  = "audio/mp4"
)

var (
	// ErrAudioTooLarge means the upload is empty or over the configured limit.
	ErrAudioTooLarge = errors.New("audio exceeds configured upload limit")
	// ErrUnsupportedAudio means the payload is not a recognized audio container.
	ErrUnsupportedAudio = errors.New("unsupported audio format")
)

// sniffLength is how much of an upload is inspected, as in http.DetectContentType.
const sniffLength = 512

type Files struct{ Root string }

// ValidateAudio checks the upload size and sniffs its container. It returns
// the content type to record for the job.
func ValidateAudio(input *multipart.FileHeader, maxBytes int64) (string, error) {
	if input.Size <= 0 || input.Size > maxBytes {
		return "", ErrAudioTooLarge
	}
	source, err := input.Open()
	if err != nil {
		return "", err
	}
	defer source.Close()
	buf := make([]byte, sniffLength)
	n, err := io.ReadFull(source, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if contentType := SniffAudio(buf[:n]); contentType != "" {
		return contentType, nil
	}
	return "", ErrUnsupportedAudio
}

// SniffAudio recognizes WAV and the containers browsers' MediaRecorder
// produces: WebM/Matroska (Chrome, Edge), Ogg with an audio codec (Firefox),
// and ISO BMFF/MP4 (Safari, iOS). It returns "" for anything else. Go's
// http.DetectContentType is not enough: it reports Ogg as application/ogg and
// misses MP4 files whose brands do not start with "mp4" (for example M4A).
func SniffAudio(head []byte) string {
	switch {
	case isWAV(head):
		return ContentTypeWAV
	case isOggAudio(head):
		return ContentTypeOgg
	case isWebM(head):
		return ContentTypeWebM
	case isMP4Audio(head):
		return ContentTypeMP4
	}
	return ""
}

func isWAV(head []byte) bool {
	return len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE"
}

// oggAudioCodecs are the identification packets of Ogg audio codecs. A video
// (Theora) or skeleton first stream is not accepted.
var oggAudioCodecs = [][]byte{[]byte("OpusHead"), []byte("\x01vorbis"), []byte("\x7fFLAC"), []byte("Speex   ")}

// isOggAudio checks the first Ogg page: capture pattern, version 0, the
// beginning-of-stream flag, and an audio codec's identification packet.
func isOggAudio(head []byte) bool {
	if len(head) < 27 || string(head[0:4]) != "OggS" || head[4] != 0 || head[5]&0x02 == 0 {
		return false
	}
	packet := 27 + int(head[26])
	if packet >= len(head) {
		return false
	}
	for _, codec := range oggAudioCodecs {
		if bytes.HasPrefix(head[packet:], codec) {
			return true
		}
	}
	return false
}

// isWebM checks the EBML magic and a WebM or Matroska DocType in the header.
func isWebM(head []byte) bool {
	if len(head) < 4 || !bytes.Equal(head[0:4], []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return false
	}
	header := head[:min(len(head), 64)]
	return bytes.Contains(header, []byte("webm")) || bytes.Contains(header, []byte("matroska"))
}

// mp4AudioBrands are ISO BMFF brands used by audio recordings: Safari's
// MediaRecorder (iso5/iso6/mp41 or mp42), iOS and Android M4A and 3GP files.
var mp4AudioBrands = map[string]bool{
	"M4A ": true, "M4B ": true, "mp41": true, "mp42": true, "isom": true, "dash": true,
	"iso2": true, "iso3": true, "iso4": true, "iso5": true, "iso6": true, "iso7": true, "iso8": true, "iso9": true,
	"3gp4": true, "3gp5": true, "3gp6": true, "3g2a": true,
}

// mp4ImageBrands mark HEIF/AVIF still images, which share the ftyp box.
var mp4ImageBrands = map[string]bool{
	"mif1": true, "msf1": true, "miaf": true, "heic": true, "heix": true, "hevc": true, "hevx": true, "avif": true, "avis": true,
}

// isMP4Audio checks the leading ftyp box: a sane size, a known audio brand
// (major or compatible) and no still-image brand.
func isMP4Audio(head []byte) bool {
	if len(head) < 16 || string(head[4:8]) != "ftyp" {
		return false
	}
	size := int(binary.BigEndian.Uint32(head[0:4]))
	if size < 16 || size%4 != 0 || size > len(head) {
		return false
	}
	audio := false
	for offset := 8; offset+4 <= size; offset += 4 {
		if offset == 12 { // minor version, not a brand
			continue
		}
		brand := string(head[offset : offset+4])
		if mp4ImageBrands[brand] {
			return false
		}
		audio = audio || mp4AudioBrands[brand]
	}
	return audio
}

// Digest is the hex SHA-256 of an upload's content. It tells a retried
// upload of the same recording from a different one before anything is
// stored.
func Digest(input *multipart.FileHeader) (string, error) {
	source, err := input.Open()
	if err != nil {
		return "", err
	}
	defer source.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, source); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (f Files) Store(key string, input *multipart.FileHeader) (int64, error) {
	if err := os.MkdirAll(f.Root, 0o750); err != nil {
		return 0, err
	}
	source, err := input.Open()
	if err != nil {
		return 0, err
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(f.Root, filepath.Base(key)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer target.Close()
	n, err := io.Copy(target, source)
	if err != nil {
		return 0, err
	}
	return n, nil
}
func (f Files) Path(key string) string { return filepath.Join(f.Root, filepath.Base(key)) }
func (f Files) Remove(key string) error {
	if err := os.Remove(f.Path(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove audio: %w", err)
	}
	return nil
}
