package upload

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Save-ийн бичилт дууссан ч эцсийн нэр гарахаас өмнөх мөчид эцсийн нэр байх ЁСГҮЙ;
// түр файл нуугдмал нэртэй бөгөөд public Serve-ээр хүрэгдэхгүй.
func TestSaveNeverExposesIncompleteFinalPath(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	final := filepath.Join(dir, "images", "a.txt")
	called := false
	s.beforePublish = func(rel string) {
		called = true
		if _, err := os.Lstat(final); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("эцсийн нэр эрт гарсан: %v", err)
		}
		ents, _ := os.ReadDir(filepath.Dir(final))
		if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), tmpPrefix) {
			t.Errorf("түр файл хүлээсэн: %v", ents)
		}
	}
	if err := s.Save(context.Background(), "images/a.txt", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("hook дуудагдсангүй")
	}
	b, _ := os.ReadFile(final)
	if string(b) != "hello" {
		t.Fatalf("агуулга=%q", b)
	}
	ents, _ := os.ReadDir(filepath.Dir(final))
	if len(ents) != 1 {
		t.Fatalf("түр файл үлдсэн: %v", ents)
	}
}

// Олон Save зэрэг нэг нэр рүү: яг нэг нь амжилттай, бусад нь ErrExist, агуулга бүрэн.
func TestSaveConcurrentNoClobber(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewLocalStorage(dir)
	defer s.Close()
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Save(context.Background(), "x/same.bin", bytes.NewReader(bytes.Repeat([]byte{byte('a' + i)}, 4096)))
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case !errors.Is(e, fs.ErrExist):
			t.Fatalf("гэнэтийн алдаа: %v", e)
		}
	}
	if ok != 1 {
		t.Fatalf("амжилттай=%d", ok)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "x", "same.bin"))
	if len(b) != 4096 || bytes.Count(b, b[:1]) != 4096 {
		t.Fatal("файл бүрэн/цэвэр биш")
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "x"))
	if len(ents) != 1 {
		t.Fatalf("түр файл үлдсэн: %v", ents)
	}
}

// Уншилтын алдаанд эцсийн нэр ч, түр файл ч үлдэхгүй.
func TestSaveFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewLocalStorage(dir)
	defer s.Close()
	err := s.Save(context.Background(), "images/b.txt", &failReader{})
	if err == nil {
		t.Fatal("алдаа хүлээсэн")
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "images"))
	if len(ents) != 0 {
		t.Fatalf("үлдэгдэл: %v", ents)
	}
}

type failReader struct{ n int }

func (f *failReader) Read(p []byte) (int, error) {
	if f.n++; f.n > 1 {
		return 0, errors.New("boom")
	}
	return copy(p, "partial"), nil
}

// Хуучин (crash-ийн) түр файл public Serve-ээр хүрэгдэхгүй; 0 байт файл immutable кэшгүй.
func TestServeHidesTmpAndDoesNotCacheEmpty(t *testing.T) {
	u := newTestUploader(t)
	putFile(t, u, "images/2026/November/.khan-tmp-deadbeef", []byte("partial"))
	putFile(t, u, "documents/empty.txt", nil)
	app := newApp(t, u, false)
	resp, _ := do(t, app, httpGet("/uploaded/images/2026/November/.khan-tmp-deadbeef"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	resp, _ = do(t, app, httpGet("/uploaded/documents/empty.txt"))
	if cc := resp.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Fatalf("0 байт файл immutable кэшлэгдсэн: %q", cc)
	}
}

func httpGet(p string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, p, nil)
	return r
}

// Signer алдаатай үед Handler 500 өгнө (файл хадгалагдсан), Store Result.Path + алдаа буцаана.
func TestPrivateSignFailureIs500(t *testing.T) {
	e := newPrivEnv(t, nil)
	e.u.privTTL = -1 // Sign: ttl эерэг биш → алдаа
	data := []byte("%PDF-1.4\n%%EOF\n")
	resp, _ := do(t, e.app, privUploadReq(t, "/api/upload/private", nil, "a.pdf", "application/pdf", data, "7", "11111111-1111-1111-1111-111111111111"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	res, err := e.u.StoreFile(context.Background(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: "11111111-1111-1111-1111-111111111111"}, bytes.NewReader(data), int64(len(data)))
	if err == nil || res.Path == "" || res.URL != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if ok, _ := e.u.privStorage.Exists(context.Background(), strings.TrimPrefix(res.Path, privateDir+"/")); !ok {
		t.Fatal("файл хадгалагдсан байх ёстой")
	}
}

// Symlink-ээр нэг хавтас руу заасан public/private зам давхцсанд New алдаа өгнө.
func TestNewRejectsSymlinkedOverlap(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "uploaded"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(root, "uploaded"), link); err != nil {
		t.Skip("symlink дэмжигдэхгүй")
	}
	// private = public-ийн alias
	if _, err := New(Config{Root: root, PrivateRoot: link, MaxBytes: 1 << 20}); err == nil {
		t.Fatal("alias давхцал хүлээсэн")
	}
	// private = alias/sub (public дотор, одоогоор байхгүй)
	if _, err := New(Config{Root: root, PrivateRoot: filepath.Join(link, "sub"), MaxBytes: 1 << 20}); err == nil {
		t.Fatal("alias/sub давхцал хүлээсэн")
	}
	// Root өөрөө symlink, private нь бодит замаар root дотор.
	rootLink := filepath.Join(base, "rootlink")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Root: rootLink, PrivateRoot: filepath.Join(root, "uploaded", "p"), MaxBytes: 1 << 20}); err == nil {
		t.Fatal("root symlink давхцал хүлээсэн")
	}
	// Зөв тусдаа хавтас OK.
	u, err := New(Config{Root: root, PrivateRoot: filepath.Join(base, "priv"), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_ = u.Close()
}
