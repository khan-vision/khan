package upload

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/tenant"
)

var testNow = time.Date(2026, time.November, 6, 10, 0, 0, 0, time.UTC)

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return img
}

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifBytes(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, testImage(4, 4), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeaderOnly — IHDR-ээр w*h зарласан (decode хийвэл асар их санах ой) PNG.
func pngHeaderOnly(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	b.Write(l[:])
	b.WriteString("IHDR")
	b.Write(ihdr)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	b.Write(c[:])
	return b.Bytes()
}

func webpBytes() []byte {
	// VP8L: 0x2f + (w-1)|(h-1)<<14 → 2x2
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{0x16, 0, 0, 0})
	b.WriteString("WEBPVP8L")
	b.Write([]byte{0x0a, 0, 0, 0})
	b.WriteByte(0x2f)
	v := uint32(1) | uint32(1)<<14
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	b.Write(x[:])
	b.Write(make([]byte, 6))
	return b.Bytes()
}

func zipBytes(t testing.TB, files ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, f := range files {
		w, err := zw.Create(f)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("<x/>"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func mp4Bytes() []byte {
	return append([]byte{0, 0, 0, 0x18}, []byte("ftypisom\x00\x00\x02\x00isomiso2")...)
}

func newTestUploader(t testing.TB, mut ...func(*Config)) *Uploader {
	t.Helper()
	cfg := Config{
		Root:      t.TempDir(),
		MaxBytes:  5 << 20,
		Thumbnail: Thumbnail{Enabled: true},
		Now:       func() time.Time { return testNow },
	}
	for _, m := range mut {
		m(&cfg)
	}
	u, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func store(u *Uploader, name, ct string, data []byte) (Result, error) {
	return u.Store(context.Background(), name, ct, bytes.NewReader(data), int64(len(data)))
}

// newApp — Handler-ийг fake JWT-тэй, Serve-ийг auth-гүй суулгасан app.
func newApp(t testing.TB, u *Uploader, withAuth bool) *fiber.App {
	t.Helper()
	app := fiber.New()
	if withAuth {
		app.Use(func(c fiber.Ctx) error {
			c.Locals(tenant.LocalsKey, &jwt.Token{Claims: jwt.MapClaims{"id": "7", "org_id": "org-1"}})
			return c.Next()
		})
	}
	u.Mount(app.Group("/api/upload"), nil)
	u.MountServe(app)
	return app
}

func multipartReq(t testing.TB, field, filename, contentType string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if field != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
		h.Set("Content-Type", contentType)
		p, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = p.Write(data)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func do(t testing.TB, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}
