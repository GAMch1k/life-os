// Package media stores the images attached to captures: one file per image
// on the inbox-media volume, named by the SHA-256 of its bytes, so the same
// photo shared twice is stored once. The content type comes from sniffing
// the bytes; the caller's MIME type and file name are never trusted (as for
// Voice's audio).
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Content types of accepted images.
const (
	JPEG = "image/jpeg"
	PNG  = "image/png"
	WebP = "image/webp"
)

// MaxPerCapture is how many images one upload, and one capture, may carry.
const MaxPerCapture = 5

// ReferencePrefix starts a capture's reference to one of its images:
// "inbox-media:<sha256>", read with GET /v1/media/{sha256}.
const ReferencePrefix = "inbox-media:"

var (
	// ErrTooLarge means the image is empty or over the size limit.
	ErrTooLarge = errors.New("image exceeds the upload limit")
	// ErrHEIC means a HEIC/HEIF photo, which Life OS cannot read yet.
	ErrHEIC = errors.New("HEIC images are not supported")
	// ErrUnsupported means the bytes are not a JPEG, PNG, or WebP image.
	ErrUnsupported = errors.New("unsupported image format")
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash reports whether value is a lowercase hex SHA-256.
func ValidHash(value string) bool { return hashPattern.MatchString(value) }

// Reference is the media reference a capture stores for an image.
func Reference(hash string) string { return ReferencePrefix + hash }

// HashOf returns the image a media reference names, if it is one.
func HashOf(reference string) (string, bool) {
	hash, ok := strings.CutPrefix(reference, ReferencePrefix)
	return hash, ok && ValidHash(hash)
}

// Sniff recognizes JPEG, PNG, and WebP from an image's first bytes. A HEIC
// or HEIF photo (an ISO BMFF file with an image brand, as iPhones and some
// Android cameras save) is ErrHEIC; anything else ErrUnsupported.
func Sniff(head []byte) (string, error) {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG, nil
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return PNG, nil
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return WebP, nil
	case isHEIF(head):
		return "", ErrHEIC
	}
	return "", ErrUnsupported
}

// heifBrands are the ISO BMFF brands of HEIC/HEIF still images and
// sequences. AVIF shares the container but is not HEIC: it is unsupported.
var heifBrands = map[string]bool{
	"heic": true, "heix": true, "hevc": true, "hevx": true, "heim": true, "heis": true,
	"hevm": true, "hevs": true, "mif1": true, "msf1": true,
}

// isHEIF checks the leading ftyp box for a HEIF brand (major or compatible).
func isHEIF(head []byte) bool {
	if len(head) < 16 || string(head[4:8]) != "ftyp" {
		return false
	}
	size := min(int(binary.BigEndian.Uint32(head[0:4])), len(head))
	avif := false
	heif := false
	for offset := 8; offset+4 <= size; offset += 4 {
		if offset == 12 { // minor version, not a brand
			continue
		}
		brand := string(head[offset : offset+4])
		avif = avif || brand == "avif" || brand == "avis"
		heif = heif || heifBrands[brand]
	}
	return heif && !avif
}

// Store keeps image files in Root.
type Store struct{ Root string }

// Image is one stored image.
type Image struct {
	Hash        string
	ContentType string
	Size        int64
}

// Save checks an image's size and format and stores it under its hash. The
// same bytes saved again are stored once.
func (s Store) Save(data []byte, maxBytes int64) (Image, error) {
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return Image{}, ErrTooLarge
	}
	contentType, err := Sniff(data[:min(len(data), 64)])
	if err != nil {
		return Image{}, err
	}
	sum := sha256.Sum256(data)
	image := Image{Hash: hex.EncodeToString(sum[:]), ContentType: contentType, Size: int64(len(data))}
	if _, err := os.Stat(s.Path(image.Hash)); err == nil {
		return image, nil
	}
	if err := os.MkdirAll(s.Root, 0o750); err != nil {
		return Image{}, err
	}
	// Written aside and renamed, so a reader never sees a partial file.
	temp, err := os.CreateTemp(s.Root, ".upload-*")
	if err != nil {
		return Image{}, err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return Image{}, err
	}
	if err := temp.Close(); err != nil {
		return Image{}, err
	}
	if err := os.Chmod(temp.Name(), 0o640); err != nil {
		return Image{}, err
	}
	if err := os.Rename(temp.Name(), s.Path(image.Hash)); err != nil {
		return Image{}, err
	}
	return image, nil
}

// Path is where an image's file is.
func (s Store) Path(hash string) string { return filepath.Join(s.Root, filepath.Base(hash)) }

// Remove deletes an image's file; a missing one is already removed.
func (s Store) Remove(hash string) error {
	if err := os.Remove(s.Path(hash)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove image: %w", err)
	}
	return nil
}
