package upload

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putFile — Storage-ийг тойрч диск дээр шууд файл тавина (хуучин файл, гар аргаар).
func putFile(t *testing.T, u *Uploader, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(u.cfg.Root, "uploaded", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestServeHeaders(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	putFile(t, u, "images/2026/November/a.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	putFile(t, u, "images/2026/November/a.png", pngBytes(t, 4, 4))
	putFile(t, u, "documents/2026/November/page.html", []byte("<script>alert(1)</script>"))
	putFile(t, u, "documents/2026/November/data.json", []byte("{}"))
	putFile(t, u, "documents/2026/November/r.pdf", []byte("%PDF-1.4\n"))
	putFile(t, u, "documents/2026/November/weird.bin", []byte("x"))
	putFile(t, u, "videos/2026/November/v.mp4", mp4Bytes())

	const csp = "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
	tests := []struct {
		url, ct, csp, disp string
	}{
		{"/uploaded/images/2026/November/a.svg", "image/svg+xml", csp, ""},
		{"/uploaded/images/2026/November/a.png", "image/png", "", ""},
		{"/uploaded/documents/2026/November/page.html", "text/html", csp, "attachment"},
		{"/uploaded/documents/2026/November/data.json", "application/json", "", "attachment"},
		{"/uploaded/documents/2026/November/r.pdf", "application/pdf", "", ""},
		{"/uploaded/documents/2026/November/weird.bin", "application/octet-stream", "", "attachment"},
		{"/uploaded/videos/2026/November/v.mp4", "video/mp4", "", ""},
	}
	for _, tc := range tests {
		resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if resp.StatusCode != 200 {
			t.Errorf("%s: status=%d", tc.url, resp.StatusCode)
			continue
		}
		h := resp.Header
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff алга", tc.url)
		}
		if !strings.HasPrefix(h.Get("Content-Type"), tc.ct) {
			t.Errorf("%s: content-type=%q want %q", tc.url, h.Get("Content-Type"), tc.ct)
		}
		if h.Get("Content-Security-Policy") != tc.csp {
			t.Errorf("%s: CSP=%q want %q", tc.url, h.Get("Content-Security-Policy"), tc.csp)
		}
		if d := h.Get("Content-Disposition"); (tc.disp == "") != (d == "") || !strings.HasPrefix(d, tc.disp) {
			t.Errorf("%s: disposition=%q want %q", tc.url, d, tc.disp)
		}
		if h.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: cache=%q", tc.url, h.Get("Cache-Control"))
		}
	}
}

func TestServeNotFoundStillNosniff(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, "/uploaded/images/2026/November/nope.png", nil))
	if resp.StatusCode != 404 || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestServePathTraversal(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	// Root-ийн гадна болон prefix-ийн гадна нууц файлууд.
	secret := filepath.Join(u.cfg.Root, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	putFile(t, u, "images/2026/November/ok.png", pngBytes(t, 2, 2))
	putFile(t, u, "images/.hidden.png", pngBytes(t, 2, 2))
	putFile(t, u, "other/x.txt", []byte("not a category"))
	putFile(t, u, "sharedlib/lib.so", []byte("\x7fELF"))
	putFile(t, u, "images/2026/November/.khan-tmp-abc", []byte("partial"))

	paths := []string{
		"/uploaded/../secret.txt",
		"/uploaded/images/../../secret.txt",
		"/uploaded/%2e%2e/secret.txt",
		"/uploaded/%2E%2E/secret.txt",
		"/uploaded/images/%2e%2e/%2e%2e/secret.txt",
		"/uploaded/images/..%2f..%2fsecret.txt",
		"/uploaded/images/%2e%2e%2f%2e%2e%2fsecret.txt",
		"/uploaded/images/2026/..%5c..%5c..%5csecret.txt",
		"/uploaded/images/2026/November/ok.png%00.jpg",
		"/uploaded/images/2026/November/ok.png%00",
		"/uploaded//etc/passwd",
		"/uploaded/%2Fetc/passwd",
		"/uploaded/images//2026/November/ok.png",
		"/uploaded/images/2026/November/",
		"/uploaded/images/2026",
		"/uploaded/images",
		"/uploaded/",
		"/uploaded/images/.hidden.png",
		"/uploaded/images/2026/November/.khan-tmp-abc",
		"/uploaded/other/x.txt",
		"/uploaded/sharedlib/lib.so",
		"/uploaded/images/%252e%252e/secret.txt",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, "http://x"+p, nil)
		resp, body := do(t, app, req)
		if resp.StatusCode == 200 || strings.Contains(body, "TOP SECRET") {
			t.Errorf("%q: status=%d body=%q", p, resp.StatusCode, body)
		}
	}
	// Контроль: зөв зам ажиллана.
	resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, "/uploaded/images/2026/November/ok.png", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("зөв зам status=%d", resp.StatusCode)
	}
}

func TestServeSymlinkEscape(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.png"), pngBytes(t, 2, 2), 0o644)
	base := filepath.Join(u.cfg.Root, "uploaded", "images")
	_ = os.MkdirAll(base, 0o755)
	if err := os.Symlink(outside, filepath.Join(base, "evil")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(base, "leak.png")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/uploaded/images/evil/secret.png", "/uploaded/images/leak.png"} {
		if resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, p, nil)); resp.StatusCode == 200 {
			t.Errorf("%s: symlink-ээр root-оос гарлаа", p)
		}
	}
}

func TestServeThumbFallback(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, true)
	res, err := store(u, "pic.png", "image/png", pngBytes(t, 900, 900))
	if err != nil || res.Thumb == "" {
		t.Fatalf("%v %+v", err, res)
	}
	_, orig := do(t, app, httptest.NewRequest(http.MethodGet, res.Path, nil))
	resp, thumb := do(t, app, httptest.NewRequest(http.MethodGet, res.Thumb, nil))
	if resp.StatusCode != 200 || len(thumb) == 0 || len(thumb) >= len(orig) {
		t.Fatalf("thumb status=%d len=%d orig=%d", resp.StatusCode, len(thumb), len(orig))
	}
	// ?thumb=true → thumb_ хувилбар.
	_, viaQuery := do(t, app, httptest.NewRequest(http.MethodGet, res.Path+"?thumb=true", nil))
	if viaQuery != thumb {
		t.Fatal("?thumb=true thumbnail өгөөгүй")
	}
	// thumb_ устсан бол (хуучин файл) эх файл.
	if err := os.Remove(filepath.Join(u.cfg.Root, filepath.FromSlash(res.Thumb))); err != nil {
		t.Fatal(err)
	}
	resp, fb := do(t, app, httptest.NewRequest(http.MethodGet, res.Thumb, nil))
	if resp.StatusCode != 200 || fb != orig {
		t.Fatalf("fallback status=%d same=%v", resp.StatusCode, fb == orig)
	}
	_, fb2 := do(t, app, httptest.NewRequest(http.MethodGet, res.Path+"?thumb=true", nil))
	if fb2 != orig {
		t.Fatal("?thumb=true fallback эх файл биш")
	}
	// Эх файл ч байхгүй бол 404.
	_ = os.Remove(filepath.Join(u.cfg.Root, filepath.FromSlash(res.Path)))
	if resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, res.Thumb, nil)); resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestServeRange(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	data := []byte("%PDF-1.4 0123456789")
	putFile(t, u, "documents/2026/November/r.pdf", data)
	url := "/uploaded/documents/2026/November/r.pdf"

	rng := func(h string) (*http.Response, string) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if h != "" {
			req.Header.Set("Range", h)
		}
		return do(t, app, req)
	}
	resp, body := rng("bytes=2-5")
	if resp.StatusCode != 206 || body != string(data[2:6]) || resp.Header.Get("Content-Range") != "bytes 2-5/19" {
		t.Fatalf("206: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("206 толгой дутуу")
	}
	if resp, body = rng("bytes=15-"); resp.StatusCode != 206 || body != string(data[15:]) {
		t.Fatalf("open-ended: %d %q", resp.StatusCode, body)
	}
	if resp, body = rng("bytes=-4"); resp.StatusCode != 206 || body != string(data[15:]) {
		t.Fatalf("suffix: %d %q", resp.StatusCode, body)
	}
	if resp, body = rng("bytes=0-9999"); resp.StatusCode != 206 || body != string(data) {
		t.Fatalf("clamped: %d %q", resp.StatusCode, body)
	}
	if resp, _ = rng("bytes=100-200"); resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */19" {
		t.Fatalf("416: %d %q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	// Олон range / буруу range ⇒ бүтэн 200.
	for _, h := range []string{"bytes=0-1,3-4", "bytes=5-2", "items=0-1", "bytes=a-b", "bytes="} {
		if resp, body = rng(h); resp.StatusCode != 200 || body != string(data) {
			t.Fatalf("%q: %d %q", h, resp.StatusCode, body)
		}
	}
	if resp, body = rng(""); resp.StatusCode != 200 || body != string(data) {
		t.Fatalf("full: %d", resp.StatusCode)
	}
}

func TestServeConditionalAndHead(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	putFile(t, u, "documents/2026/November/r.pdf", []byte("%PDF-1.4 hello"))
	url := "/uploaded/documents/2026/November/r.pdf"

	resp, _ := do(t, app, httptest.NewRequest(http.MethodGet, url, nil))
	etag := resp.Header.Get("ETag")
	if etag == "" || resp.Header.Get("Last-Modified") == "" {
		t.Fatal("ETag/Last-Modified алга")
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-None-Match", etag)
	if resp, _ = do(t, app, req); resp.StatusCode != 304 {
		t.Fatalf("304 хүлээсэн: %d", resp.StatusCode)
	}
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-None-Match", `W/"other"`)
	if resp, _ = do(t, app, req); resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	resp, body := do(t, app, httptest.NewRequest(http.MethodHead, url, nil))
	if resp.StatusCode != 200 || body != "" || resp.Header.Get("Content-Length") != "14" {
		t.Fatalf("HEAD: %d %q cl=%q", resp.StatusCode, body, resp.Header.Get("Content-Length"))
	}
	resp, _ = do(t, app, httptest.NewRequest(http.MethodPost, url, nil))
	if resp.StatusCode == 200 {
		t.Fatal("POST зөвшөөрөгдсөн")
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		h             string
		size          int64
		start, length int64
		st            rangeState
	}{
		{"bytes=0-0", 10, 0, 1, rangeOK},
		{"bytes=0-", 10, 0, 10, rangeOK},
		{"bytes=9-", 10, 9, 1, rangeOK},
		{"bytes=10-", 10, 0, 0, rangeUnsatisfiable},
		{"bytes=-3", 10, 7, 3, rangeOK},
		{"bytes=-30", 10, 0, 10, rangeOK},
		{"bytes=-0", 10, 0, 0, rangeUnsatisfiable},
		{"BYTES=1-2", 10, 1, 2, rangeOK},
		{"bytes=2-1", 10, 0, 0, rangeIgnore},
		{"bytes=-", 10, 0, 0, rangeIgnore},
		{"bytes=--1", 10, 0, 0, rangeIgnore},
		{"bytes=99999999999999999999-", 10, 0, 0, rangeIgnore},
		{"bytes=0-1,2-3", 10, 0, 0, rangeIgnore},
	}
	for _, tc := range tests {
		s, l, st := parseRange(tc.h, tc.size)
		if st != tc.st || (st == rangeOK && (s != tc.start || l != tc.length)) {
			t.Errorf("%q: (%d,%d,%d) want (%d,%d,%d)", tc.h, s, l, st, tc.start, tc.length, tc.st)
		}
	}
}
