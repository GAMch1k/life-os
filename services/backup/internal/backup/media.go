package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MediaDir is the shared store of media files in the backup directory:
// media/<first two hex digits>/<sha256>. A file is stored once, whatever
// number of runs refer to it.
const MediaDir = "media"

// MediaService is the service whose media the backups copy: Inbox's
// capture images, named by their SHA-256 on the inbox-media volume and
// recorded in its inbox_media table.
const MediaService = "inbox"

var mediaHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MediaRef is one media file a database refers to.
type MediaRef struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// MediaProblem is a media file that was not stored, or did not check out.
type MediaProblem struct {
	SHA256  string `json:"sha256"`
	Problem string `json:"problem"`
}

// MediaSet is what a run stored of a service's media.
type MediaSet struct {
	Service  string `json:"service"`
	Database string `json:"database"`
	// List is the file in the run directory naming every media file the run
	// stored, as MediaRefs; Count and Bytes sum it up.
	List  string `json:"list,omitempty"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
	// Copied and CopiedBytes are the files this run added to the store;
	// the others were there already.
	Copied      int   `json:"copied"`
	CopiedBytes int64 `json:"copiedBytes"`
	// Missing are files the database refers to that were not stored: not
	// on the volume, or not the bytes their name says. They are warnings,
	// not failures.
	Missing []MediaProblem `json:"missing,omitempty"`
	// Error is why no media could be stored at all; the run then fails.
	Error string `json:"error,omitempty"`
}

// mediaListFile is a run's list of the media files it stored.
func mediaListFile(service string) string { return "media-" + service + ".json" }

// readMediaRefs reads the media a database refers to in the transaction's
// snapshot: for Inbox, every row of inbox_media. A database without the
// table refers to none.
func readMediaRefs(ctx context.Context, tx pgx.Tx) ([]MediaRef, error) {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('public.inbox_media') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("find inbox_media: %w", err)
	}
	if !exists {
		return []MediaRef{}, nil
	}
	rows, err := tx.Query(ctx, "SELECT sha256, byte_size FROM public.inbox_media ORDER BY sha256")
	if err != nil {
		return nil, fmt.Errorf("list inbox_media: %w", err)
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MediaRef, error) {
		var ref MediaRef
		err := row.Scan(&ref.SHA256, &ref.Bytes)
		return ref, err
	})
	if err != nil {
		return nil, fmt.Errorf("list inbox_media: %w", err)
	}
	return refs, nil
}

// MediaStore is the backup directory's media store.
type MediaStore struct{ Root string }

// Path is where a media file is stored.
func (m MediaStore) Path(hash string) string {
	return filepath.Join(m.Root, hash[:2], hash)
}

// Copy stores each of refs from the source directory, where each file is
// named by its SHA-256, unless it is stored already. A file is written
// aside, checked against its name and size while copying, flushed, and
// renamed. Files missing from the source, or not the bytes their name
// says, are returned as problems; an error means the source cannot be
// read at all.
func (m MediaStore) Copy(ctx context.Context, source string, refs []MediaRef) (stored []MediaRef, copied int, copiedBytes int64, problems []MediaProblem, err error) {
	if _, err := os.ReadDir(source); err != nil {
		return nil, 0, 0, nil, mediaSourceError(source, err)
	}
	stored = []MediaRef{}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return nil, 0, 0, nil, ctx.Err()
		}
		if !mediaHash.MatchString(ref.SHA256) {
			problems = append(problems, MediaProblem{SHA256: ref.SHA256, Problem: "not a SHA-256 name"})
			continue
		}
		if info, err := os.Stat(m.Path(ref.SHA256)); err == nil && info.Size() == ref.Bytes {
			stored = append(stored, ref)
			continue
		}
		bytes, err := m.put(source, ref)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			problems = append(problems, MediaProblem{SHA256: ref.SHA256, Problem: "not on the media volume"})
		case errors.Is(err, fs.ErrPermission):
			return nil, 0, 0, nil, mediaSourceError(source, err)
		case err != nil:
			problems = append(problems, MediaProblem{SHA256: ref.SHA256, Problem: err.Error()})
		default:
			stored = append(stored, ref)
			copied++
			copiedBytes += bytes
		}
	}
	return stored, copied, copiedBytes, problems, nil
}

// mediaSourceError says what to do when the media volume cannot be read.
func mediaSourceError(source string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("the media volume cannot be read: %w (the backup service needs the media files' group: set LIFE_OS_INBOX_MEDIA_GID to the output of docker compose exec inbox id -g)", err)
	}
	return fmt.Errorf("the media volume %s cannot be read: %w", source, err)
}

func (m MediaStore) put(source string, ref MediaRef) (int64, error) {
	in, err := os.Open(filepath.Join(source, ref.SHA256))
	if err != nil {
		return 0, err
	}
	defer in.Close()
	dir := filepath.Dir(m.Path(ref.SHA256))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	out, err := os.CreateTemp(dir, "."+ref.SHA256+".partial-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(out.Name())
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hash), in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, fmt.Errorf("copy: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != ref.SHA256 {
		return 0, fmt.Errorf("its bytes hash to %s, not its name", got)
	}
	if written != ref.Bytes {
		return 0, fmt.Errorf("it has %d bytes, inbox_media says %d", written, ref.Bytes)
	}
	if err := os.Rename(out.Name(), m.Path(ref.SHA256)); err != nil {
		return 0, err
	}
	return written, syncDir(dir)
}

// Usage is the number and size of the stored media files.
func (m MediaStore) Usage() (files int, bytes int64, err error) {
	err = filepath.WalkDir(m.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.Type().IsRegular() && mediaHash.MatchString(entry.Name()) {
			if info, err := entry.Info(); err == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	return files, bytes, err
}

// Collect deletes the stored media files keep does not name, and files a
// crash left half-written. It touches only files under the store named
// like a SHA-256 (or their partial copies), in directories named by their
// first two hex digits.
func (m MediaStore) Collect(keep map[string]bool) (removed int, err error) {
	dirs, err := os.ReadDir(m.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, dir := range dirs {
		prefix := dir.Name()
		if !dir.IsDir() || len(prefix) != 2 || !mediaHash.MatchString(prefix+strings.Repeat("0", 62)) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(m.Root, prefix))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, file := range files {
			name := file.Name()
			if !file.Type().IsRegular() {
				continue
			}
			leftover := strings.HasPrefix(name, ".") && strings.Contains(name, ".partial-")
			unreferenced := mediaHash.MatchString(name) && strings.HasPrefix(name, prefix) && !keep[name]
			if !leftover && !unreferenced {
				continue
			}
			if err := os.Remove(filepath.Join(m.Root, prefix, name)); err != nil {
				errs = append(errs, err)
				continue
			}
			if unreferenced {
				removed++
			}
		}
	}
	return removed, errors.Join(errs...)
}

// writeMediaList writes a run's list of stored media files.
func writeMediaList(dir, name string, refs []MediaRef) error {
	return writeJSONAtomic(dir, name, refs)
}

// readMediaList reads a run's list of stored media files.
func readMediaList(dir, name string) ([]MediaRef, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("not a media list name: %q", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var refs []MediaRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return refs, nil
}

// MediaCheck is a restore check of a run's media in the store.
type MediaCheck struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"`
	// Hashed is how many were read and hashed; the others, past the check's
	// byte budget, were checked for presence and size only.
	Hashed   int            `json:"hashed"`
	Problems []MediaProblem `json:"problems"`
	Error    string         `json:"error,omitempty"`
}

// CheckMedia checks that every media file a run stored is in the store
// with its size, hashing them until hashBudget bytes have been read.
func (m MediaStore) CheckMedia(ctx context.Context, runDir string, set *MediaSet, hashBudget int64) MediaCheck {
	check := MediaCheck{Problems: []MediaProblem{}}
	refs, err := readMediaList(runDir, set.List)
	if err != nil {
		check.Error = fmt.Sprintf("the run's media list cannot be read: %v", err)
		return check
	}
	check.Count = len(refs)
	var read int64
	for _, ref := range refs {
		if ctx.Err() != nil {
			check.Error = "stopped before it finished"
			return check
		}
		path := m.Path(ref.SHA256)
		info, err := os.Stat(path)
		switch {
		case err != nil:
			check.Problems = append(check.Problems, MediaProblem{SHA256: ref.SHA256, Problem: "missing from the store"})
			continue
		case info.Size() != ref.Bytes:
			check.Problems = append(check.Problems, MediaProblem{SHA256: ref.SHA256, Problem: fmt.Sprintf("%d bytes in the store, the run says %d", info.Size(), ref.Bytes)})
			continue
		}
		if read+ref.Bytes > hashBudget {
			continue
		}
		if err := verifyFile(path, ref.Bytes, ref.SHA256); err != nil {
			check.Problems = append(check.Problems, MediaProblem{SHA256: ref.SHA256, Problem: err.Error()})
		}
		read += ref.Bytes
		check.Hashed++
	}
	check.OK = len(check.Problems) == 0
	return check
}
