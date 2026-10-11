package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mediaFile writes bytes to dir under their SHA-256 and returns its ref.
func mediaFile(t *testing.T, dir, content string) MediaRef {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, hash), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return MediaRef{SHA256: hash, Bytes: int64(len(content))}
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestMediaStoreCopiesOnlyWhatIsMissingAndChecksIt(t *testing.T) {
	source, root := t.TempDir(), filepath.Join(t.TempDir(), MediaDir)
	store := MediaStore{Root: root}
	letter := mediaFile(t, source, "a photo of a letter")
	receipt := mediaFile(t, source, "a photo of a receipt")
	gone := MediaRef{SHA256: hashOf("pruned meanwhile"), Bytes: 16}
	// A file whose bytes are not what its name says.
	damaged := MediaRef{SHA256: hashOf("the original"), Bytes: 12}
	if err := os.WriteFile(filepath.Join(source, damaged.SHA256), []byte("not original"), 0o640); err != nil {
		t.Fatal(err)
	}
	refs := []MediaRef{letter, receipt, gone, damaged}

	stored, copied, copiedBytes, problems, err := store.Copy(context.Background(), source, refs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored, []MediaRef{letter, receipt}) || copied != 2 || copiedBytes != letter.Bytes+receipt.Bytes {
		t.Fatalf("stored %+v, copied %d (%d bytes)", stored, copied, copiedBytes)
	}
	if len(problems) != 2 || problems[0].SHA256 != gone.SHA256 || problems[0].Problem != "not on the media volume" || !strings.Contains(problems[1].Problem, "hash to") {
		t.Fatalf("problems = %+v", problems)
	}
	path := store.Path(letter.SHA256)
	if filepath.Base(filepath.Dir(path)) != letter.SHA256[:2] {
		t.Fatalf("stored at %s", path)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored file = %v, %v", info, err)
	}
	// Files never change: the next run copies nothing again.
	stored, copied, _, _, err = store.Copy(context.Background(), source, []MediaRef{letter, receipt})
	if err != nil || copied != 0 || len(stored) != 2 {
		t.Fatalf("second copy: stored %d, copied %d, %v", len(stored), copied, err)
	}
	if files, bytes, err := store.Usage(); err != nil || files != 2 || bytes != letter.Bytes+receipt.Bytes {
		t.Fatalf("Usage = %d, %d, %v", files, bytes, err)
	}
}

func TestMediaStoreSaysWhenTheVolumeCannotBeRead(t *testing.T) {
	store := MediaStore{Root: t.TempDir()}
	if _, _, _, _, err := store.Copy(context.Background(), filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("a missing media volume was read")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	source := t.TempDir()
	ref := mediaFile(t, source, "a photo")
	if err := os.Chmod(filepath.Join(source, ref.SHA256), 0o000); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err := store.Copy(context.Background(), source, []MediaRef{ref})
	if err == nil || !strings.Contains(err.Error(), "LIFE_OS_INBOX_MEDIA_GID") {
		t.Fatalf("Copy = %v", err)
	}
	if err := CheckMediaSource(source); err == nil || !strings.Contains(err.Error(), "LIFE_OS_INBOX_MEDIA_GID") {
		t.Fatalf("CheckMediaSource = %v", err)
	}
	if err := CheckMediaSource(t.TempDir()); err != nil {
		t.Fatalf("an empty volume: %v", err)
	}
}

func TestMediaStoreCollectsOnlyUnreferencedMedia(t *testing.T) {
	source, root := t.TempDir(), t.TempDir()
	store := MediaStore{Root: root}
	kept := mediaFile(t, source, "kept")
	dropped := mediaFile(t, source, "dropped")
	if _, _, _, _, err := store.Copy(context.Background(), source, []MediaRef{kept, dropped}); err != nil {
		t.Fatal(err)
	}
	// A partial copy a crash left, and things that are not the store's.
	leftover := filepath.Join(root, kept.SHA256[:2], "."+kept.SHA256+".partial-123")
	foreign := []string{filepath.Join(root, "notes.txt"), filepath.Join(root, "xy", "file"), filepath.Join(root, kept.SHA256[:2], "readme")}
	for _, path := range append([]string{leftover}, foreign...) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.Collect(map[string]bool{kept.SHA256: true})
	if err != nil || removed != 1 {
		t.Fatalf("Collect = %d, %v", removed, err)
	}
	if _, err := os.Stat(store.Path(kept.SHA256)); err != nil {
		t.Errorf("the kept file is gone: %v", err)
	}
	for _, path := range []string{store.Path(dropped.SHA256), leftover} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived", path)
		}
	}
	for _, path := range foreign {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was touched: %v", path, err)
		}
	}
	if removed, err := (MediaStore{Root: filepath.Join(root, "missing")}).Collect(nil); err != nil || removed != 0 {
		t.Fatalf("a missing store: %d, %v", removed, err)
	}
}

func TestCheckMediaFindsMissingAndDamagedFiles(t *testing.T) {
	source, root, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	store := MediaStore{Root: root}
	refs := []MediaRef{mediaFile(t, source, "one"), mediaFile(t, source, "two"), mediaFile(t, source, "three!")}
	if _, _, _, _, err := store.Copy(context.Background(), source, refs); err != nil {
		t.Fatal(err)
	}
	if err := writeMediaList(runDir, mediaListFile("inbox"), refs); err != nil {
		t.Fatal(err)
	}
	set := &MediaSet{Service: "inbox", List: mediaListFile("inbox"), Count: 3}
	if check := store.CheckMedia(context.Background(), runDir, set, DefaultMediaHashBudget); !check.OK || check.Count != 3 || check.Hashed != 3 {
		t.Fatalf("check = %+v", check)
	}
	// A budget of 4 bytes hashes only the first file; the rest are checked
	// for presence and size.
	if check := store.CheckMedia(context.Background(), runDir, set, 4); !check.OK || check.Hashed != 1 {
		t.Fatalf("budgeted check = %+v", check)
	}
	if err := os.Remove(store.Path(refs[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(refs[1].SHA256), []byte("TWO"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(refs[2].SHA256), []byte("three"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := store.CheckMedia(context.Background(), runDir, set, DefaultMediaHashBudget)
	if check.OK || len(check.Problems) != 3 || check.Problems[0].Problem != "missing from the store" || !strings.Contains(check.Problems[1].Problem, "SHA-256") || !strings.Contains(check.Problems[2].Problem, "bytes in the store") {
		t.Fatalf("check = %+v", check)
	}
	if check := store.CheckMedia(context.Background(), runDir, &MediaSet{List: "missing.json"}, DefaultMediaHashBudget); check.OK || check.Error == "" {
		t.Fatalf("an unreadable list = %+v", check)
	}
}
