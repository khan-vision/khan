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
// symlink зөвхөн root дотор үлдэх үед л дагагдана. Файл 0644, хавтас 0755.
type LocalStorage struct {
	root *os.Root
}

// NewLocalStorage — dir хавтсыг (байхгүй бол 0755-аар) үүсгэж нээнэ.
func NewLocalStorage(dir string) (*LocalStorage, error) {
	if dir == "" {
		return nil, errors.New("upload: Root хоосон")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("upload: хавтас үүсгэж чадсангүй: %w", err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("upload: root нээж чадсангүй: %w", err)
	}
	return &LocalStorage{root: r}, nil
}

// Close — root handle-ийг хаана.
func (s *LocalStorage) Close() error { return s.root.Close() }

// Save — Storage.Save. Дараалал: (1) нэрийг O_EXCL-ээр нөөцөлнэ (давхцлыг
// атомаар илрүүлнэ), (2) агуулгыг ижил хавтсанд түр файлд бичиж fsync хийнэ,
// (3) түр файлыг нөөцлөсөн нэр дээр rename хийнэ (уншигч хагас файл харахгүй).
func (s *LocalStorage) Save(ctx context.Context, relPath string, r io.Reader) (err error) {
	if !ValidRelPath(relPath) {
		return ErrUnsafePath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir := path.Dir(relPath); dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	reserve, err := s.root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err // fs.ErrExist давхцалд
	}
	_ = reserve.Close()
	tmpName := path.Join(path.Dir(relPath), ".tmp-"+randomHex(8))
	defer func() {
		if err != nil {
			_ = s.root.Remove(tmpName)
			_ = s.root.Remove(relPath)
		}
	}()
	tmp, err := s.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
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
	return s.root.Rename(tmpName, relPath)
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
