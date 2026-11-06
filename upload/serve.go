package upload

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/obs"
)

const (
	// cacheControl — файлын нэр unique (давхцахад суффикс) ⇒ өөрчлөгддөггүй.
	cacheControl = "public, max-age=31536000, immutable"
	// sandboxCSP — SVG/HTML/XML шууд нээгдсэн ч script ажиллуулахгүй (<img>-д нөлөөгүй).
	sandboxCSP = "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
)

// activeExt — хөтөч дотор script ажиллуулж чадах төрлүүд ⇒ CSP sandbox.
var activeExt = map[string]bool{
	"svg": true, "svgz": true, "html": true, "htm": true, "xhtml": true, "xht": true, "xml": true,
}

// inlineExt — хөтөч дотор шууд харуулах (Content-Disposition: inline) төрлүүд;
// бусад нь `attachment` (html/xml/json/csv/docx/zip …).
var inlineExt = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "svg": true,
	"pdf": true, "mp4": true, "m4v": true, "webm": true, "mp3": true, "wav": true, "aac": true, "m4a": true,
}

// contentTypeFor — өргөтгөлөөс MIME (OS-ийн mime db-д найдахгүй: эхлээд өөрийн
// жагсаалт). Мэдэгдэхгүй бол application/octet-stream.
func contentTypeFor(ext string) string {
	if ext == "svgz" {
		return "application/octet-stream"
	}
	if m := mimeOf(ext); m != "" {
		return m
	}
	if m := mime.TypeByExtension("." + ext); m != "" {
		return m
	}
	return "application/octet-stream"
}

// Serve — GET/HEAD `<Prefix>/*`. Зам зөвхөн идэвхтэй ангилал доторх энгийн файл;
// `..`, абсолют, NUL, `.`-аар эхэлсэн сегмент (түр файл), хавтас → 404 (жагсаалтгүй).
// `thumb_<name>` байхгүй бол эх файл (lambda-ийн fallback); `?thumb=true` бол
// thumb_ хувилбар байвал түүнийг. Range (206/416), ETag/Last-Modified (304) дэмжинэ.
func (u *Uploader) Serve() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
		method := c.Method()
		if method != fiber.MethodGet && method != fiber.MethodHead {
			c.Set(fiber.HeaderAllow, "GET, HEAD")
			return c.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "Буруу method"})
		}
		rel, ok := u.relFromRequest(c.Path())
		if !ok {
			return notFound(c)
		}
		cat := rel[:strings.IndexByte(rel, '/')]
		dir, name := path.Split(rel)

		if cat == CategoryImages && c.Query("thumb") == "true" && !strings.HasPrefix(name, thumbPrefix) {
			if ok, _ := u.storage.Exists(c.Context(), dir+thumbPrefix+name); ok {
				rel, name = dir+thumbPrefix+name, thumbPrefix+name
			}
		}
		f, fi, err := u.storage.Open(c.Context(), rel)
		if errors.Is(err, fs.ErrNotExist) && cat == CategoryImages && strings.HasPrefix(name, thumbPrefix) {
			name = strings.TrimPrefix(name, thumbPrefix)
			f, fi, err = u.storage.Open(c.Context(), dir+name)
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return notFound(c)
			}
			obs.From(c.Context()).Error("upload: файл нээх алдаа", "path", rel, "error", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Серверийн алдаа"})
		}
		cc := cacheControl
		if fi.Size() == 0 {
			// 0 байт файл бүрэн бус байж болзошгүй: нэг жил кэшлүүлэхгүй, үргэлж дахин шалгуулна.
			cc = "public, max-age=0, must-revalidate"
		}
		return respondFile(c, f, fi, name, respondOpts{cacheControl: cc})
	}
}

// respondOpts — respondFile-ийн хувилбарууд (public/private).
type respondOpts struct {
	cacheControl string
	// disposition — "attachment" үргэлж татуулна; "inline" зөвхөн inlineExt-д; "" төрлөөс.
	disposition string
	// private — Referrer-Policy/X-Robots-Tag нэмнэ (URL-д гарын үсэг байдаг).
	private bool
}

// respondFile — нээгдсэн f-ийг (header, ETag/304, Range, HEAD) хариулна; f-ийг
// өөрөө хаана (stream эхэлсэн бол stream дуусахад).
func respondFile(c fiber.Ctx, f io.ReadSeekCloser, fi fs.FileInfo, name string, o respondOpts) error {
	method := c.Method()
	streaming := false
	defer func() {
		if !streaming {
			_ = f.Close()
		}
	}()

	ext := ""
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		ext = strings.ToLower(name[i+1:])
	}
	c.Set(fiber.HeaderContentType, contentTypeFor(ext))
	c.Set(fiber.HeaderCacheControl, o.cacheControl)
	c.Set(fiber.HeaderAcceptRanges, "bytes")
	if o.private {
		c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
		c.Set("X-Robots-Tag", "noindex, noarchive")
	}
	if activeExt[ext] {
		c.Set(fiber.HeaderContentSecurityPolicy, sandboxCSP)
	}
	if o.disposition == DispositionAttachment || !inlineExt[ext] {
		c.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	etag := `W/"` + strconv.FormatInt(fi.Size(), 16) + "-" + strconv.FormatInt(fi.ModTime().UnixNano(), 16) + `"`
	lastMod := fi.ModTime().UTC().Format(http.TimeFormat)
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderLastModified, lastMod)

	if notModified(c, etag, fi.ModTime().Unix()) {
		return c.SendStatus(fiber.StatusNotModified)
	}

	size := fi.Size()
	start, length, status := int64(0), size, fiber.StatusOK
	if rh := c.Get(fiber.HeaderRange); rh != "" && method == fiber.MethodGet {
		if ir := c.Get(fiber.HeaderIfRange); ir == "" || ir == etag || ir == lastMod {
			s, l, state := parseRange(rh, size)
			switch state {
			case rangeOK:
				start, length, status = s, l, fiber.StatusPartialContent
				c.Set(fiber.HeaderContentRange, "bytes "+strconv.FormatInt(s, 10)+"-"+strconv.FormatInt(s+l-1, 10)+"/"+strconv.FormatInt(size, 10))
			case rangeUnsatisfiable:
				c.Set(fiber.HeaderContentRange, "bytes */"+strconv.FormatInt(size, 10))
				return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
			}
		}
	}
	c.Status(status)
	if method == fiber.MethodHead || length == 0 {
		c.Response().Header.SetContentLength(int(length))
		return nil
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Серверийн алдаа"})
		}
	}
	streaming = true
	return c.SendStream(readCloser{io.LimitReader(f, length), f}, int(length))
}

type readCloser struct {
	io.Reader
	io.Closer
}

func notFound(c fiber.Ctx) error {
	return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Файл олдсонгүй"})
}

// relFromRequest — хүсэлтийн замаас Storage-ийн relPath гаргаж, аюулгүй эсэхийг шалгана.
func (u *Uploader) relFromRequest(p string) (string, bool) {
	if !strings.HasPrefix(p, u.prefix+"/") {
		return "", false
	}
	rel, ok := unescapePath(p[len(u.prefix)+1:])
	if !ok || !ValidRelPath(rel) {
		return "", false
	}
	segs := strings.Split(rel, "/")
	if len(segs) < 2 {
		return "", false
	}
	if _, ok := u.rules[segs[0]]; !ok {
		return "", false
	}
	for _, s := range segs {
		if strings.HasPrefix(s, ".") {
			return "", false
		}
	}
	return rel, true
}

// notModified — If-None-Match (эсвэл түүнгүй үед If-Modified-Since) таарвал true.
func notModified(c fiber.Ctx, etag string, modUnix int64) bool {
	if inm := c.Get(fiber.HeaderIfNoneMatch); inm != "" {
		for _, t := range strings.Split(inm, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.TrimPrefix(t, "W/") == strings.TrimPrefix(etag, "W/") {
				return true
			}
		}
		return false
	}
	if ims := c.Get(fiber.HeaderIfModifiedSince); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && modUnix <= t.Unix() {
			return true
		}
	}
	return false
}

type rangeState int

const (
	rangeIgnore rangeState = iota // хоосон/буруу/олон range ⇒ бүтэн 200
	rangeOK
	rangeUnsatisfiable
)

// parseRange — нэг `bytes=a-b` | `bytes=a-` | `bytes=-n` range. Олон range болон
// синтакс алдааг RFC 9110-ийн дагуу үл тоож бүтнээр нь өгнө.
func parseRange(h string, size int64) (start, length int64, st rangeState) {
	const unit = "bytes="
	if len(h) < len(unit) || !strings.EqualFold(h[:len(unit)], unit) {
		return 0, 0, rangeIgnore
	}
	spec := strings.TrimSpace(h[len(unit):])
	if spec == "" || strings.Contains(spec, ",") {
		return 0, 0, rangeIgnore
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, rangeIgnore
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" { // suffix: сүүлийн n байт
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, rangeIgnore
		}
		if n == 0 || size == 0 {
			return 0, 0, rangeUnsatisfiable
		}
		n = min(n, size)
		return size - n, n, rangeOK
	}
	s, err := strconv.ParseInt(a, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, rangeIgnore
	}
	e := size - 1
	if b != "" {
		if e, err = strconv.ParseInt(b, 10, 64); err != nil || e < s {
			return 0, 0, rangeIgnore
		}
		e = min(e, size-1)
	}
	if s >= size {
		return 0, 0, rangeUnsatisfiable
	}
	return s, e - s + 1, rangeOK
}
