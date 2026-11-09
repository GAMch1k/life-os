package processor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG captures are decoded to make them smaller.
)

// Claude's limits for one image of a request: its base64 data at most 5 MB
// and at most 8000 px on a side. Inbox accepts larger photos (up to 10 MiB by
// default), so a capture's image over a limit is made smaller before a Claude
// Code run: scaled to at most claudeImageLongEdge on the long edge and
// re-encoded as JPEG. Claude reads at most 2576 px on the long edge (Opus 4.7
// and Sonnet 5 on; earlier models 1568 px) and scales a larger image itself,
// so this loses nothing it would have read. Images within the limits are sent
// as they are.
const (
	// claudeMaxImageBytes is the most an image may have before base64: 5 MiB
	// of base64 data.
	claudeMaxImageBytes = (5 << 20) / 4 * 3
	claudeMaxImageSide  = 8000
	claudeImageLongEdge = 2576
	// claudeMinImageLongEdge is as small as an image is made before giving up.
	claudeMinImageLongEdge = 768
	// claudeMaxDecodePixels bounds what is decoded (about 75 MB for a phone
	// JPEG of this size): a 48 MP photo fits, a decompression bomb does not.
	claudeMaxDecodePixels = 50_000_000
)

// claudeImageQualities are the JPEG qualities tried, best first, before the
// image is scaled down further.
var claudeImageQualities = []int{85, 70, 55}

// errImageNotSendable marks a capture image that is over Claude's limits and
// cannot be made to fit: a format Go cannot decode (WebP), a damaged file, or
// too many pixels to decode. Retrying cannot help, so the capture goes to
// Human Review.
var errImageNotSendable = errors.New("capture image cannot be sent")

// imageNotSendable says which image could not be sent, and why.
type imageNotSendable struct {
	index, count int
	reason       string
}

func (e imageNotSendable) Error() string {
	return fmt.Sprintf("image %d of %d of the capture is over Claude's limits (5 MB, 8000 px a side) and could not be made smaller: %s", e.index, e.count, e.reason)
}

func (imageNotSendable) Is(target error) bool { return target == errImageNotSendable }

// fitClaudeImages returns the capture's images within Claude's limits: each
// one within them unchanged, each one over them scaled down and re-encoded as
// JPEG. An image that cannot be made to fit is an imageNotSendable error.
func fitClaudeImages(images []Image) ([]Image, error) {
	if len(images) == 0 {
		return images, nil
	}
	fitted := make([]Image, len(images))
	for i, img := range images {
		fit, err := fitClaudeImage(img)
		if err != nil {
			return nil, imageNotSendable{index: i + 1, count: len(images), reason: err.Error()}
		}
		fitted[i] = fit
	}
	return fitted, nil
}

// fitClaudeImage is one image within Claude's limits.
func fitClaudeImage(img Image) (Image, error) {
	config, _, configErr := image.DecodeConfig(bytes.NewReader(img.Data))
	if len(img.Data) <= claudeMaxImageBytes && (configErr != nil || (config.Width <= claudeMaxImageSide && config.Height <= claudeMaxImageSide)) {
		// Within the limits, or small enough and in a format only Claude
		// reads (WebP): sent as it is.
		return img, nil
	}
	size := fmt.Sprintf("%s, %.1f MB", img.ContentType, float64(len(img.Data))/(1<<20))
	if configErr != nil {
		return Image{}, fmt.Errorf("%s that cannot be decoded here (%v)", size, configErr)
	}
	if config.Width*config.Height > claudeMaxDecodePixels {
		return Image{}, fmt.Errorf("%s of %d×%d px, too many pixels to decode", size, config.Width, config.Height)
	}
	source, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		return Image{}, fmt.Errorf("%s that cannot be decoded here (%v)", size, err)
	}
	orientation := 1
	if img.ContentType == "image/jpeg" {
		orientation = jpegOrientation(img.Data)
	}
	width, height := scaledSize(source.Bounds().Dx(), source.Bounds().Dy(), claudeImageLongEdge)
	for {
		// The photo as it is shown: JPEG re-encoding drops its EXIF
		// orientation, so the pixels are turned instead.
		scaled := orient(downscale(source, width, height), orientation)
		for _, quality := range claudeImageQualities {
			var out bytes.Buffer
			if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: quality}); err != nil {
				return Image{}, fmt.Errorf("re-encode %s: %v", size, err)
			}
			if out.Len() <= claudeMaxImageBytes {
				return Image{ContentType: "image/jpeg", Data: out.Bytes()}, nil
			}
		}
		if max(width, height)*3/4 < claudeMinImageLongEdge {
			return Image{}, fmt.Errorf("%s still over 5 MB at %d×%d px", size, width, height)
		}
		width, height = max(1, width*3/4), max(1, height*3/4)
	}
}

// scaledSize fits width×height within longEdge on its long edge, keeping the
// aspect ratio; a smaller image keeps its size.
func scaledSize(width, height, longEdge int) (int, int) {
	if width <= longEdge && height <= longEdge {
		return width, height
	}
	if width >= height {
		return longEdge, max(1, (height*longEdge+width/2)/width)
	}
	return max(1, (width*longEdge+height/2)/height), longEdge
}

// downscale scales src to width×height by averaging the source pixels each
// target pixel covers (a box filter, which suits shrinking). Transparency is
// flattened onto white, since JPEG has none.
func downscale(src image.Image, width, height int) *image.RGBA {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	at := pixelReader(src)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0, y1 := span(y, height, sh)
		for x := 0; x < width; x++ {
			x0, x1 := span(x, width, sw)
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := at(bounds.Min.X+sx, bounds.Min.Y+sy)
					r, g, b, a = r+uint64(pr), g+uint64(pg), b+uint64(pb), a+uint64(pa)
					n++
				}
			}
			// Premultiplied colour over white: c + (1 - alpha) * white.
			white := n*0xffff - a
			offset := dst.PixOffset(x, y)
			dst.Pix[offset] = uint8((r + white) / n >> 8)
			dst.Pix[offset+1] = uint8((g + white) / n >> 8)
			dst.Pix[offset+2] = uint8((b + white) / n >> 8)
			dst.Pix[offset+3] = 0xff
		}
	}
	return dst
}

// span is the source range [from, to) target index i of n covers in size.
func span(i, n, size int) (int, int) {
	from, to := i*size/n, (i+1)*size/n
	if to <= from {
		to = from + 1
	}
	return from, to
}

// pixelReader reads src's pixels as premultiplied 16-bit RGBA, directly for
// the image types JPEG and PNG decode to most often.
func pixelReader(src image.Image) func(x, y int) (r, g, b, a uint32) {
	switch img := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			c := img.YCbCrAt(x, y)
			r, g, b := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			return uint32(r) * 0x101, uint32(g) * 0x101, uint32(b) * 0x101, 0xffff
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			c := img.RGBAAt(x, y)
			return uint32(c.R) * 0x101, uint32(c.G) * 0x101, uint32(c.B) * 0x101, uint32(c.A) * 0x101
		}
	default:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return src.At(x, y).RGBA() }
	}
}

// orient turns img as EXIF orientation 2 to 8 says the photo is shown; 1 or
// an unknown value leaves it as it is.
func orient(img *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2: // mirrored
				dx, dy = w-1-x, y
			case 3: // upside down
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored upside down
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // turned a quarter clockwise to show
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // turned a quarter counter-clockwise to show
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dst.PixOffset(dx, dy):dst.PixOffset(dx, dy)+4], img.Pix[img.PixOffset(x, y):img.PixOffset(x, y)+4])
		}
	}
	return dst
}

// jpegOrientation is a JPEG's EXIF orientation (1 to 8), or 1 when it has
// none or it cannot be read.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 1
		}
		marker := data[i+1]
		if marker == 0xff {
			i++
			continue
		}
		if marker == 0xda || marker == 0xd9 {
			// Image data starts (or ends): no EXIF before it.
			return 1
		}
		length := int(binary.BigEndian.Uint16(data[i+2:]))
		if length < 2 || i+2+length > len(data) {
			return 1
		}
		segment := data[i+4 : i+2+length]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return exifOrientation(segment[6:])
		}
		i += 2 + length
	}
	return 1
}

// exifOrientation reads tag 0x0112 from the first IFD of EXIF TIFF data.
func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:]) != 42 {
		return 1
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	entries := int(order.Uint16(tiff[ifd:]))
	for e := 0; e < entries; e++ {
		entry := ifd + 2 + e*12
		if entry+12 > len(tiff) {
			return 1
		}
		// A SHORT value sits in the first two bytes of the value field.
		if order.Uint16(tiff[entry:]) == 0x0112 && order.Uint16(tiff[entry+2:]) == 3 {
			if value := int(order.Uint16(tiff[entry+8:])); value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}
	return 1
}
