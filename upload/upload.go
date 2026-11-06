// Package upload нь файл оруулах (POST) ба үйлчлэх (GET) аюулгүй давхарга —
// lambda-ийн `POST /lambda/krud/upload` болон `/uploaded/images/...` thumbnail
// route-ийг орлоно. URL ба хавтасны бүтэц ИЖИЛ үлдэнэ:
//
//	<Root>/uploaded/<images|documents|videos|audios>/<YYYY>/<MonthName>/<file>
//	/uploaded/images/2026/November/photo.jpg   (thumbnail: thumb_photo.jpg)
//
// Lambda-ийн асуудлууд ба засвар:
//   - Төрлийг зөвхөн өргөтгөл/клиентийн Content-Type-аар тодорхойлдог байсан ⇒
//     өргөтгөлийн allowlist + агуулгын (magic byte, decode, zip бүтэц) шалгалт;
//     таараагүй бол 400. `sharedlib` (.so) төрлийг бүрмөсөн хассан.
//   - SVG-г зөвхөн `<script`/`<!DOCTYPE` string-ээр шалгадаг, Content-Type
//     "octet-stream" бол огт шалгадаггүй байсан (stored XSS) ⇒ XML tokenizer-ээр
//     шалгаж (svg.go), .svg-д клиентийн Content-Type=image/svg+xml шаардана.
//   - Нэрийг рекурсив os.Stat-аар давхцуулдаг (race) ⇒ O_EXCL + хязгаартай давталт.
//   - Файлыг 0700, шууд (хагас бичигдэх боломжтой) бичдэг ⇒ түр файл+rename, 0644/0755.
//   - Decompression bomb хамгаалалтгүй ⇒ MaxPixels (анхдагч 50 MP).
//
// Анхаарах (аппын тал):
//   - Fiber `BodyLimit`-ийг ≥ Config.MaxBytes + 1 МБ (multipart нэмэлт) болгож
//     тохируулна, эс бөгөөс fasthttp өөрөө 413 өгнө.
//   - POST route-ийг заавал auth-ын ард суулгана (Mount). Handler мөн ч principal
//     байхгүй бол 401 өгнө (AllowAnonymous=false үед).
//   - Serve route нь нийтэд нээлттэй (`<img src>` cookie-гүй ажиллах ёстой). Тиймээс
//     анхдагчаар файлын нэрэнд 64 битийн санамсаргүй хэсэг нэмж URL-ийг таашгүй
//     болгоно (`geree-3f9a0c1d2e4b5a6f.pdf`); lambda-ийн `geree_1.pdf` маягийн
//     нэрийг дарааллаар нь таамаглаж бусдын баримтыг татах боломжтой байсан.
package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/obs"
)

// Анхдагч утгууд.
const (
	DefaultURLPrefix    = "/uploaded"
	DefaultMaxBytes     = 10 << 20
	DefaultThumbSize    = 300
	DefaultMaxPixels    = 50_000_000
	multipartSlackBytes = 1 << 20
	thumbWorkers        = 4
)

// Thumbnail — thumbnail тохиргоо (зөвхөн jpg/jpeg/png; gif/webp/svg-д хийхгүй).
type Thumbnail struct {
	Enabled bool
	Size    int // хамгийн урт тал (px); 0 → 300
}

// Config — Uploader-ийн тохиргоо.
type Config struct {
	// Root — диск дээрх public хавтас (ж: "public"). Файл `<Root><URLPrefix>/...`-д
	// хадгалагдана. Storage өгсөн бол хэрэглэхгүй.
	Root string
	// URLPrefix — нийтийн URL-ийн угтвар (анхдагч "/uploaded").
	URLPrefix string
	// MaxBytes — нэг файлын дээд хэмжээ (config.Upload.MaxBytes); ≤0 → 10 МБ.
	MaxBytes int64
	// Types — ангилал → дүрэм; nil бол DefaultTypes().
	Types map[string]TypeRule
	// Thumbnail — thumbnail тохиргоо.
	Thumbnail Thumbnail
	// MaxPixels — зургийн өргөн*өндрийн дээд (≤0 → 50 сая).
	MaxPixels int64
	// Now — цаг (тест); nil → time.Now.
	Now func() time.Time
	// Storage — nil бол Root дээр LocalStorage.
	Storage Storage
	// LambdaCompat — ЗӨВХӨН шилжилтийн хуучин route-д (`/lambda/krud/upload`):
	// хариу text/plain зам, сарын нэртэй хавтас (`2026/November`), уншигдахуйц
	// дараалсан нэр (`a.jpg`, `a-1.jpg`). Анхдагч (false): JSON Result, `2026/11`,
	// таашгүй нэр.
	LambdaCompat bool
	// BaseURL — JSON-ийн `url`-д угтвар болгох origin (ж: "https://cdn.example.mn");
	// хоосон бол `url` = `path`.
	BaseURL string
	// AllowAnonymous — true бол нэвтрээгүй хүсэлтийг Handler өөрөө хаахгүй (нийтийн
	// форм). Анхдагч false: auth.User(c) байхгүй бол 401.
	AllowAnonymous bool
}

// FromConfig — config.Upload-ээс Config (MaxBytes, thumbnail идэвхтэй).
func FromConfig(up config.Upload, root string) Config {
	return Config{
		Root:      root,
		MaxBytes:  int64(up.MaxBytes),
		Thumbnail: Thumbnail{Enabled: true, Size: DefaultThumbSize},
	}
}

// Result — амжилттай оруулсан файл.
type Result struct {
	Path  string `json:"path"`            // /uploaded/images/2026/November/a.jpg
	URL   string `json:"url"`             // BaseURL + Path
	Name  string `json:"name"`            // a.jpg (серверийн нэр)
	Size  int64  `json:"size"`            // байт
	Type  string `json:"type"`            // ангилал: images|documents|videos|audios
	Thumb string `json:"thumb,omitempty"` // /uploaded/images/.../thumb_a.jpg
}

// Error — клиентэд харуулах алдаа (HTTP статустай).
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func badRequest(msg string) *Error { return &Error{fiber.StatusBadRequest, msg} }

// Uploader — оруулах ба үйлчлэх.
type Uploader struct {
	cfg     Config
	prefix  string
	storage Storage
	local   *LocalStorage // өөрөө үүсгэсэн бол Close-д
	extCat  map[string]string
	rules   map[string]TypeRule
	now     func() time.Time
	thumbs  chan struct{}
	once    sync.Once
}

// New — Config-г шалгаж Uploader үүсгэнэ.
func New(cfg Config) (*Uploader, error) {
	prefix, err := normalizePrefix(cfg.URLPrefix)
	if err != nil {
		return nil, err
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxPixels <= 0 {
		cfg.MaxPixels = DefaultMaxPixels
	}
	if cfg.Thumbnail.Size <= 0 {
		cfg.Thumbnail.Size = DefaultThumbSize
	}
	rules := cfg.Types
	if rules == nil {
		rules = DefaultTypes()
	}
	extCat := make(map[string]string)
	for cat, rule := range rules {
		if !validCategory(cat) {
			return nil, fmt.Errorf("upload: ангилалын нэр буруу: %q", cat)
		}
		for _, e := range rule.Exts {
			e = strings.ToLower(e)
			sp, ok := known[e]
			if !ok {
				return nil, fmt.Errorf("upload: %q өргөтгөлд агуулгын шалгагч алга (зөвшөөрөх боломжгүй)", e)
			}
			if sp.category != cat {
				return nil, fmt.Errorf("upload: %q өргөтгөл %q ангилалд хамаарахгүй", e, cat)
			}
			extCat[e] = cat
		}
	}
	u := &Uploader{cfg: cfg, prefix: prefix, extCat: extCat, rules: rules, now: cfg.Now,
		thumbs: make(chan struct{}, thumbWorkers)}
	if u.now == nil {
		u.now = time.Now
	}
	u.storage = cfg.Storage
	if u.storage == nil {
		if cfg.Root == "" {
			return nil, errors.New("upload: Root эсвэл Storage шаардлагатай")
		}
		ls, err := NewLocalStorage(cfg.Root + prefix)
		if err != nil {
			return nil, err
		}
		u.local, u.storage = ls, ls
	}
	return u, nil
}

// Close — өөрөө үүсгэсэн LocalStorage-ийг хаана.
func (u *Uploader) Close() error {
	var err error
	u.once.Do(func() {
		if u.local != nil {
			err = u.local.Close()
		}
	})
	return err
}

// Prefix — нийтийн URL-ийн угтвар (ж: "/uploaded"); Serve-ийг `Prefix()+"/*"`-д суулгана.
func (u *Uploader) Prefix() string { return u.prefix }

func normalizePrefix(p string) (string, error) {
	if p == "" {
		return DefaultURLPrefix, nil
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" || strings.ContainsAny(p, "?#%\\\x00") || strings.Contains(p, "..") || strings.Contains(p, "//") {
		return "", fmt.Errorf("upload: URLPrefix буруу: %q", p)
	}
	return p, nil
}

func validCategory(c string) bool {
	if c == "" {
		return false
	}
	for _, r := range c {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// limit — ангилалын үр дүнтэй дээд хэмжээ.
func (u *Uploader) limit(category string) int64 {
	l := u.cfg.MaxBytes
	if r := u.rules[category].MaxBytes; r > 0 && r < l {
		l = r
	}
	return l
}

// Store — нэг файлыг шалгаж хадгална (HTTP-гүй). filename/clientContentType нь
// клиентийн өгсөн (итгэхгүй) утга; f нь multipart.File. Алдаа нь *Error (клиентийн)
// эсвэл дотоод алдаа.
func (u *Uploader) Store(ctx context.Context, filename, clientContentType string, f File, size int64) (Result, error) {
	base, ext := splitFileName(filename)
	cat, ok := u.extCat[ext]
	if !ok {
		return Result{}, &Error{fiber.StatusUnsupportedMediaType, "Энэ төрлийн файл оруулах боломжгүй"}
	}
	sp := known[ext]
	if size <= 0 {
		return Result{}, badRequest("Файл хоосон байна")
	}
	if size > u.limit(cat) {
		return Result{}, &Error{fiber.StatusRequestEntityTooLarge,
			fmt.Sprintf("Файлын хэмжээ %d МБ-аас хэтэрсэн байна", (u.limit(cat)+(1<<20)-1)>>20)}
	}
	if ext == "svg" {
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(clientContentType, ";", 2)[0]))
		if ct != "image/svg+xml" {
			return Result{}, badRequest("SVG файлын төрөл буруу")
		}
	}
	head, err := readAt(f, size, sniffLen)
	if err != nil {
		return Result{}, fmt.Errorf("upload: файл унших: %w", err)
	}
	if err := sp.check(head, f, size); err != nil {
		// Зургийн өргөтгөл буруу (утас/интернэтээс татсан PNG-г .jpg гэх мэт) нь элбэг —
		// агуулга нь өөр ЗӨВШӨӨРӨГДСӨН raster зураг бол татгалзахын оронд жинхэнэ
		// өргөтгөлөөр нь хадгална. Бусад тохиолдолд (exe→jpg, svg г.м.) хатуу хэвээр.
		real := ""
		if errors.Is(err, errContent) && cat == CategoryImages && ext != "svg" {
			real = u.rasterExt(head)
		}
		switch {
		case real != "":
			ext, sp = real, known[real]
		case errors.Is(err, errContent):
			return Result{}, badRequest("Файлын агуулга өргөтгөлтэй таарахгүй байна")
		default:
			return Result{}, badRequest(err.Error())
		}
	}
	if cat == CategoryImages && ext != "svg" {
		if _, _, err = imageConfig(f, size, u.cfg.MaxPixels); err != nil {
			if errors.Is(err, errTooManyPixels) {
				return Result{}, badRequest("Зургийн нягт (pixel) хэт их байна")
			}
			return Result{}, badRequest("Зураг уншигдсангүй")
		}
	}

	now := u.now()
	month := fmt.Sprintf("%02d", int(now.Month()))
	if u.cfg.LambdaCompat {
		month = now.Month().String()
	}
	dir := path.Join(cat, fmt.Sprintf("%d", now.Year()), month)
	var name string
	for i := 0; i < maxNameTrials; i++ {
		cand := u.candidateName(base, ext, i)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		err := u.storage.Save(ctx, path.Join(dir, cand), io.NewSectionReader(f, 0, size))
		if err == nil {
			name = cand
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return Result{}, fmt.Errorf("upload: хадгалах: %w", err)
		}
	}
	if name == "" {
		return Result{}, errors.New("upload: давхцалгүй нэр олдсонгүй")
	}

	res := Result{
		Name: name, Size: size, Type: cat,
		Path: u.prefix + "/" + path.Join(dir, name),
	}
	res.URL = u.cfg.BaseURL + res.Path
	if u.cfg.Thumbnail.Enabled && (ext == "jpg" || ext == "jpeg" || ext == "png") {
		thumbName := thumbPrefix + name
		if err := u.makeThumb(ctx, f, size, ext, path.Join(dir, thumbName)); err != nil {
			obs.From(ctx).Warn("upload: thumbnail үүсгэж чадсангүй", "file", res.Path, "error", err)
		} else {
			res.Thumb = u.prefix + "/" + path.Join(dir, thumbName)
		}
	}
	return res, nil
}

// rasterExt — агуулгаар нь танигдсан, тохиргоонд зөвшөөрөгдсөн raster зургийн
// өргөтгөл (олдохгүй бол ""). SVG-г хэзээ ч буцаахгүй.
func (u *Uploader) rasterExt(head []byte) string {
	byMime := map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp"}
	ext, ok := byMime[http.DetectContentType(head)]
	if !ok {
		return ""
	}
	if c, allowed := u.extCat[ext]; !allowed || c != CategoryImages {
		return ""
	}
	return ext
}

func (u *Uploader) makeThumb(ctx context.Context, f File, size int64, ext, rel string) error {
	select {
	case u.thumbs <- struct{}{}:
		defer func() { <-u.thumbs }()
	case <-ctx.Done():
		return ctx.Err()
	}
	img, err := imaging.Decode(io.NewSectionReader(f, 0, size), imaging.AutoOrientation(true))
	if err != nil {
		return err
	}
	img = imaging.Fit(img, u.cfg.Thumbnail.Size, u.cfg.Thumbnail.Size, imaging.Lanczos)
	var buf bytes.Buffer
	if ext == "png" {
		err = imaging.Encode(&buf, img, imaging.PNG)
	} else {
		err = imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(75))
	}
	if err != nil {
		return err
	}
	return u.storage.Save(ctx, rel, &buf)
}

// Handler — `POST` multipart, талбар `file`. Хариу: анхдагч JSON Result;
// LambdaCompat=true бол plain-text нийтийн зам. Алдаа:
// `{"error":"..."}` 400/401/413/415/500.
func (u *Uploader) Handler() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !u.cfg.AllowAnonymous {
			if _, ok := auth.User(c); !ok {
				return fail(c, &Error{fiber.StatusUnauthorized, "Зөвшөөрөлгүй"})
			}
		}
		if cl := int64(c.Request().Header.ContentLength()); cl > u.cfg.MaxBytes+multipartSlackBytes {
			return fail(c, &Error{fiber.StatusRequestEntityTooLarge, "Файлын хэмжээ хэт их байна"})
		}
		fh, err := c.FormFile("file")
		if err != nil {
			var fe *fiber.Error
			if errors.As(err, &fe) && fe.Code == fiber.StatusRequestEntityTooLarge {
				return fail(c, &Error{fiber.StatusRequestEntityTooLarge, "Файлын хэмжээ хэт их байна"})
			}
			return fail(c, badRequest("Файл олдсонгүй"))
		}
		src, err := fh.Open()
		if err != nil {
			return fail(c, badRequest("Файл уншигдсангүй"))
		}
		defer src.Close()

		log := obs.With(c)
		res, err := u.Store(c.Context(), fh.Filename, fh.Header.Get(fiber.HeaderContentType), src, fh.Size)
		if err != nil {
			var ue *Error
			if errors.As(err, &ue) {
				log.Info("upload: татгалзсан", "reason", ue.Msg, "status", ue.Status)
				return fail(c, ue)
			}
			log.Error("upload: алдаа", "error", err)
			return fail(c, &Error{fiber.StatusInternalServerError, "Файл хадгалахад алдаа гарлаа"})
		}
		logUploaded(log, res)
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
		if !u.cfg.LambdaCompat {
			return c.JSON(res)
		}
		c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
		return c.SendString(res.Path)
	}
}

func logUploaded(log *slog.Logger, r Result) {
	log.Info("upload: хадгалсан", "path", r.Path, "size", r.Size, "type", r.Type)
}

func fail(c fiber.Ctx, e *Error) error {
	return c.Status(e.Status).JSON(fiber.Map{"error": e.Msg})
}

// Mount — r дээр `POST /`-ийг auth middleware-ийн ард бүртгэнэ:
//
//	api := app.Group("/api/upload")
//	up.Mount(api, auth.Required(kr))  // POST /api/upload
//
// Lambda-ийн хуучин `/lambda/krud/upload` замыг шилжилтийн үед ижил Handler-т заана.
// auth nil бол зөвхөн Handler (AllowAnonymous=false үед өөрөө 401 өгнө).
func (u *Uploader) Mount(r fiber.Router, authMW fiber.Handler) {
	if authMW != nil {
		r.Post("/", authMW, u.Handler())
		return
	}
	r.Post("/", u.Handler())
}

// MountServe — GET/HEAD `<Prefix>/*`-ийг app дээр бүртгэнэ (auth-гүй).
func (u *Uploader) MountServe(r fiber.Router) {
	r.Get(u.prefix+"/*", u.Serve())
	r.Head(u.prefix+"/*", u.Serve())
}

// unescapePath — URL-ийн зам дахь %XX-ийг НЭГ удаа тайлна.
func unescapePath(p string) (string, bool) {
	s, err := url.PathUnescape(p)
	return s, err == nil
}
