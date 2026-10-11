// Command documents draws the synthetic letter and receipts the AI runtime
// benchmark and the Codex actions smoke photograph-capture (P16-03): a
// Finanzamt letter with a deadline, a cash receipt (Kassenbon, BAR), and a
// card receipt (girocard). They are drawn deterministically with the Go
// fonts, large and black on white, so Codex reads them reliably, and are
// committed next to this file. Run it from this directory to draw them again:
//
//	GOWORK=off go run .
//
// The people, addresses, and numbers are made up.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// line is one line of a document: its text, its style, and how many line
// heights of space come before it.
type line struct {
	text   string
	style  string
	before float64
}

// document is a page to draw: its width, margin, and lines.
type document struct {
	file   string
	width  int
	margin int
	lines  []line
}

var documents = []document{
	{
		file: "letter-finanzamt.png", width: 1240, margin: 80,
		lines: []line{
			{text: "Finanzamt Darmstadt", style: "title"},
			{text: "Soderstraße 30 · 64283 Darmstadt", style: "small"},
			{text: "Herrn", style: "body", before: 1},
			{text: "Max Mustermann", style: "body"},
			{text: "Musterweg 1", style: "body"},
			{text: "12345 Musterstadt", style: "body"},
			{text: "Darmstadt, 02.10.2026", style: "body", before: 1},
			{text: "Steuernummer: 007/123/45678", style: "body"},
			{text: "Anforderung von Unterlagen", style: "bold", before: 1},
			{text: "Einkommensteuer 2025", style: "bold"},
			{text: "Sehr geehrter Herr Mustermann,", style: "body", before: 1},
			{text: "für die Bearbeitung Ihrer Einkommensteuer 2025", style: "body", before: 0.5},
			{text: "benötigen wir noch Belege zu Ihren Werbungskosten.", style: "body"},
			{text: "Bitte reichen Sie die Unterlagen", style: "body"},
			{text: "bis zum 30.11.2026 ein.", style: "bold"},
			{text: "Mit freundlichen Grüßen", style: "body", before: 1},
			{text: "Ihr Finanzamt Darmstadt", style: "body"},
		},
	},
	{
		file: "receipt-cash-rewe.png", width: 760, margin: 50,
		lines: []line{
			{text: "         REWE", style: "monotitle"},
			{text: "   REWE Markt GmbH", style: "mono"},
			{text: " Hauptstraße 12, Darmstadt", style: "mono"},
			{text: "                     EUR", style: "mono", before: 1},
			{text: "KAFFEE BOHNEN       8,99", style: "mono"},
			{text: "OLIVENOEL           6,49", style: "mono"},
			{text: "APFELSAFT           1,99", style: "mono"},
			{text: "REIS                1,79", style: "mono"},
			{text: "SCHOKOLADE          1,49", style: "mono"},
			{text: "ORANGEN             2,72", style: "mono"},
			{text: "------------------------", style: "mono"},
			{text: "SUMME EUR          23,47", style: "monobold"},
			{text: "------------------------", style: "mono"},
			{text: "GEGEBEN BAR        30,00", style: "mono"},
			{text: "RÜCKGELD BAR        6,53", style: "mono"},
			{text: "10.10.2026  11:42  Bon 4711", style: "mono", before: 1},
			{text: "Vielen Dank für Ihren Einkauf", style: "mono"},
		},
	},
	{
		file: "receipt-card-girocard.png", width: 760, margin: 50,
		lines: []line{
			{text: "    EDEKA Weber", style: "monotitle"},
			{text: "  Bahnhofstraße 5, Darmstadt", style: "mono"},
			{text: "     Kartenzahlung", style: "monobold", before: 1},
			{text: "       girocard", style: "monobold"},
			{text: "Terminal-ID   61234567", style: "mono", before: 1},
			{text: "Datum  09.10.2026  18:05", style: "mono"},
			{text: "Beleg-Nr.          0815", style: "mono"},
			{text: "Betrag EUR        41,20", style: "monobold", before: 1},
			{text: "   Zahlung erfolgt", style: "mono", before: 1},
			{text: "     Kundenbeleg", style: "mono"},
		},
	},
}

func main() {
	faces, err := loadFaces()
	if err != nil {
		log.Fatal(err)
	}
	for _, doc := range documents {
		if err := draw1(doc, faces); err != nil {
			log.Fatalf("%s: %v", doc.file, err)
		}
		fmt.Println("wrote", doc.file)
	}
}

// loadFaces are the styles: the Go fonts at sizes a phone photo of a letter
// or a receipt would read at.
func loadFaces() (map[string]font.Face, error) {
	styles := map[string]struct {
		ttf  []byte
		size float64
	}{
		"title": {gobold.TTF, 46}, "bold": {gobold.TTF, 36}, "body": {goregular.TTF, 36}, "small": {goregular.TTF, 28},
		"monotitle": {gomonobold.TTF, 50}, "mono": {gomono.TTF, 34}, "monobold": {gomonobold.TTF, 34},
	}
	faces := map[string]font.Face{}
	for name, style := range styles {
		parsed, err := opentype.Parse(style.ttf)
		if err != nil {
			return nil, err
		}
		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: style.size, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		faces[name] = face
	}
	return faces, nil
}

// draw1 draws one document, black on white, and writes it as a PNG.
func draw1(doc document, faces map[string]font.Face) error {
	height := doc.margin
	for _, l := range doc.lines {
		height += int((1 + l.before) * lineHeight(faces[l.style]))
	}
	height += doc.margin
	page := image.NewRGBA(image.Rect(0, 0, doc.width, height))
	draw.Draw(page, page.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	y := float64(doc.margin)
	for _, l := range doc.lines {
		face := faces[l.style]
		y += (1 + l.before) * lineHeight(face)
		drawer := font.Drawer{Dst: page, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(doc.margin, int(y-lineHeight(face)*0.3))}
		drawer.DrawString(l.text)
	}
	out, err := os.Create(doc.file)
	if err != nil {
		return err
	}
	defer out.Close()
	return (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(out, page)
}

// lineHeight is a style's line height in pixels.
func lineHeight(face font.Face) float64 {
	return float64(face.Metrics().Height.Ceil()) * 1.25
}
