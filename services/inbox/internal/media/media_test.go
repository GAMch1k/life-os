package media

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Image beginnings as cameras and browsers write them.
var (
	jpegHead = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	webpHead = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	// An ftyp box: size 24, major brand, minor version, one compatible brand.
	ftyp = func(major, compatible string) []byte {
		return append([]byte{0, 0, 0, 24}, []byte("ftyp"+major+"\x00\x00\x00\x00"+compatible+"mif1")...)
	}
)

func TestSniffAcceptsJPEGPNGAndWebPOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		head []byte
		want string
		err  error
	}{
		"jpeg":          {jpegHead, JPEG, nil},
		"png":           {pngHead, PNG, nil},
		"webp":          {webpHead, WebP, nil},
		"heic":          {ftyp("heic", "heic"), "", ErrHEIC},
		"heif":          {ftyp("mif1", "miaf"), "", ErrHEIC},
		"avif":          {ftyp("avif", "avif"), "", ErrUnsupported},
		"mp4 video":     {append([]byte{0, 0, 0, 20}, []byte("ftypisom\x00\x00\x02\x00isom")...), "", ErrUnsupported},
		"gif":           {[]byte("GIF89a\x01\x00"), "", ErrUnsupported},
		"wav":           {[]byte("RIFF\x24\x00\x00\x00WAVEfmt "), "", ErrUnsupported},
		"text":          {[]byte("hello"), "", ErrUnsupported},
		"svg":           {[]byte("<svg xmlns="), "", ErrUnsupported},
		"empty":         {nil, "", ErrUnsupported},
		"truncated png": {[]byte("\x89PN"), "", ErrUnsupported},
	} {
		got, err := Sniff(tc.head)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: Sniff = %q, %v; want %q, %v", name, got, err, tc.want, tc.err)
		}
	}
}

func TestReferencesNameImagesByTheirHash(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if Reference(hash) != "inbox-media:"+hash {
		t.Fatalf("Reference = %q", Reference(hash))
	}
	if got, ok := HashOf("inbox-media:" + hash); !ok || got != hash {
		t.Fatalf("HashOf = %q, %v", got, ok)
	}
	for _, bad := range []string{hash, "inbox-media:" + hash[:63], "inbox-media:" + "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef", "https://example.com/a.jpg", "inbox-media:../../etc/passwd"} {
		if _, ok := HashOf(bad); ok {
			t.Errorf("HashOf(%q) accepted it", bad)
		}
	}
}

func TestSaveStoresEachImageOnceByItsHash(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "media")}
	photo := append(append([]byte{}, jpegHead...), bytes.Repeat([]byte{7}, 1000)...)
	first, err := store.Save(photo, 2000)
	if err != nil || first.ContentType != JPEG || first.Size != int64(len(photo)) || !ValidHash(first.Hash) {
		t.Fatalf("Save = %+v, %v", first, err)
	}
	stored, err := os.ReadFile(store.Path(first.Hash))
	if err != nil || !bytes.Equal(stored, photo) {
		t.Fatalf("stored = %d bytes, %v", len(stored), err)
	}
	again, err := store.Save(photo, 2000)
	if err != nil || again != first {
		t.Fatalf("again = %+v, %v", again, err)
	}
	entries, _ := os.ReadDir(store.Root)
	if len(entries) != 1 {
		t.Fatalf("files = %v, want the image once and no temporary file", entries)
	}
	if _, err := store.Save(photo, int64(len(photo))-1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := store.Save(nil, 2000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := store.Save(ftyp("heic", "heic"), 2000); !errors.Is(err, ErrHEIC) {
		t.Fatalf("heic: %v", err)
	}
	if err := store.Remove(first.Hash); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(first.Hash); err != nil {
		t.Fatalf("removing a removed image: %v", err)
	}
}
