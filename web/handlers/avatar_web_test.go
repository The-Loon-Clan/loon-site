package handlers

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// The avatar pipeline: the one place the site invites a member to upload a
// photograph of themselves, and therefore the one place it accepts a file from
// somebody it has no reason to trust and then serves it to everybody else.
//
// Every function here was at 0%. The code is careful — it sniffs bytes rather
// than filenames, checks dimensions from the header before decoding, and
// re-encodes rather than passing anything through — and none of that care was
// checked by anything.

// ── helpers ─────────────────────────────────────────────────────────────

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// hugePNGHeader builds a PNG that is nothing but a signature and a valid IHDR
// claiming enormous dimensions.
//
// This is the decode bomb, and it is the reason processAvatar reads
// DecodeConfig before Decode: the file is under a hundred bytes and honestly
// declares a size whose pixel buffer would be gigabytes. A pipeline that
// decoded first and measured afterwards would allocate all of it, from a
// request that cost the sender nothing.
func hugePNGHeader(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})

	ihdr := new(bytes.Buffer)
	ihdr.WriteString("IHDR")
	_ = binary.Write(ihdr, binary.BigEndian, w)
	_ = binary.Write(ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{8, 6, 0, 0, 0}) // 8-bit RGBA, no interlace

	_ = binary.Write(&buf, binary.BigEndian, uint32(ihdr.Len()-4)) // length excludes the type
	body := ihdr.Bytes()
	buf.Write(body)
	// A real CRC, so the header is genuinely valid and the rejection cannot be
	// mistaken for "this file is corrupt".
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(body))
	return buf.Bytes()
}

// ── avatarBlobName: the traversal guard ─────────────────────────────────

func TestAvatarBlobNameRefusesAnythingOutsideTheNamespace(t *testing.T) {
	// This value comes from the DATABASE (avatar_path) and is handed to
	// blob.Remove, which joins it onto the upload root. Anything that escapes
	// that root is a delete-arbitrary-file primitive, so the interesting cases
	// are all the ways a path can leave without looking like it does.
	for _, in := range []string{
		"",
		"/etc/passwd",
		"../../etc/passwd",
		"/uploads/../../../etc/passwd",
		"/uploads/..%2f..%2fetc",
		"/uploads//etc/passwd",
		"/uploadsavatars/1.jpg", // prefix match without the separator
		"/other/avatars/1.jpg",
		"https://example.com/uploads/avatars/1.jpg",
		"uploads/avatars/1.jpg", // no leading slash
	} {
		if got := avatarBlobName(in); got != "" {
			t.Errorf("avatarBlobName(%q) = %q, want \"\" — that path leaves the namespace", in, got)
		}
	}
}

func TestAvatarBlobNameKeepsRealNames(t *testing.T) {
	// The other half: the guard must not be so strict that a real avatar can
	// never be deleted, which would leak a file on every avatar change and show
	// up only as a disk filling up.
	if got := avatarBlobName("/uploads/avatars/7-a1b2c3d4e5f6.jpg"); got != "avatars/7-a1b2c3d4e5f6.jpg" {
		t.Errorf("avatarBlobName of a real avatar = %q, want the store-relative name", got)
	}
}

// ── avatarName: never the uploaded filename ─────────────────────────────

func TestAvatarNameIsGeneratedNotTaken(t *testing.T) {
	// The uploaded filename is attacker-controlled and choosing it would let
	// somebody decide where in the namespace their file lands.
	first, err := avatarName(42)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "avatars/42-") || !strings.HasSuffix(first, ".jpg") {
		t.Errorf("avatarName(42) = %q, want avatars/42-<random>.jpg", first)
	}
	if strings.Contains(first, "..") || strings.Contains(first, "//") {
		t.Errorf("avatarName produced a traversable name: %q", first)
	}

	// A REPLACEMENT must land on a new URL, or a browser that cached the old
	// avatar keeps showing it and the member believes the upload failed.
	second, err := avatarName(42)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("two avatars for one member got the same name; a cached browser " +
			"would never show the new one")
	}
}

// ── processAvatar ───────────────────────────────────────────────────────

func TestProcessAvatarRejectsWhatIsNotAnImage(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":      {},
		"plain text": []byte("this is not an image, it is a sentence"),
		"a script":   []byte("#!/bin/sh\nrm -rf /\n"),
		"html":       []byte("<html><body><script>alert(1)</script></body></html>"),
		// A JPEG extension proves nothing: the type is sniffed from the bytes.
		"zip named .jpg": {'P', 'K', 3, 4, 0, 0, 0, 0},
	} {
		if _, err := processAvatar(raw); err == nil {
			t.Errorf("%s was accepted as an avatar", name)
		}
	}
}

func TestProcessAvatarRefusesADecodeBombFromItsHeader(t *testing.T) {
	// 30000x30000 is 900 million pixels: 3.6 GB as RGBA, declared by a file of
	// under a hundred bytes. The rejection has to come from the header, because
	// by the time a decoder has told you the size it has already allocated it.
	bomb := hugePNGHeader(t, 30000, 30000)
	if len(bomb) > 200 {
		t.Fatalf("the test's own bomb is %d bytes, which is not the point", len(bomb))
	}

	_, err := processAvatar(bomb)
	if err == nil {
		t.Fatal("a 30000x30000 image was accepted")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("rejected with %q, want the size error — being rejected as "+
			"unreadable would mean the size check never ran", err)
	}
}

func TestProcessAvatarAcceptsTheLimitAndRefusesJustPastIt(t *testing.T) {
	// The boundary, from headers alone, so neither case allocates anything.
	if _, err := processAvatar(hugePNGHeader(t, 4096, 4096)); err != nil &&
		strings.Contains(err.Error(), "too large") {
		t.Error("exactly 4096x4096 was refused as too large; the limit is inclusive")
	}
	if _, err := processAvatar(hugePNGHeader(t, 4097, 4096)); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Errorf("4097x4096 was not refused as too large: %v", err)
	}
}

func TestProcessAvatarAlwaysReturnsSquareJPEG(t *testing.T) {
	for _, in := range []struct{ w, h int }{
		{300, 300}, {800, 450}, {450, 800}, {64, 64}, {1000, 100},
	} {
		out, err := processAvatar(pngOf(t, in.w, in.h))
		if err != nil {
			t.Fatalf("%dx%d: %v", in.w, in.h, err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("%dx%d produced something undecodable: %v", in.w, in.h, err)
		}
		if format != "jpeg" {
			t.Errorf("%dx%d produced %s, want jpeg — the output format is fixed "+
				"so one <img> renders every avatar", in.w, in.h, format)
		}
		if cfg.Width != cfg.Height {
			t.Errorf("%dx%d produced %dx%d, which is not square", in.w, in.h, cfg.Width, cfg.Height)
		}
		if cfg.Width > avatarSize {
			t.Errorf("%dx%d produced %dpx, larger than the %dpx ceiling", in.w, in.h, cfg.Width, avatarSize)
		}
	}
}

func TestASmallAvatarIsNotBlownUp(t *testing.T) {
	// A 64px upload stays 64px. Upscaling would turn a small, sharp avatar into
	// a large blurry one and cost bytes to do it.
	out, err := processAvatar(pngOf(t, 64, 64))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 64 {
		t.Errorf("a 64px avatar came out at %dpx", cfg.Width)
	}
}

func TestTheUploadIsReEncodedSoMetadataCannotSurvive(t *testing.T) {
	// The privacy property. A phone photo carries EXIF, and EXIF carries GPS —
	// on the one image the site asks a member to upload of themselves. Passing
	// a valid small JPEG straight through would be the tempting optimisation
	// and would publish where the photograph was taken.
	//
	// Built as a real JPEG with an APP1/Exif segment spliced in, so this tests
	// the pipeline rather than a string.
	var body bytes.Buffer
	if err := jpeg.Encode(&body, image.NewGray(image.Rect(0, 0, 300, 300)), nil); err != nil {
		t.Fatal(err)
	}
	raw := body.Bytes()

	const marker = "GPSLatitudeSECRET"
	exif := append([]byte{0xFF, 0xE1, 0x00, 0x20}, []byte("Exif\x00\x00"+marker)...)
	withEXIF := append(append(append([]byte{}, raw[:2]...), exif...), raw[2:]...)

	if !bytes.Contains(withEXIF, []byte(marker)) {
		t.Fatal("the test's own fixture lost its marker before processing")
	}

	out, err := processAvatar(withEXIF)
	if err != nil {
		t.Fatalf("a JPEG with an EXIF segment was rejected outright: %v", err)
	}
	if bytes.Contains(out, []byte(marker)) {
		t.Error("EXIF data survived into the stored avatar — the upload was " +
			"passed through instead of being decoded to pixels and re-encoded")
	}
}

// ── the geometry, on its own ────────────────────────────────────────────

func TestSquareCropTakesTheCentre(t *testing.T) {
	// Centred, not top-left: a portrait cropped from the top loses the chin,
	// and one cropped from the left loses half the face.
	img := image.NewRGBA(image.Rect(0, 0, 100, 40))
	got := squareCrop(img).Bounds()
	if got.Dx() != 40 || got.Dy() != 40 {
		t.Fatalf("crop of 100x40 = %dx%d, want 40x40", got.Dx(), got.Dy())
	}
	if got.Min.X != 30 {
		t.Errorf("crop starts at x=%d, want x=30 (centred)", got.Min.X)
	}
}

func TestSquareCropLeavesASquareAlone(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	if got := squareCrop(img); got != image.Image(img) {
		t.Error("an already-square image was copied rather than returned as-is")
	}
}

func TestScaleSquareNeverUpscales(t *testing.T) {
	small := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if got := scaleSquare(small).Bounds().Dx(); got != 100 {
		t.Errorf("a 100px image was scaled to %dpx", got)
	}
	big := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	if got := scaleSquare(big).Bounds().Dx(); got != avatarSize {
		t.Errorf("a 1000px image scaled to %dpx, want %dpx", got, avatarSize)
	}
}
