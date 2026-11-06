package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestStoreTypeDetection(t *testing.T) {
	good := map[string]struct {
		name string
		data []byte
	}{
		"png":     {"a.png", pngBytes(t, 8, 8)},
		"jpg":     {"a.jpg", jpegBytes(t, 8, 8)},
		"jpeg":    {"A.JPEG", jpegBytes(t, 8, 8)},
		"gif":     {"a.gif", gifBytes(t)},
		"webp":    {"a.webp", webpBytes()},
		"pdf":     {"a.pdf", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")},
		"doc":     {"a.doc", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...)},
		"xls":     {"a.xls", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...)},
		"docx":    {"a.docx", zipBytes(t, "[Content_Types].xml", "word/document.xml")},
		"xlsx":    {"a.xlsx", zipBytes(t, "[Content_Types].xml", "xl/workbook.xml")},
		"pptx":    {"a.pptx", zipBytes(t, "[Content_Types].xml", "ppt/presentation.xml")},
		"zip":     {"a.zip", zipBytes(t, "x.txt")},
		"csv":     {"a.csv", []byte("id,нэр\n1,Бат\n")},
		"csv1251": {"b.csv", []byte{0xC1, 0xE0, 0xF2, ',', 0xC1, 0xFD, '\n'}}, // CP1251
		"txt":     {"a.txt", []byte("сайн байна уу\n")},
		"json":    {"a.json", []byte("\xEF\xBB\xBF{\"a\":[1,2,{\"b\":null}]}")},
		"mp4":     {"a.mp4", mp4Bytes()},
		"m4v":     {"a.m4v", mp4Bytes()},
		"webm":    {"a.webm", append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 32)...)},
		"mp3":     {"a.mp3", append([]byte("ID3\x03\x00"), make([]byte, 32)...)},
		"mp3s":    {"b.mp3", append([]byte{0xFF, 0xFB, 0x90, 0x00}, make([]byte, 32)...)},
		"wav":     {"a.wav", append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...)},
		"aac":     {"a.aac", append([]byte{0xFF, 0xF1, 0x50, 0x80}, make([]byte, 32)...)},
		"m4a":     {"a.m4a", mp4Bytes()},
	}
	for name, tc := range good {
		t.Run("ok/"+name, func(t *testing.T) {
			u := newTestUploader(t)
			res, err := store(u, tc.name, "application/octet-stream", tc.data)
			if err != nil {
				t.Fatalf("амжилттай байх ёстой: %v", err)
			}
			if !strings.HasPrefix(res.Path, "/uploaded/") || !strings.Contains(res.Path, "/2026/11/") {
				t.Fatalf("path=%q", res.Path)
			}
		})
	}

	// Өргөтгөл буруу raster зураг → жинхэнэ өргөтгөлөөр хадгалагдана (татгалзахгүй).
	t.Run("png data as jpg stored as png", func(t *testing.T) {
		u := newTestUploader(t)
		res, err := store(u, "photo.jpg", "image/jpeg", pngBytes(t, 4, 4))
		if err != nil {
			t.Fatalf("амжилттай байх ёстой: %v", err)
		}
		if !regexp.MustCompile(`/photo-[0-9a-f]{16}\.png$`).MatchString(res.Path) {
			t.Fatalf("path=%q, .png хүлээсэн", res.Path)
		}
	})

	bad := []struct {
		name   string
		file   string
		data   []byte
		status int
	}{
		{"exe as jpg", "evil.jpg", []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff"), 400},
		{"html as png", "x.png", []byte("<html><script>alert(1)</script></html>"), 400},
		{"html as txt", "x.txt", []byte("<!DOCTYPE html><html><body>hi</body></html>"), 400},
		{"html as csv", "x.csv", []byte("<script>alert(1)</script>"), 400},
		{"nul in txt", "x.txt", []byte("abc\x00def"), 400},
		{"gif header + script polyglot", "x.gif", []byte("GIF89a<script>alert(1)</script>"), 400},
		{"png magic only", "x.png", []byte("\x89PNG\r\n\x1a\ngarbage"), 400},
		{"pdf wrong", "x.pdf", []byte("hello world"), 400},
		{"docx without word part", "x.docx", zipBytes(t, "[Content_Types].xml", "x.xml"), 400},
		{"docx not a zip", "x.docx", []byte("PK\x03\x04junk"), 400},
		{"doc without OLE", "x.doc", []byte("plain text"), 400},
		{"json invalid", "x.json", []byte(`{"a":`), 400},
		{"json trailing garbage", "x.json", []byte(`{"a":1} <script>`), 400},
		{"mp4 wrong", "x.mp4", []byte("not a video at all"), 400},
		{"mp3 wrong", "x.mp3", []byte("not audio at all!"), 400},
		{"empty", "x.png", nil, 400},
		{"pixel bomb", "x.png", pngHeaderOnly(10000, 10000), 400},
		{"php", "x.php", []byte("<?php system($_GET[0]);"), 415},
		{"php.jpg double ext content php", "x.php.jpg", []byte("<?php system($_GET[0]);"), 400},
		{"html", "x.html", []byte("<html>"), 415},
		{"exe", "x.exe", []byte("MZ"), 415},
		{"shared lib", "x.so", []byte("\x7fELF"), 415},
		{"swf", "x.swf", []byte("FWS"), 415},
		{"avi", "x.avi", []byte("RIFF"), 415},
		{"no ext", "README", []byte("hi"), 415},
		{"dot only", "photo.", pngBytes(t, 4, 4), 415},
		{"dotfile", ".htaccess", []byte("x"), 415},
		{"svgz", "x.svgz", []byte("x"), 415},
		{"weird ext", "x.jp\u0067\u00e9", pngBytes(t, 4, 4), 415},
	}
	for _, tc := range bad {
		t.Run("bad/"+tc.name, func(t *testing.T) {
			u := newTestUploader(t)
			_, err := store(u, tc.file, "application/octet-stream", tc.data)
			var ue *Error
			if !errors.As(err, &ue) {
				t.Fatalf("*Error хүлээсэн, авсан %v", err)
			}
			if ue.Status != tc.status {
				t.Fatalf("status=%d want %d (%s)", ue.Status, tc.status, ue.Msg)
			}
			// Татгалзсан файл диск дээр үлдэх ёсгүй.
			assertNoFiles(t, u)
		})
	}
}

func assertNoFiles(t *testing.T, u *Uploader) {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(u.cfg.Root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n != 0 {
		t.Fatalf("татгалзсан ч %d файл үлдсэн", n)
	}
}

func TestStoreSVGClientContentType(t *testing.T) {
	u := newTestUploader(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`)
	for _, ct := range []string{"application/octet-stream", "", "text/xml", "image/png"} {
		if _, err := store(u, "a.svg", ct, svg); err == nil {
			t.Fatalf("ct=%q зөвшөөрөгдсөн", ct)
		}
	}
	for _, ct := range []string{"image/svg+xml", "IMAGE/SVG+XML; charset=utf-8"} {
		if _, err := store(u, "a.svg", ct, svg); err != nil {
			t.Fatalf("ct=%q татгалзсан: %v", ct, err)
		}
	}
	// Агуулга зөв ч script-тэй бол Content-Type зөв байсан ч татгалзана.
	bad := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if _, err := store(u, "b.svg", "image/svg+xml", bad); err == nil {
		t.Fatal("script-тэй SVG зөвшөөрөгдсөн")
	}
}

func TestSplitFileName(t *testing.T) {
	tests := []struct{ in, base, ext string }{
		{"photo.jpg", "photo", "jpg"},
		{"PHOTO.JPG", "photo", "jpg"},
		{"Гэрээ №1.pdf", "geree_1", "pdf"},
		{"Өдрийн тайлан (эцсийн).docx", "udriin_tailan_etssiin", "docx"},
		{"../../etc/passwd.txt", "passwd", "txt"},
		{`C:\Users\bat\fake.png`, "fake", "png"},
		{"a b  c.png", "a_b_c", "png"},
		{"shell.php.jpg", "shell_php", "jpg"},
		{"тест", "test", ""},
		{"日本語.png", "file", "png"},
		{"___.png", "file", "png"},
		{"thumb_x.png", "x", "png"},
		{"thumb_thumb_x.png", "x", "png"},
		{"a\x00b.png", "a_b", "png"},
		{".png", "png", ""},
		{strings.Repeat("a", 300) + ".png", strings.Repeat("a", 80), "png"},
		{"a--b__c.png", "a-b_c", "png"},
	}
	for _, tc := range tests {
		base, ext := splitFileName(tc.in)
		if base != tc.base || ext != tc.ext {
			t.Errorf("splitFileName(%q) = (%q,%q), want (%q,%q)", tc.in, base, ext, tc.base, tc.ext)
		}
		for _, r := range base {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				t.Errorf("%q: base-д аюултай тэмдэгт %q", tc.in, r)
			}
		}
	}
}

var unguessableName = regexp.MustCompile(`^geree_1-[0-9a-f]{16}\.pdf$`)

func TestStoreDefaultUnguessableName(t *testing.T) {
	u := newTestUploader(t)
	r1, err := store(u, "Гэрээ №1.pdf", "", []byte("%PDF-1.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store(u, "Гэрээ №1.pdf", "", []byte("%PDF-1.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !unguessableName.MatchString(r1.Name) || !unguessableName.MatchString(r2.Name) || r1.Name == r2.Name {
		t.Fatalf("names: %q %q", r1.Name, r2.Name)
	}
	if r1.Path != "/uploaded/documents/2026/11/"+r1.Name {
		t.Fatalf("path=%q", r1.Path)
	}
}

func TestStoreCyrillicNameLambdaCompat(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.LambdaCompat = true })
	r1, err := store(u, "Гэрээ №1.pdf", "", []byte("%PDF-1.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store(u, "Гэрээ №1.pdf", "", []byte("%PDF-1.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r1.Name != "geree_1.pdf" || r2.Name != "geree_1-1.pdf" {
		t.Fatalf("names: %q %q", r1.Name, r2.Name)
	}
	if r1.Path != "/uploaded/documents/2026/November/geree_1.pdf" {
		t.Fatalf("path=%q", r1.Path)
	}
}

func TestStoreConcurrentSameName(t *testing.T) {
	u := newTestUploader(t)
	data := pngBytes(t, 4, 4)
	const n = 20
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := store(u, "same.png", "image/png", data)
			paths[i], errs[i] = res.Path, err
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if seen[paths[i]] {
			t.Fatalf("давхардсан path %q", paths[i])
		}
		seen[paths[i]] = true
		rel := strings.TrimPrefix(paths[i], "/uploaded/")
		if ok, _ := u.storage.Exists(context.Background(), rel); !ok {
			t.Fatalf("%s диск дээр алга", rel)
		}
	}
}

func TestStoreCollisionFallsBackToRandomSuffix(t *testing.T) {
	u := newTestUploader(t)
	for i := range counterTries + 3 {
		if _, err := store(u, "x.txt", "", []byte("hello")); err != nil {
			t.Fatalf("#%d: %v", i, err)
		}
	}
}

func TestStoreSizeLimits(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.MaxBytes = 1000 })
	_, err := store(u, "big.txt", "", bytes.Repeat([]byte("a"), 1001))
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("err=%v", err)
	}
	if _, err := store(u, "ok.txt", "", bytes.Repeat([]byte("a"), 1000)); err != nil {
		t.Fatal(err)
	}

	// Ангилал тус бүрийн хязгаар Config.MaxBytes-ээс бага байж болно.
	u2 := newTestUploader(t, func(c *Config) {
		c.Types = DefaultTypes()
		c.Types[CategoryImages] = TypeRule{Exts: []string{"png"}, MaxBytes: 100}
	})
	if _, err := store(u2, "a.png", "", pngBytes(t, 64, 64)); err == nil {
		t.Fatal("зургийн ангилалын хязгаар үйлчлээгүй")
	}
	// Ангилалын хязгаар Config.MaxBytes-ээс их байж болохгүй.
	u3 := newTestUploader(t, func(c *Config) {
		c.MaxBytes = 50
		c.Types = map[string]TypeRule{CategoryDocuments: {Exts: []string{"txt"}, MaxBytes: 1 << 30}}
	})
	if got := u3.limit(CategoryDocuments); got != 50 {
		t.Fatalf("limit=%d", got)
	}
}

func TestNewConfigValidation(t *testing.T) {
	root := t.TempDir()
	cases := map[string]Config{
		"unknown ext":      {Root: root, Types: map[string]TypeRule{"images": {Exts: []string{"exe"}}}},
		"ext wrong cat":    {Root: root, Types: map[string]TypeRule{"documents": {Exts: []string{"png"}}}},
		"bad category":     {Root: root, Types: map[string]TypeRule{"../x": {Exts: []string{"png"}}}},
		"no root":          {},
		"prefix traversal": {Root: root, URLPrefix: "/a/../b"},
		"prefix query":     {Root: root, URLPrefix: "/a?x"},
	}
	for name, cfg := range cases {
		if u, err := New(cfg); err == nil {
			_ = u.Close()
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	u, err := New(Config{Root: root, URLPrefix: "files/"})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if u.Prefix() != "/files" {
		t.Fatalf("prefix=%q", u.Prefix())
	}
	if st, err := os.Stat(filepath.Join(root, "files")); err != nil || !st.IsDir() {
		t.Fatal("storage хавтас үүсээгүй")
	}
}

func TestThumbnailCreated(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.LambdaCompat = true })
	res, err := store(u, "Big.PNG", "image/png", pngBytes(t, 800, 600))
	if err != nil {
		t.Fatal(err)
	}
	if res.Thumb != "/uploaded/images/2026/November/thumb_big.png" {
		t.Fatalf("thumb=%q", res.Thumb)
	}
	f, _, err := u.storage.Open(context.Background(), "images/2026/November/thumb_big.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 300 || cfg.Height != 225 {
		t.Fatalf("thumb %dx%d", cfg.Width, cfg.Height)
	}
	// JPEG, GIF, SVG
	if r, err := store(u, "p.jpg", "", jpegBytes(t, 400, 400)); err != nil || r.Thumb == "" {
		t.Fatalf("jpg thumb: %v %+v", err, r)
	}
	if r, err := store(u, "p.gif", "", gifBytes(t)); err != nil || r.Thumb != "" {
		t.Fatalf("gif-д thumb байх ёсгүй: %v %+v", err, r)
	}
	// Thumbnail унтраалттай үед.
	u2 := newTestUploader(t, func(c *Config) { c.Thumbnail.Enabled = false })
	if r, err := store(u2, "p.png", "", pngBytes(t, 400, 400)); err != nil || r.Thumb != "" {
		t.Fatalf("thumb унтраалттай: %v %+v", err, r)
	}
}

// failThumbStorage — thumb_ нэртэй Save-г бүтэлгүйтүүлнэ.
type failThumbStorage struct{ Storage }

func (s failThumbStorage) Save(ctx context.Context, rel string, r io.Reader) error {
	if strings.Contains(rel, thumbPrefix) {
		return errors.New("disk full")
	}
	return s.Storage.Save(ctx, rel, r)
}

func TestThumbnailFailureDoesNotFailUpload(t *testing.T) {
	ls, err := NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	u := newTestUploader(t, func(c *Config) { c.Storage = failThumbStorage{ls} })
	res, err := store(u, "a.png", "", pngBytes(t, 400, 400))
	if err != nil {
		t.Fatalf("thumbnail алдаа upload-ыг унагаасан: %v", err)
	}
	if res.Thumb != "" {
		t.Fatalf("thumb=%q", res.Thumb)
	}
}

func TestHandlerPlainTextCompat(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.LambdaCompat = true })
	app := newApp(t, u, true)
	resp, body := do(t, app, multipartReq(t, "file", "Photo.jpg", "image/jpeg", jpegBytes(t, 20, 20)))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d %s", resp.StatusCode, body)
	}
	if body != "/uploaded/images/2026/November/photo.jpg" {
		t.Fatalf("body=%q", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type=%q", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff алга")
	}
	// Файл Serve-ээр нээгдэнэ.
	r2, got := do(t, app, httptest.NewRequest(http.MethodGet, body, nil))
	if r2.StatusCode != 200 || !bytes.HasPrefix([]byte(got), []byte{0xFF, 0xD8}) {
		t.Fatalf("serve status=%d", r2.StatusCode)
	}
}

func TestHandlerJSONDefault(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.BaseURL = "https://cdn.test" })
	app := newApp(t, u, true)
	resp, body := do(t, app, multipartReq(t, "file", "a.png", "image/png", pngBytes(t, 500, 500)))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d %s", resp.StatusCode, body)
	}
	var r Result
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^a-[0-9a-f]{16}\.png$`).MatchString(r.Name) {
		t.Fatalf("name=%q", r.Name)
	}
	want := Result{
		Path: "/uploaded/images/2026/11/" + r.Name, URL: "https://cdn.test/uploaded/images/2026/11/" + r.Name,
		Name: r.Name, Type: "images", Thumb: "/uploaded/images/2026/11/thumb_" + r.Name, Size: r.Size,
	}
	if r != want || r.Size <= 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestHandlerErrors(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.MaxBytes = 2000 })
	app := newApp(t, u, true)
	png := pngBytes(t, 4, 4)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"no file field", multipartReq(t, "", "", "", nil), 400},
		{"wrong field", multipartReq(t, "doc", "a.png", "image/png", png), 400},
		{"unsupported", multipartReq(t, "file", "a.exe", "application/octet-stream", []byte("MZ")), 415},
		{"mismatch", multipartReq(t, "file", "a.jpg", "image/jpeg", []byte("MZ....")), 400},
		{"svg octet-stream", multipartReq(t, "file", "a.svg", "application/octet-stream", svg), 400},
		{"too large", multipartReq(t, "file", "a.txt", "text/plain", bytes.Repeat([]byte("a"), 2001)), 413},
		{"too large body", multipartReq(t, "file", "a.txt", "text/plain", bytes.Repeat([]byte("a"), 2<<20+10)), 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := do(t, app, tc.req)
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want %d body=%s", resp.StatusCode, tc.status, body)
			}
			var e map[string]string
			if err := json.Unmarshal([]byte(body), &e); err != nil || e["error"] == "" {
				t.Fatalf("error JSON биш: %q", body)
			}
		})
	}
	assertNoFiles(t, u)
}

func TestHandlerRequiresPrincipal(t *testing.T) {
	u := newTestUploader(t)
	app := newApp(t, u, false)
	resp, _ := do(t, app, multipartReq(t, "file", "a.png", "image/png", pngBytes(t, 4, 4)))
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	assertNoFiles(t, u)

	anon := newTestUploader(t, func(c *Config) { c.AllowAnonymous = true })
	resp, _ = do(t, newApp(t, anon, false), multipartReq(t, "file", "a.png", "image/png", pngBytes(t, 4, 4)))
	if resp.StatusCode != 200 {
		t.Fatalf("anonymous status=%d", resp.StatusCode)
	}
}

func TestMountUsesAuthMiddleware(t *testing.T) {
	u := newTestUploader(t, func(c *Config) { c.AllowAnonymous = true })
	app := fiber.New()
	called := false
	u.Mount(app.Group("/up"), func(c fiber.Ctx) error {
		called = true
		return c.SendStatus(fiber.StatusForbidden)
	})
	req := multipartReq(t, "file", "a.png", "image/png", pngBytes(t, 4, 4))
	req.URL.Path = "/up"
	resp, _ := do(t, app, req)
	if !called || resp.StatusCode != 403 {
		t.Fatalf("auth middleware ажиллаагүй: called=%v status=%d", called, resp.StatusCode)
	}
}

func TestStoredFilePermissions(t *testing.T) {
	u := newTestUploader(t)
	res, err := store(u, "a.png", "", pngBytes(t, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(u.cfg.Root, filepath.FromSlash(res.Path))
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o022 != 0 || st.Mode().Perm()&0o444 != 0o444 {
		t.Fatalf("file mode=%v", st.Mode())
	}
	dst, _ := os.Stat(filepath.Dir(p))
	if dst.Mode().Perm()&0o022 != 0 || dst.Mode().Perm()&0o755 != 0o755 {
		t.Fatalf("dir mode=%v", dst.Mode())
	}
	// .khan-tmp- файл үлдээгүй.
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("түр файл үлдсэн: %s", e.Name())
		}
	}
}

func TestStoreHonoursCancelledContext(t *testing.T) {
	u := newTestUploader(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := []byte("%PDF-1.4\n")
	_, err := u.Store(ctx, "a.pdf", "", bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("цуцлагдсан ctx-д алдаа хүлээсэн")
	}
	_ = fmt.Sprint(err)
}
