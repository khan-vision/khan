package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidRelPath(t *testing.T) {
	ok := []string{"images/a.png", "a", "images/2026/November/x_y-1.jpg"}
	bad := []string{"", ".", "..", "../x", "a/../b", "a/./b", "/abs", "a//b", "a/", "a\\b", "a\x00b", "images/..", "%2e%2e/x"[:0]}
	for _, p := range ok {
		if !ValidRelPath(p) {
			t.Errorf("%q зөвшөөрөгдөх ёстой", p)
		}
	}
	for _, p := range bad {
		if ValidRelPath(p) {
			t.Errorf("%q зөвшөөрөгдөх ёсгүй", p)
		}
	}
}

func TestLocalStorageBasics(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.Save(ctx, "images/a/b.txt", strings.NewReader("one")); err != nil {
		t.Fatal(err)
	}
	// Дарахгүй.
	err = s.Save(ctx, "images/a/b.txt", strings.NewReader("two"))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("ErrExist хүлээсэн: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "images/a/b.txt"))
	if string(got) != "one" {
		t.Fatalf("файл дарагдсан: %q", got)
	}
	f, fi, err := s.Open(ctx, "images/a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "one" || fi.Size() != 3 {
		t.Fatalf("open: %q %d", b, fi.Size())
	}
	if ok, err := s.Exists(ctx, "images/a/b.txt"); !ok || err != nil {
		t.Fatalf("exists=%v %v", ok, err)
	}
	if ok, _ := s.Exists(ctx, "images/a"); ok {
		t.Fatal("хавтас файл болж харагдлаа")
	}
	if _, _, err := s.Open(ctx, "images/a"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("хавтас Open: %v", err)
	}
	if _, _, err := s.Open(ctx, "images/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err=%v", err)
	}
	for _, p := range []string{"../x", "/etc/passwd", "a/../../x", "a\x00b"} {
		if err := s.Save(ctx, p, strings.NewReader("x")); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("Save(%q) err=%v", p, err)
		}
		if _, _, err := s.Open(ctx, p); err == nil {
			t.Errorf("Open(%q) амжилттай болов", p)
		}
		if ok, _ := s.Exists(ctx, p); ok {
			t.Errorf("Exists(%q)", p)
		}
	}
}

type errReader struct{ n int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.n == 0 {
		return 0, errors.New("boom")
	}
	e.n--
	return copy(p, "abc"), nil
}

func TestLocalStorageFailedSaveLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewLocalStorage(dir)
	defer s.Close()
	if err := s.Save(context.Background(), "x/y.bin", &errReader{n: 2}); err == nil {
		t.Fatal("алдаа хүлээсэн")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("хагас файл үлдсэн: %v", files)
	}
	// Нэр дахин ашиглагдах боломжтой.
	if err := s.Save(context.Background(), "x/y.bin", bytes.NewReader([]byte("ok"))); err != nil {
		t.Fatal(err)
	}
}

func TestLocalStorageSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, _ := NewLocalStorage(dir)
	defer s.Close()
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Хавтас symlink гадагш.
	if err := os.Symlink(outside, filepath.Join(dir, "images", "evil")); err != nil {
		t.Skip("symlink дэмжигдэхгүй:", err)
	}
	// Файл symlink гадагш.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "images", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	// Абсолют symlink хавтас.
	if err := os.Symlink("/etc", filepath.Join(dir, "images", "etc")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"images/evil/secret.txt", "images/leak.txt", "images/etc/passwd"} {
		if f, _, err := s.Open(ctx, p); err == nil {
			f.Close()
			t.Errorf("Open(%q) root-оос гадагш гарлаа", p)
		}
		if ok, _ := s.Exists(ctx, p); ok {
			t.Errorf("Exists(%q) = true", p)
		}
	}
	if err := s.Save(ctx, "images/evil/new.txt", strings.NewReader("pwn")); err == nil {
		t.Error("symlink хавтсаар root-оос гадуур бичигдлээ")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("outside/new.txt үүссэн")
	}
	// Файл symlink-ийн нэр дээр бичихийг оролдох (O_EXCL symlink-ийг дагахгүй).
	if err := s.Save(ctx, "images/leak.txt", strings.NewReader("pwn")); err == nil {
		t.Error("symlink файл дээр бичигдлээ")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "TOP SECRET" {
		t.Errorf("гадаад файл өөрчлөгдсөн: %q", b)
	}
	// Root доторх symlink зөвшөөрөгдөнө.
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "real", "ok.txt"), []byte("fine"), 0o644)
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := s.Open(ctx, "alias/ok.txt"); err != nil {
		t.Errorf("root доторх symlink: %v", err)
	} else {
		f.Close()
	}
}
