package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/inbox/internal/api/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/media"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

// OrphanMediaAge is how long an uploaded image no capture refers to is kept:
// long enough for the capture that uploads it to follow.
const OrphanMediaAge = 24 * time.Hour

// WithMedia stores capture images in files, each at most maxBytes. Without
// it the media endpoints answer 503 and captures cannot name images.
func (h *Handler) WithMedia(files media.Store, maxBytes int64) *Handler {
	h.media, h.maxImageBytes = files, maxBytes
	return h
}

func (h *Handler) mediaConfigured() bool { return h.media.Root != "" && h.maxImageBytes > 0 }

func mediaUnavailable() error {
	return httpserver.NewError(fiber.StatusServiceUnavailable, "media_unavailable", "Capture images are not configured")
}

// UploadMedia stores the images of one upload. Every image is checked before
// any is stored, so a refused upload leaves nothing behind.
func (h *Handler) UploadMedia(c fiber.Ctx) error {
	if !h.mediaConfigured() {
		return mediaUnavailable()
	}
	form, err := c.MultipartForm()
	if err != nil || len(form.File["images"]) == 0 {
		return httpserver.NewError(400, "images_required", "Send one to five images in the multipart field images")
	}
	files := form.File["images"]
	if len(files) > media.MaxPerCapture {
		return httpserver.NewError(400, "too_many_images", fmt.Sprintf("A capture can have at most %d images", media.MaxPerCapture))
	}
	uploads := make([][]byte, 0, len(files))
	for _, file := range files {
		data, err := h.readImage(file)
		if err != nil {
			return err
		}
		uploads = append(uploads, data)
	}
	result := generated.MediaList{Items: make([]generated.Media, 0, len(uploads))}
	for _, data := range uploads {
		image, err := h.media.Save(data, h.maxImageBytes)
		if err != nil {
			return imageError(err, h.maxImageBytes)
		}
		row, err := h.queries.RecordMedia(c.Context(), store.RecordMediaParams{Sha256: image.Hash, ContentType: image.ContentType, ByteSize: image.Size})
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, mediaOutput(row))
	}
	return c.Status(201).JSON(result)
}

// readImage reads one uploaded file and checks its size and format.
func (h *Handler) readImage(file *multipart.FileHeader) ([]byte, error) {
	if file.Size <= 0 || file.Size > h.maxImageBytes {
		return nil, imageError(media.ErrTooLarge, h.maxImageBytes)
	}
	source, err := file.Open()
	if err != nil {
		return nil, internal(err)
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, h.maxImageBytes+1))
	if err != nil {
		return nil, internal(err)
	}
	if int64(len(data)) > h.maxImageBytes {
		return nil, imageError(media.ErrTooLarge, h.maxImageBytes)
	}
	if _, err := media.Sniff(data[:min(len(data), 64)]); err != nil {
		return nil, imageError(err, h.maxImageBytes)
	}
	return data, nil
}

func imageError(err error, maxBytes int64) error {
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return httpserver.NewError(413, "image_too_large", fmt.Sprintf("Each image can be at most %d MB", maxBytes/(1<<20)))
	case errors.Is(err, media.ErrHEIC):
		return httpserver.NewError(400, "heic_not_supported", "HEIC photos are not supported yet: share the photo as a JPEG (for example, set the camera to Most compatible)")
	case errors.Is(err, media.ErrUnsupported):
		return httpserver.NewError(400, "unsupported_image", "Images must be JPEG, PNG, or WebP")
	}
	return internal(err)
}

// GetMedia serves an image. Its address is its content's hash, so the
// response never changes: browsers may keep it, privately.
func (h *Handler) GetMedia(c fiber.Ctx, hash string) error {
	if !media.ValidHash(hash) {
		return httpserver.NewError(400, "bad_request", "Media is named by its SHA-256 in lowercase hex")
	}
	if !h.mediaConfigured() {
		return mediaUnavailable()
	}
	row, err := h.queries.GetMedia(c.Context(), hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpserver.NewError(404, "not_found", "Media not found")
	}
	if err != nil {
		return internal(err)
	}
	data, err := os.ReadFile(h.media.Path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return httpserver.NewError(404, "not_found", "Media not found")
	}
	if err != nil {
		return internal(err)
	}
	c.Set(fiber.HeaderContentType, row.ContentType)
	c.Set(fiber.HeaderCacheControl, "private, max-age=31536000, immutable")
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set(fiber.HeaderContentDisposition, "inline")
	// Opened on its own, an image can run nothing.
	c.Set(fiber.HeaderContentSecurityPolicy, "default-src 'none'; img-src 'self'; sandbox")
	return c.Send(data)
}

// captureMedia checks a capture's media references: at most MaxPerCapture,
// each an inbox-media reference to an uploaded image. It returns them once
// each, in order, so the capture never stores null or a duplicate.
func (h *Handler) captureMedia(ctx context.Context, references *[]string) ([]string, error) {
	result := []string{}
	if references == nil || len(*references) == 0 {
		return result, nil
	}
	seen := map[string]bool{}
	hashes := []string{}
	for _, reference := range *references {
		hash, ok := media.HashOf(reference)
		if !ok {
			return nil, httpserver.NewError(400, "invalid_media_reference", fmt.Sprintf("%q is not a media reference from POST /v1/media", reference))
		}
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
			result = append(result, reference)
		}
	}
	if len(result) > media.MaxPerCapture {
		return nil, httpserver.NewError(400, "too_many_images", fmt.Sprintf("A capture can have at most %d images", media.MaxPerCapture))
	}
	rows, err := h.queries.ListMedia(ctx, hashes)
	if err != nil {
		return nil, internal(err)
	}
	if len(rows) != len(hashes) {
		return nil, httpserver.NewError(400, "unknown_media", "A media reference names no uploaded image")
	}
	return result, nil
}

// PruneMedia removes images no capture refers to once they are older than
// OrphanMediaAge: their rows and files, and files left without a row (an
// upload interrupted between the two). Media a capture refers to stays as
// long as the capture. It returns how many images it removed.
func (h *Handler) PruneMedia(ctx context.Context, now time.Time) (int, error) {
	if !h.mediaConfigured() {
		return 0, nil
	}
	before := now.Add(-OrphanMediaAge)
	removed, err := h.queries.DeleteOrphanMedia(ctx, pgtype.Timestamptz{Time: before, Valid: true})
	if err != nil {
		return 0, err
	}
	for _, hash := range removed {
		if err := h.media.Remove(hash); err != nil {
			return len(removed), err
		}
	}
	entries, err := os.ReadDir(h.media.Root)
	if errors.Is(err, os.ErrNotExist) {
		return len(removed), nil
	}
	if err != nil {
		return len(removed), err
	}
	var stray []string
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.IsDir() || !info.ModTime().Before(before) {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, ".upload-"):
			_ = os.Remove(h.media.Path(name))
		case media.ValidHash(name):
			stray = append(stray, name)
		}
	}
	if len(stray) == 0 {
		return len(removed), nil
	}
	known, err := h.queries.ListMedia(ctx, stray)
	if err != nil {
		return len(removed), err
	}
	recorded := map[string]bool{}
	for _, row := range known {
		recorded[row.Sha256] = true
	}
	count := len(removed)
	for _, hash := range stray {
		if !recorded[hash] {
			if err := h.media.Remove(hash); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func mediaOutput(row store.InboxMedium) generated.Media {
	return generated.Media{Reference: media.Reference(row.Sha256), Sha256: row.Sha256, ContentType: generated.MediaContentType(row.ContentType), ByteSize: row.ByteSize}
}
