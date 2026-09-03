package qrcode

import (
	"errors"
	"fmt"
	"html"
	"strings"

	generator "github.com/skip2/go-qrcode"
)

const maxContentBytes = 512

// SVG renders a compact, self-contained QR code without scripts or external
// resources. The generated bitmap includes the standard quiet zone.
func SVG(content string) (string, error) {
	if content == "" {
		return "", errors.New("QR content is empty")
	}
	if len(content) > maxContentBytes {
		return "", fmt.Errorf("QR content exceeds %d bytes", maxContentBytes)
	}
	code, err := generator.New(content, generator.Medium)
	if err != nil {
		return "", fmt.Errorf("encode QR content: %w", err)
	}
	bitmap := code.Bitmap()
	if len(bitmap) == 0 || len(bitmap) != len(bitmap[0]) {
		return "", errors.New("QR encoder returned an invalid bitmap")
	}

	var path strings.Builder
	for y, row := range bitmap {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&path, "M%d %dh%d", start, y, x-start)
		}
	}

	size := len(bitmap)
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="%s" shape-rendering="crispEdges"><rect width="100%%" height="100%%" fill="#fff"/><path d="%s" fill="none" stroke="#000" stroke-width="1" transform="translate(0 .5)"/></svg>`, size, size, html.EscapeString(content), path.String()), nil
}
