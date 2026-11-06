package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// ErrUnsafePath — Storage-д өгсөн зам аюултай (`..`, абсолют, NUL, `\`).
var ErrUnsafePath = errors.New("upload: аюултай файлын зам")

// Storage — файл хадгалах backend. Local диск (LocalStorage) анхдагч; S3 зэрэг
// бусдыг энэ interface-ээр залгана. relPath нь үргэлж `/`-ээр тусгаарласан,
// цэвэрлэгдсэн хэлбэртэй (жишээ нь "images/2026/November/photo.jpg").
type Storage interface {
	// Save — r-ийг relPath-д бичнэ. relPath АЛЬ ХЭДИЙН байвал ХЭЗЭЭ Ч дарахгүй,
	// errors.Is(err, fs.ErrExist) таарах алдаа буцаана (давхцал шийдэх үндэс).
	// Алдаа гарвал хагас бичигдсэн файл үлдэх ЁСГҮЙ.
	Save(ctx context.Context, relPath string, r io.Reader) error
	// Open — файлыг нээнэ. Байхгүй (эсвэл хавтас) бол fs.ErrNotExist.
	Open(ctx context.Context, relPath string) (io.ReadSeekCloser, fs.FileInfo, error)
	// Exists — файл (хавтас биш) байгаа эсэх.
	Exists(ctx context.Context, relPath string) (bool, error)
}

// ValidRelPath — Storage-ийн relPath зөвхөн аюулгүй байх ёстой: хоосон биш, `/`-ээр
// эхлэхгүй, `.`/`..`/хоосон сегментгүй, NUL болон `\` агуулаагүй.
func ValidRelPath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\x00\\") {
		return false
	}
	return fs.ValidPath(p) && p != "."
}

// LocalStorage — local дискний Storage. os.Root ашигладаг тул Root-оос гадагш
// гарах `..` болон symlink-ийг kernel түвшинд (atomic) хориглоно: root доторх
// symlink зөвхөн root дотор үлдэх үед л дагагдана. Файл 0644, хавтас 0755 (хувийн: 0600/0700).
type LocalStorage struct {
	root     *os.Root
	fileMode os.FileMode
	dirMode  os.FileMode
	// beforePublish — зөвхөн тестийн hook: бичиж дууссан, гэхдээ эцсийн нэр
	// гарахаас өмнөх мөчид дуудагдана.
	beforePublish func(relPath string)
}

// NewLocalStorage — dir хавтсыг (байхгүй бол 0755-аар) үүсгэж нээнэ.
func NewLocalStorage(dir string) (*LocalStorage, error) {
	return newLocalStorage(dir, 0o644, 0o755)
}

// NewPrivateLocalStorage — хувийн файлын local Storage: файл 0600, хавтас 0700
// (зөвхөн аппын процессийн эзэн уншина). Бусад нь NewLocalStorage-тэй ижил.
func NewPrivateLocalStorage(dir string) (*LocalStorage, error) {
	return newLocalStorage(dir, 0o600, 0o700)
}

func newLocalStorage(dir string, fileMode, dirMode os.FileMode) (*LocalStorage, error) {
	if dir == "" {
		return nil, errors.New("upload: Root хоосон")
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("upload: хавтас үүсгэж чадсангүй: %w", err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("upload: root нээж чадсангүй: %w", err)
	}
	return &LocalStorage{root: r, fileMode: fileMode, dirMode: dirMode}, nil
}

// Close — root handle-ийг хаана.
func (s *LocalStorage) Close() error { return s.root.Close() }

// tmpPrefix — Save-ийн түр файлын нэрийн угтвар. Цэгээр эхэлдэг тул Serve /
// ServePrivate хэзээ ч үйлчлэхгүй (relFromRequest `.`-эхлэлийг, ServePrivate
// regexp хориглоно).
const tmpPrefix = ".khan-tmp-"

// Save — Storage.Save. Эцсийн нэр дээр ЗӨВХӨН бүрэн бичигдсэн файл гарна:
// (1) агуулгыг ижил хавтсанд нуугдмал түр файлд (`.khan-tmp-*`) бичиж fsync
// хийнэ, (2) түр файлыг эцсийн нэр рүү os.Link-ээр (no-replace, атомар;
// байвал fs.ErrExist) холбоод түр нэрийг устгана. Link дэмжигдэхгүй
// файлын системд (EPERM/ENOTSUP) Lstat + Rename-д буцна (хэт нарийн уралдаан
// боломжтой: хоёр зэрэгцээ Save нэг нэрийг сонговол сүүлийнх нь дарж болно).
// Алдаа/цуцлалтад түр файл устана; crash хийвэл зөвхөн нуугдмал `.khan-tmp-*` үлдэнэ.
func (s *LocalStorage) Save(ctx context.Context, relPath string, r io.Reader) (err error) {
	if !ValidRelPath(relPath) {
		return ErrUnsafePath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir := path.Dir(relPath); dir != "." {
		if err := s.root.MkdirAll(dir, s.dirMode); err != nil {
			return err
		}
	}
	tmpName := path.Join(path.Dir(relPath), tmpPrefix+randomHex(8))
	tmp, err := s.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, s.fileMode)
	if err != nil {
		return err
	}
	// Түр нэр ямагт устана (амжилттай Link-ийн дараа ч — hard link хоёр нэртэй).
	defer func() { _ = s.root.Remove(tmpName) }()
	if _, err = io.Copy(tmp, &ctxReader{ctx: ctx, r: r}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if s.beforePublish != nil {
		s.beforePublish(relPath)
	}
	return s.publish(tmpName, relPath)
}

// publish — tmp-г dst нэр дээр дарахгүйгээр гаргана.
func (s *LocalStorage) publish(tmp, dst string) error {
	err := s.root.Link(tmp, dst)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	// Link дэмжигдэхгүй байж болзошгүй: хамгаалалттай fallback.
	if _, serr := s.root.Lstat(dst); serr == nil {
		return &fs.PathError{Op: "save", Path: dst, Err: fs.ErrExist}
	} else if !errors.Is(serr, fs.ErrNotExist) {
		return err
	}
	return s.root.Rename(tmp, dst)
}

// Open — Storage.Open.
func (s *LocalStorage) Open(ctx context.Context, relPath string) (io.ReadSeekCloser, fs.FileInfo, error) {
	if !ValidRelPath(relPath) {
		return nil, nil, fs.ErrNotExist
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	f, err := s.root.Open(relPath)
	if err != nil {
		return nil, nil, mapEscape(err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fs.ErrNotExist
	}
	return f, fi, nil
}

// Exists — Storage.Exists.
func (s *LocalStorage) Exists(ctx context.Context, relPath string) (bool, error) {
	if !ValidRelPath(relPath) {
		return false, nil
	}
	fi, err := s.root.Stat(relPath)
	if err != nil {
		if err = mapEscape(err); errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic("upload: crypto/rand: " + err.Error()) // Go 1.24+: rand.Read хэзээ ч алдаа өгөхгүй
	}
	return hex.EncodeToString(b)
}

// mapEscape — os.Root-ийн "path escapes from parent" (root-оос гарсан symlink/..)
// алдааг fs.ErrNotExist болгоно: гаднах хүнд ийм файл "байхгүй".
func mapEscape(err error) error {
	if err != nil && strings.Contains(err.Error(), "path escapes from parent") {
		return fs.ErrNotExist
	}
	return err
}
