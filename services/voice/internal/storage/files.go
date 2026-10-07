package storage

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
)

type Files struct{ Root string }

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
