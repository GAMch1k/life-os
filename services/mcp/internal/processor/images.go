package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Captures can carry images (photos shared to Life OS or attached in the
// composer). Inbox stores them and a capture names them in mediaReferences
// as "inbox-media:<sha256>". The runner reads them from Inbox before planning
// and Codex receives them as files with the item (CodexCLI, --image).

// Image is one image of a capture.
type Image struct {
	ContentType string
	Data        []byte
}

// CaptureImages are the images a planning run gets, and how many of the
// capture's images could not be attached (over the limits, or gone).
type CaptureImages struct {
	Images  []Image
	Omitted int
}

// Limits of one planning run: Inbox allows five images of up to 10 MiB (at
// most 20 MiB when configured larger); Codex gets at most 25 MiB in total.
const (
	maxCaptureImages     = 5
	maxImageBytes        = 20 << 20
	maxCaptureImageBytes = 25 << 20
)

const mediaReferencePrefix = "inbox-media:"

var mediaHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// imageExtensions name the files Codex receives by their content type.
var imageExtensions = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}

type imagesKey struct{}

// WithImages gives the planning runs started with ctx the capture's images.
func WithImages(ctx context.Context, images CaptureImages) context.Context {
	return context.WithValue(ctx, imagesKey{}, images)
}

func imagesFrom(ctx context.Context) CaptureImages {
	images, _ := ctx.Value(imagesKey{}).(CaptureImages)
	return images
}

// imagesNote is the prompt line for a capture with images; empty without.
func imagesNote(images CaptureImages) string {
	if len(images.Images) == 0 && images.Omitted == 0 {
		return ""
	}
	note := fmt.Sprintf("The item comes with %d attached image(s), which are part of the capture: read them together with its text. ", len(images.Images))
	if images.Omitted > 0 {
		note += fmt.Sprintf("%d more image(s) of the capture could not be attached. ", images.Omitted)
	}
	return note
}

type mediaFetcher interface {
	Fetch(ctx context.Context, references []string) (CaptureImages, error)
}

// InboxMedia reads capture images from Inbox (GET /v1/media/{sha256}) on the
// private network.
type InboxMedia struct {
	URL  string
	HTTP *http.Client
}

// Fetch reads the images a capture's media references name, in order, within
// the limits of one run. An image Inbox no longer has, or one beyond the
// limits, is left out and counted; Inbox being unreachable or failing is an
// error, so the capture is retried rather than planned without its images.
// References that are not Inbox images are ignored.
func (m InboxMedia) Fetch(ctx context.Context, references []string) (CaptureImages, error) {
	var result CaptureImages
	total := 0
	for _, reference := range references {
		hash, ok := strings.CutPrefix(reference, mediaReferencePrefix)
		if !ok || !mediaHashPattern.MatchString(hash) {
			continue
		}
		if len(result.Images) == maxCaptureImages {
			result.Omitted++
			continue
		}
		image, err := m.get(ctx, hash)
		if errors.Is(err, errImageUnusable) {
			result.Omitted++
			continue
		}
		if err != nil {
			return CaptureImages{}, fmt.Errorf("image %s: %w", hash[:12], err)
		}
		if total+len(image.Data) > maxCaptureImageBytes {
			result.Omitted++
			continue
		}
		total += len(image.Data)
		result.Images = append(result.Images, image)
	}
	return result, nil
}

// errImageUnusable marks an image that cannot be attached: gone, too large,
// or not an image Codex reads.
var errImageUnusable = errors.New("image cannot be attached")

func (m InboxMedia) get(ctx context.Context, hash string) (Image, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.URL, "/")+"/v1/media/"+hash, nil)
	if err != nil {
		return Image{}, err
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return Image{}, err
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return Image{}, errImageUnusable
	case response.StatusCode != http.StatusOK:
		return Image{}, fmt.Errorf("Inbox returned %s", response.Status)
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("content-type"), ";")[0])
	if imageExtensions[contentType] == "" {
		return Image{}, errImageUnusable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return Image{}, err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return Image{}, errImageUnusable
	}
	return Image{ContentType: contentType, Data: data}, nil
}
