package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/cache"
	"github.com/khan-vision/khan/events"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// ─── туслахууд ───

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// cfx — cache-тэй fixture: fx + хуурамч цагтай Cache + DB query тоолуур.
type cfx struct {
	*fx
	c   *cache.Cache
	clk *testClock
	n   *atomic.Int64
}

func newCfx(t *testing.T) *cfx {
	t.Helper()
	f := newFx(t)
	clk := &testClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	c, err := cache.New(cache.Config{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return &cfx{fx: f, c: c, clk: clk, n: countQueries(f.db)}
}

var cbSeq atomic.Int64

// countQueries — DB-д хүрсэн SELECT/Row-г тоолно (304/hit-д DB хүрэхгүйг батлах).
func countQueries(db *gorm.DB) *atomic.Int64 {
	var n atomic.Int64
	inc := func(*gorm.DB) { n.Add(1) }
	id := cbSeq.Add(1)
	_ = db.Callback().Query().Before("gorm:query").Register(fmt.Sprintf("khan:test_cnt_q_%d", id), inc)
	_ = db.Callback().Row().Before("gorm:row").Register(fmt.Sprintf("khan:test_cnt_r_%d", id), inc)
	return &n
}

func (f *cfx) spec() *CacheSpec { return &CacheSpec{Cache: f.c} }

func (f *cfx) room(spec *CacheSpec) *Resource[map[string]any] {
	r := f.roomResource()
	if spec == nil {
		spec = f.spec()
	}
	r.Cache = spec
	return r
}

type hresp struct {
	resp
	H http.Header
}

func (h hresp) cache() string { return h.H.Get("X-Khan-Cache") }
func (h hresp) etag() string  { return h.H.Get("ETag") }

// doH — do(), гэхдээ толгой хүсэж/буцаана.
func doH(t *testing.T, app *fiber.App, method, path, org string, role int, body any, hdr map[string]string) hresp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-T-Org", org)
		req.Header.Set("X-T-Role", fmt.Sprint(role))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	out := hresp{resp: resp{Status: res.StatusCode, Raw: raw}, H: res.Header}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

func get(t *testing.T, app *fiber.App, path, org string, role int) hresp {
	t.Helper()
	return doH(t, app, "GET", path, org, role, nil, nil)
}

func names(r resp) []string {
	var out []string
	for _, row := range r.list("data") {
		out = append(out, fmt.Sprint(row["name"]))
	}
	return out
}

func wantNames(t *testing.T, r resp, want ...string) {
	t.Helper()
	got := names(r)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("жагсаалт %v, хүлээсэн %v: %s", got, want, r.Raw)
	}
}

func mkRoom(t *testing.T, app *fiber.App, org, name string) string {
	t.Helper()
	r := do(t, app, "POST", "/rooms", org, 1, map[string]any{"name": name})
	mustStatus(t, r, 201)
	return r.str("id")
}

// ─── unit (DB-гүй) ───

func TestCacheHelpersUnit(t *testing.T) {
	if canBits(map[string]bool{"read": true, "update": true}) != "1010" || canBits(nil) != "0000" {
		t.Error("canBits")
	}
	for in, want := range map[string]string{
		"":                            "",
		"  ":                          "",
		"{}":                          "",
		" { } ":                       "",
		`{"b":{"v":1},"a":{"v":"x"}}`: `{"a":{"v":"x"},"b":{"v":1}}`,
		"{ \"a\" : { \"op\":\"eq\" , \"v\": 1.0 } }": `{"a":{"op":"eq","v":1.0}}`,
		`{"a":1} junk`: `{"a":1} junk`, // задлагдахгүй → түүхий (query 400 өгнө)
		`not json`:     `not json`,
		`{"a":`:        `{"a":`,
	} {
		if got := canonFilters(in); got != want {
			t.Errorf("canonFilters(%q)=%q, want %q", in, got, want)
		}
	}
	// listParts нь query-ийн өөрийн хэвшүүлэлтээр: ижил үр дүнтэй хүсэлт → ижил түлхүүр.
	r := &Resource[map[string]any]{Name: "t.x", Table: "t.x",
		Fields: []Field{{Name: "name", Kind: validate.String}},
		List:   query.Spec{Sort: map[string]string{"name": "t.name"}, MaxLimit: 100}}
	e, err := r.build(false)
	if err != nil {
		t.Fatal(err)
	}
	key := func(req query.Request) string {
		p, ok := e.listParts(req)
		if !ok {
			return "<bypass>"
		}
		return strings.Join(p, "|")
	}
	same := [][2]query.Request{
		{{Page: 1, Search: "x", Filters: `{"b":1, "a":2}`}, {Page: 1, Search: "x", Filters: `{"a":2,"b":1}`}},
		{{Page: 0}, {Page: 1}},                                     // page<1 → 1
		{{Limit: 99999}, {Limit: 100}},                             // MaxLimit-ээр хязгаарлагдана
		{{Limit: 0}, {Limit: 25}},                                  // анхдагч limit
		{{Search: "  x "}, {Search: "x"}},                          // trim
		{{Sort: "nope", Dir: "desc"}, {}},                          // whitelist-гүй sort → DefaultOrder
		{{Sort: "name", Dir: "DESC"}, {Sort: "name", Dir: "desc"}}, // dir жижиг/том
	}
	for i, pair := range same {
		if key(pair[0]) != key(pair[1]) {
			t.Errorf("same[%d]: %q ≠ %q", i, key(pair[0]), key(pair[1]))
		}
	}
	diff := [][2]query.Request{
		{{Page: 1, Search: "x"}, {Page: 2, Search: "x"}},
		{{Sort: "name", Dir: "asc"}, {Sort: "name", Dir: "desc"}},
		{{Limit: 10}, {Limit: 20}},
		{{Search: "a"}, {Search: "b"}},
	}
	for i, pair := range diff {
		if key(pair[0]) == key(pair[1]) {
			t.Errorf("diff[%d]: ижил түлхүүр %q", i, key(pair[0]))
		}
	}
	if key(query.Request{Search: strings.Repeat("я", maxCachedSearch+1)}) != "<bypass>" ||
		key(query.Request{Page: maxCachedPage + 1}) != "<bypass>" {
		t.Error("урт хайлт / хол хуудсыг cache хийх ёсгүй")
	}
}

func TestWithCanUnit(t *testing.T) {
	app := fiber.New()
	var got []byte
	var gerr error
	app.Get("/", func(c fiber.Ctx) error {
		w := (&engine{}).withCan(c, map[string]bool{"read": true, "create": false})
		got, gerr = w([]byte(`{"data":[],"total":0}`))
		if gerr != nil {
			return gerr
		}
		empty, err := w([]byte(`{}`))
		if err != nil || string(empty) != `{"can":{"create":false,"read":true}}` {
			t.Errorf("хоосон объект: %s %v", empty, err)
		}
		if _, err := w([]byte(`[1]`)); err == nil {
			t.Error("массив body алдаа өгөх ёстой")
		}
		if _, err := w(nil); err == nil {
			t.Error("хоосон body алдаа өгөх ёстой")
		}
		return nil
	})
	if _, err := app.Test(httptest.NewRequest("GET", "/", nil), fiber.TestConfig{Timeout: 0}); err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"can":{"create":false,"read":true},"data":[],"total":0}` {
		t.Errorf("%s", got)
	}
}

func TestCacheSpecValidation(t *testing.T) {
	f := newFx(t)
	c, _ := cache.New(cache.Config{})
	for name, spec := range map[string]*CacheSpec{
		"Cache nil":        {},
		"TTL сөрөг":        {Cache: c, TTL: -1},
		"DependsOn буруу":  {Cache: c, DependsOn: []string{"Bad Name"}},
		"Bumps буруу":      {Cache: c, Bumps: []string{"a:b"}},
		"DependsOn хоосон": {Cache: c, DependsOn: []string{""}},
	} {
		r := f.roomResource()
		r.Cache = spec
		if err := r.Check(); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
		if err := r.Mount(fiber.New()); err == nil {
			t.Errorf("%s: Mount алдаа хүлээсэн", name)
		}
	}
	r := f.roomResource()
	r.Cache = &CacheSpec{Cache: c, DependsOn: []string{"test.room", "x.y", "x.y"}, Bumps: []string{"a.b", "test.room"}, TTL: time.Second}
	e, err := r.build(true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.cache.dependsOn, ",") != "x.y" || strings.Join(e.cache.bumpNames, ",") != "test.room,a.b" {
		t.Errorf("dedupe: deps=%v bumps=%v", e.cache.dependsOn, e.cache.bumpNames)
	}
}

// ─── hit / miss / байт-байтаар ижил ───

func TestCacheHitMissAndIdenticalBody(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	plain := mountOn(t, "/plain", f.roomResource()) // cache-гүй, ижил өгөгдөл
	for _, n := range []string{"b", "a", "c"} {
		do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": n, "price": "9999999999999999.99"})
	}

	first := get(t, app, "/rooms", f.orgA, 1)
	mustStatus(t, first.resp, 200)
	if first.cache() != "miss" || first.etag() == "" {
		t.Fatalf("1-р хүсэлт: cache=%q etag=%q", first.cache(), first.etag())
	}
	wantNames(t, first.resp, "a", "b", "c")
	if first.H.Get("Cache-Control") != "private, no-cache" || !strings.Contains(first.H.Get("Vary"), "Authorization") || !strings.Contains(first.H.Get("Vary"), "Cookie") {
		t.Errorf("толгойнууд: %v", first.H)
	}
	if !strings.HasPrefix(first.H.Get("Content-Type"), "application/json") {
		t.Errorf("content-type: %q", first.H.Get("Content-Type"))
	}

	q0 := f.n.Load()
	second := get(t, app, "/rooms", f.orgA, 1)
	if second.cache() != "hit" || f.n.Load() != q0 {
		t.Fatalf("2-р хүсэлт: cache=%q DB query %d→%d", second.cache(), q0, f.n.Load())
	}
	if !bytes.Equal(first.Raw, second.Raw) || first.etag() != second.etag() {
		t.Error("hit-ийн body/ETag miss-тэй ижил биш")
	}

	// cache-гүй resource-той БАЙТ-БАЙТААР ижил (can, decimal overlay, filterable...). Gen/ID-аас болж
	// өөр өгөгдөл тул ижил өгөгдөл дээр нь харьцуулъя: cache-гүй route нь ижил хүснэгтийг уншина.
	un := get(t, plain, "/plain", f.orgA, 1)
	if un.cache() != "" || un.etag() != "" {
		t.Errorf("cache-гүй resource толгой нэмсэн: %v", un.H)
	}
	if !bytes.Equal(first.Raw, un.Raw) {
		t.Fatalf("cache-тэй ≠ cache-гүй:\n%s\n%s", first.Raw, un.Raw)
	}
	if !strings.Contains(string(first.Raw), `"price":9999999999999999.99`) {
		t.Errorf("decimal overlay: %s", first.Raw)
	}

	// POST /query ижил хүсэлтэд ижил түлхүүр (GET-тэй), filters-ийн хэлбэр/дараалал үл хамаарна
	post := doH(t, app, "POST", "/rooms/query", f.orgA, 1, map[string]any{"page": 1}, nil)
	if post.cache() != "hit" || !bytes.Equal(post.Raw, first.Raw) {
		t.Errorf("POST /query: cache=%q", post.cache())
	}
	fa := doH(t, app, "POST", "/rooms/query", f.orgA, 1, `{"filters":{"status":{"op":"eq","v":"draft"},"capacity":{"op":"gte","v":0}}}`, nil)
	fb := doH(t, app, "POST", "/rooms/query", f.orgA, 1, `{"filters":"{ \"capacity\":{\"op\":\"gte\",\"v\":0}, \"status\":{\"op\":\"eq\",\"v\":\"draft\"} }"}`, nil)
	mustStatus(t, fa.resp, 200)
	if fa.cache() != "miss" || fb.cache() != "hit" || !bytes.Equal(fa.Raw, fb.Raw) {
		t.Errorf("filters нормчлол: %q %q", fa.cache(), fb.cache())
	}
	// өөр page/search/sort → өөр entry
	for _, p := range []string{"/rooms?page=2&limit=2", "/rooms?search=a", "/rooms?sort=name&dir=desc", "/rooms?limit=1"} {
		if r := get(t, app, p, f.orgA, 1); r.cache() != "miss" {
			t.Errorf("%s: %q", p, r.cache())
		}
		if r := get(t, app, p, f.orgA, 1); r.cache() != "hit" {
			t.Errorf("%s 2-р: %q", p, r.cache())
		}
	}
	if r := get(t, app, "/rooms?sort=name&dir=desc", f.orgA, 1); strings.Join(names(r.resp), "") != "cba" {
		t.Errorf("sort desc: %s", r.Raw)
	}
	// meta болон export-д cache толгой байхгүй
	if m := get(t, app, "/rooms/meta", f.orgA, 1); m.Status != 200 || m.cache() != "" || m.etag() != "" {
		t.Errorf("meta: %v", m.H)
	}
}

func TestCacheDetailWithChildrenIdentical(t *testing.T) {
	f := newCfx(t)
	cached := f.invoiceResource()
	cached.Cache = f.spec()
	app := mountOn(t, "/inv", cached)
	plain := mountOn(t, "/plain", f.invoiceResource())
	item := f.person(f.orgA, "Бараа", 1)
	c := do(t, app, "POST", "/inv", f.orgA, 1, map[string]any{"number": "INV-1",
		"lines": []map[string]any{{"item_id": item, "qty": 2, "price": "9999999999999999.99", "position": 1}},
		"notes": []map[string]any{{"body": "тэмдэглэл"}}})
	mustStatus(t, c, 201)
	id := c.str("id")

	d1 := get(t, app, "/inv/"+id, f.orgA, 1)
	d2 := get(t, app, "/inv/"+id, f.orgA, 1)
	pl := get(t, plain, "/plain/"+id, f.orgA, 1)
	mustStatus(t, d1.resp, 200)
	if d1.cache() != "miss" || d2.cache() != "hit" || !bytes.Equal(d1.Raw, d2.Raw) || !bytes.Equal(d1.Raw, pl.Raw) {
		t.Fatalf("detail: %q %q\n%s\n%s", d1.cache(), d2.cache(), d1.Raw, pl.Raw)
	}
	if !strings.Contains(string(d1.Raw), `"price":9999999999999999.99`) || !strings.Contains(string(d1.Raw), "тэмдэглэл") {
		t.Errorf("children/decimal: %s", d1.Raw)
	}
	if d1.etag() == "" || d1.H.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("толгой: %v", d1.H)
	}
	// children өөрчлөгдөх (parent PUT) → detail шинэчлэгдэнэ
	up := do(t, app, "PUT", "/inv/"+id, f.orgA, 1, map[string]any{"version": 1, "notes": []map[string]any{{"body": "шинэ"}}})
	mustStatus(t, up, 200)
	d3 := get(t, app, "/inv/"+id, f.orgA, 1)
	if d3.cache() != "miss" || !strings.Contains(string(d3.Raw), "шинэ") || strings.Contains(string(d3.Raw), "тэмдэглэл") {
		t.Fatalf("PUT-ийн дараа: %q %s", d3.cache(), d3.Raw)
	}
	if !bytes.Equal(d3.Raw, up.Raw) {
		t.Errorf("detail ≠ PUT-ийн хариу:\n%s\n%s", d3.Raw, up.Raw)
	}
}

// ─── org тусгаарлалт ───

func TestCacheOrgIsolation(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	idA := mkRoom(t, app, f.orgA, "A-ийн өрөө")
	mkRoom(t, app, f.orgB, "B-ийн өрөө")

	a := get(t, app, "/rooms", f.orgA, 1) // A-г халаана
	wantNames(t, a.resp, "A-ийн өрөө")
	b := get(t, app, "/rooms", f.orgB, 1)
	if b.cache() != "miss" || strings.Contains(string(b.Raw), "A-ийн") {
		t.Fatalf("B нь A-ийн cache-ийг харлаа: %q %s", b.cache(), b.Raw)
	}
	wantNames(t, b.resp, "B-ийн өрөө")
	if a.etag() == b.etag() {
		t.Error("өөр org-д ижил ETag")
	}
	// B нь A-ийн ETag-аар 304 авч чадахгүй
	r := doH(t, app, "GET", "/rooms", f.orgB, 1, nil, map[string]string{"If-None-Match": a.etag()})
	if r.Status != 200 || strings.Contains(string(r.Raw), "A-ийн") {
		t.Errorf("A-ийн ETag B-д 304/алдагдсан: %d %s", r.Status, r.Raw)
	}
	// A-ийн detail-ийг B-ийн халуун cache-тэй ч 404
	get(t, app, "/rooms/"+idA, f.orgA, 1)
	if d := get(t, app, "/rooms/"+idA, f.orgB, 1); d.Status != 404 || d.cache() != "" {
		t.Errorf("A-ийн detail B-д: %d %s", d.Status, d.Raw)
	}
	// 404 A-ийн cache-д нөлөөлөхгүй
	if d := get(t, app, "/rooms/"+idA, f.orgA, 1); d.Status != 200 || d.cache() != "hit" {
		t.Errorf("A detail: %d %q", d.Status, d.cache())
	}
	// A-ийн бичилт B-ийн cache-ийг хүчингүй болгохгүй
	mkRoom(t, app, f.orgA, "A2")
	if b2 := get(t, app, "/rooms", f.orgB, 1); b2.cache() != "hit" {
		t.Errorf("A-ийн бичилт B-ийн cache-ийг хүчингүй болгосон: %q", b2.cache())
	}
	if a2 := get(t, app, "/rooms", f.orgA, 1); a2.cache() != "miss" {
		t.Errorf("A-ийн cache шинэчлэгдсэнгүй: %q", a2.cache())
	}
	// гарын авлагаар: хоёр org-ийн cache entry-үүд бие даасан
	if wantA := get(t, app, "/rooms", f.orgA, 1); strings.Contains(string(wantA.Raw), "B-ийн") {
		t.Error("A нь B-г харлаа")
	}
}

// ─── write → invalidate ───

func TestCacheInvalidatedByWrites(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	idA := mkRoom(t, app, f.orgA, "r1")
	mkRoom(t, app, f.orgB, "b1")
	warm := func() {
		t.Helper()
		get(t, app, "/rooms", f.orgA, 1)
		get(t, app, "/rooms/"+idA, f.orgA, 1)
		get(t, app, "/rooms", f.orgB, 1)
	}
	fresh := func(what string) {
		t.Helper()
		if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "miss" {
			t.Errorf("%s: list cache=%q", what, l.cache())
		}
		if d := get(t, app, "/rooms/"+idA, f.orgA, 1); d.cache() != "miss" && d.Status == 200 {
			t.Errorf("%s: detail cache=%q", what, d.cache())
		}
		if l := get(t, app, "/rooms", f.orgB, 1); l.cache() != "hit" {
			t.Errorf("%s: B-ийн cache хүчингүй болсон: %q", what, l.cache())
		}
	}

	warm()
	idNew := mkRoom(t, app, f.orgA, "r2")
	fresh("create")
	wantNames(t, get(t, app, "/rooms", f.orgA, 1).resp, "r1", "r2")

	warm()
	mustStatus(t, do(t, app, "PUT", "/rooms/"+idA, f.orgA, 1, map[string]any{"name": "r1-new", "version": 1}), 200)
	fresh("update")
	wantNames(t, get(t, app, "/rooms", f.orgA, 1).resp, "r1-new", "r2")
	if d := get(t, app, "/rooms/"+idA, f.orgA, 1); d.str("name") != "r1-new" {
		t.Errorf("detail хуучин: %s", d.Raw)
	}

	warm()
	bk := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "update", "ids": []string{idA}, "set": map[string]any{"capacity": 77}})
	mustStatus(t, bk, 200)
	fresh("bulk update")
	if d := get(t, app, "/rooms/"+idA, f.orgA, 1); d.JSON["capacity"] != float64(77) {
		t.Errorf("bulk-ийн дараа detail хуучин: %s", d.Raw)
	}

	warm()
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": []string{idNew}}), 200)
	fresh("bulk delete")
	wantNames(t, get(t, app, "/rooms", f.orgA, 1).resp, "r1-new")

	warm()
	mustStatus(t, do(t, app, "DELETE", "/rooms/"+idA, f.orgA, 1, nil), 200)
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "miss" || len(l.list("data")) != 0 {
		t.Errorf("delete: %q %s", l.cache(), l.Raw)
	}
	if d := get(t, app, "/rooms/"+idA, f.orgA, 1); d.Status != 404 {
		t.Errorf("устсан мөр detail-д: %d %s", d.Status, d.Raw)
	}
	if l := get(t, app, "/rooms", f.orgB, 1); l.cache() != "hit" {
		t.Errorf("B: %q", l.cache())
	}
}

func TestCacheBulkHooksDeletePathInvalidates(t *testing.T) {
	f := newCfx(t)
	r := f.room(nil)
	var deleted atomic.Int32
	r.Hooks.AfterDelete = func(x *Ctx) error { deleted.Add(1); return nil } // bulk delete → deleteOne (мөр бүрээр)
	app := mountOn(t, "/rooms", r)
	id := mkRoom(t, app, f.orgA, "x")
	get(t, app, "/rooms", f.orgA, 1)
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": []string{id}}), 200)
	if deleted.Load() != 1 {
		t.Fatal("hook ажиллаагүй")
	}
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "miss" || len(l.list("data")) != 0 {
		t.Errorf("%q %s", l.cache(), l.Raw)
	}
}

// ─── DependsOn / Bumps ───

func TestCacheDependsOnAndBumps(t *testing.T) {
	f := newCfx(t)
	tag := f.tagResource()
	tag.Cache = f.spec()
	inv := f.invoiceResource()
	inv.Cache = &CacheSpec{Cache: f.c, Bumps: []string{"test.room"}}
	room := f.room(&CacheSpec{Cache: f.c, DependsOn: []string{"test.tag"}})

	tagApp := mountOn(t, "/tags", tag)
	invApp := mountOn(t, "/inv", inv)
	roomApp := mountOn(t, "/rooms", room)
	mkRoom(t, roomApp, f.orgA, "r")

	warm := func() {
		t.Helper()
		get(t, roomApp, "/rooms", f.orgA, 1)
		if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "hit" {
			t.Fatal("халаагдсангүй")
		}
	}

	// DependsOn: tag бичилт → room cache хүчингүй
	warm()
	mustStatus(t, do(t, tagApp, "POST", "/tags", f.orgA, 1, map[string]any{"label": "t"}), 201)
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "miss" {
		t.Errorf("DependsOn: %q", l.cache())
	}
	// өөр org-ийн tag бичилт нөлөөлөхгүй
	warm()
	mustStatus(t, do(t, tagApp, "POST", "/tags", f.orgB, 1, map[string]any{"label": "t"}), 201)
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "hit" {
		t.Errorf("өөр org-ийн DependsOn bump: %q", l.cache())
	}
	// DependsOn-д байхгүй resource (invoice нь room-д Bumps-ээр л холбогдсон ч) — эхлээд байхгүй гэж үзье
	// room.write → tag cache-д нөлөөгүй (чиглэл)
	get(t, tagApp, "/tags", f.orgA, 1)
	mkRoom(t, roomApp, f.orgA, "r2")
	if l := get(t, tagApp, "/tags", f.orgA, 1); l.cache() != "hit" {
		t.Errorf("room бичилт tag cache-г хүчингүй болгосон: %q", l.cache())
	}

	// Bumps: invoice бичилт → room cache хүчингүй
	warm()
	mustStatus(t, do(t, invApp, "POST", "/inv", f.orgA, 1, map[string]any{"number": "N1"}), 201)
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "miss" {
		t.Errorf("Bumps: %q", l.cache())
	}

	// Гадаад бичилт: cache.Bump(ctx, org, "test.tag") → room шинэчлэгдэнэ
	warm()
	if err := f.c.Bump(context.Background(), f.orgA, "test.tag"); err != nil {
		t.Fatal(err)
	}
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "miss" {
		t.Errorf("гадаад Bump: %q", l.cache())
	}
}

// ─── ETag / 304 ───

func TestCacheETag304(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	id := mkRoom(t, app, f.orgA, "e1")

	l := get(t, app, "/rooms", f.orgA, 1)
	d := get(t, app, "/rooms/"+id, f.orgA, 1)
	if l.etag() == "" || d.etag() == "" || l.etag() == d.etag() {
		t.Fatalf("ETag-ууд: %q %q", l.etag(), d.etag())
	}
	if !regexp.MustCompile(`^W/"(s[0-9a-f]{8}\.)?g[0-9]`).MatchString(l.etag()) {
		t.Errorf("ETag хэлбэр: %s", l.etag())
	}

	q0 := f.n.Load()
	n := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": l.etag()})
	if n.Status != 304 || len(n.Raw) != 0 || n.etag() != l.etag() || n.cache() != "hit" {
		t.Fatalf("304: status=%d body=%q etag=%q cache=%q", n.Status, n.Raw, n.etag(), n.cache())
	}
	if f.n.Load() != q0 {
		t.Fatalf("304 нь DB-д хүрсэн: %d → %d", q0, f.n.Load())
	}
	if n.H.Get("Cache-Control") != "private, no-cache" || !strings.Contains(n.H.Get("Vary"), "Authorization") {
		t.Errorf("304 толгой: %v", n.H)
	}
	// 304 нь cache entry байхгүй (L1 шахагдсан гэх мэт) үед ч DB-гүй
	for _, h := range []string{l.etag(), strings.TrimPrefix(l.etag(), "W/"), `"x", ` + l.etag()} {
		if r := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": h}); r.Status != 304 {
			t.Errorf("If-None-Match %q → %d", h, r.Status)
		}
	}
	// detail 304
	if r := doH(t, app, "GET", "/rooms/"+id, f.orgA, 1, nil, map[string]string{"If-None-Match": d.etag()}); r.Status != 304 || f.n.Load() != q0 {
		t.Errorf("detail 304: %d", r.Status)
	}
	// list-ийн ETag detail-д таарахгүй
	if r := doH(t, app, "GET", "/rooms/"+id, f.orgA, 1, nil, map[string]string{"If-None-Match": l.etag()}); r.Status != 200 {
		t.Errorf("list ETag detail-д: %d", r.Status)
	}
	// `*` ба буруу ETag → 200
	for _, h := range []string{"*", `W/"g0-0000000000000000"`, "garbage"} {
		if r := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": h}); r.Status != 200 {
			t.Errorf("If-None-Match %q → %d", h, r.Status)
		}
	}
	// POST /query: ETag тавина, гэхдээ 304 БИШ (RFC 9110)
	p := doH(t, app, "POST", "/rooms/query", f.orgA, 1, map[string]any{"page": 1}, map[string]string{"If-None-Match": l.etag()})
	if p.Status != 200 || p.etag() != l.etag() {
		t.Errorf("POST /query: %d etag=%q", p.Status, p.etag())
	}
	// HEAD
	if r := doH(t, app, "HEAD", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": l.etag()}); r.Status != 304 {
		t.Errorf("HEAD: %d", r.Status)
	}
	// өөр хүсэлт → өөр ETag
	if o := get(t, app, "/rooms?limit=1", f.orgA, 1); o.etag() == l.etag() {
		t.Error("өөр query ижил ETag")
	}
	// бичилт → ETag өөрчлөгдөж, хуучин нь 304 өгөхгүй
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "e2", "version": 1}), 200)
	r := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": l.etag()})
	if r.Status != 200 || r.etag() == l.etag() || !strings.Contains(string(r.Raw), "e2") {
		t.Fatalf("бичилтийн дараа хуучин ETag: %d %q %s", r.Status, r.etag(), r.Raw)
	}
	if r2 := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": r.etag()}); r2.Status != 304 {
		t.Errorf("шинэ ETag: %d", r2.Status)
	}

	// TTL (epoch): хугацаа өнгөрвөл bump хийгдээгүй ч ETag шинэчлэгдэнэ → 304 хязгааргүй хүчинтэй биш
	f.clk.Advance(cache.DefaultTTL + time.Second)
	r3 := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": r.etag()})
	if r3.Status != 200 || r3.etag() == r.etag() || r3.cache() != "miss" {
		t.Errorf("TTL-ийн дараа: %d etag=%q cache=%q", r3.Status, r3.etag(), r3.cache())
	}
}

func TestCacheNoETag(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(&CacheSpec{Cache: f.c, NoETag: true}))
	mkRoom(t, app, f.orgA, "n")
	a := get(t, app, "/rooms", f.orgA, 1)
	b := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": "*"})
	if a.etag() != "" || a.cache() != "miss" || b.cache() != "hit" || b.Status != 200 {
		t.Errorf("NoETag: etag=%q %q %q %d", a.etag(), a.cache(), b.cache(), b.Status)
	}
	if a.H.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("Cache-Control: %v", a.H)
	}
	// ETag үүсгэсэн үед ч (өөр спек) "ETag"-ийг ямар ч If-None-Match-ээр 304 болгохгүй
	if r := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": `W/"g1-abc"`}); r.Status != 200 {
		t.Errorf("%d", r.Status)
	}
}

// ─── эрх ───

func TestCachePermissions(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	id := mkRoom(t, app, f.orgA, "p")

	admin := get(t, app, "/rooms", f.orgA, 1) // role 1: бүх эрх — cache-г халаана
	reader := get(t, app, "/rooms", f.orgA, 2)
	if reader.cache() != "hit" {
		t.Errorf("reader cache=%q", reader.cache())
	}
	// can нь cache-ээс БИШ, хүсэлт бүрт тооцогдоно
	ac, rc := admin.JSON["can"].(map[string]any), reader.JSON["can"].(map[string]any)
	if ac["create"] != true || ac["delete"] != true || rc["create"] != false || rc["delete"] != false || rc["read"] != true {
		t.Fatalf("can: admin=%v reader=%v", ac, rc)
	}
	mid := get(t, app, "/rooms", f.orgA, 3)
	if mc := mid.JSON["can"].(map[string]any); mc["update"] != true || mc["delete"] != false {
		t.Errorf("role 3 can: %v", mc)
	}
	// Эрхийн хэв өөр → ETag өөр
	if admin.etag() == reader.etag() || admin.etag() == mid.etag() || reader.etag() == mid.etag() {
		t.Errorf("ETag эрхийн хэв агуулаагүй: %q %q %q", admin.etag(), reader.etag(), mid.etag())
	}
	// admin-ий ETag-аар reader 304 авч чадахгүй (can өөр)
	if r := doH(t, app, "GET", "/rooms", f.orgA, 2, nil, map[string]string{"If-None-Match": admin.etag()}); r.Status != 200 {
		t.Errorf("өөр эрхийн ETag-д 304: %d", r.Status)
	}
	// Ижил эрхийн хэв → 304
	if r := doH(t, app, "GET", "/rooms", f.orgA, 2, nil, map[string]string{"If-None-Match": reader.etag()}); r.Status != 304 {
		t.Errorf("%d", r.Status)
	}
	// detail ч мөн
	dAdmin, dReader := get(t, app, "/rooms/"+id, f.orgA, 1), get(t, app, "/rooms/"+id, f.orgA, 2)
	if dAdmin.etag() == dReader.etag() {
		t.Error("detail ETag эрхийн хэв агуулаагүй")
	}
	// read эрхгүй (role 4) → cache-тэй, ETag-тэй ч 403 (cache-ээс ӨМНӨ шалгана)
	for _, p := range []string{"/rooms", "/rooms/" + id, "/rooms/options/owner_id"} {
		r := doH(t, app, "GET", p, f.orgA, 4, nil, map[string]string{"If-None-Match": admin.etag()})
		if r.Status != 403 || r.etag() != "" || r.cache() != "" {
			t.Errorf("%s: read эрхгүй %d etag=%q cache=%q", p, r.Status, r.etag(), r.cache())
		}
	}
	if r := doH(t, app, "POST", "/rooms/query", f.orgA, 4, map[string]any{"page": 1}, nil); r.Status != 403 || strings.Contains(string(r.Raw), "p") && r.str("data") != "" {
		t.Errorf("POST /query 403: %d", r.Status)
	}
	// 401: claims-гүй
	if r := get(t, app, "/rooms", "", 0); r.Status != 401 || r.cache() != "" {
		t.Errorf("401: %d", r.Status)
	}
}

// ─── амжилтгүй бичилт bump хийхгүй ───

func TestCacheFailedWritesDoNotBump(t *testing.T) {
	f := newCfx(t)
	r := f.room(nil)
	r.Hooks.BeforeCreate = func(x *Ctx, v Values) error {
		if v["name"] == "hook-boom" {
			return errors.New("secret internal")
		}
		return nil
	}
	r.Hooks.AfterCreate = func(x *Ctx, v Values) error {
		if v["name"] == "after-boom" {
			return Conflict("after")
		}
		return nil
	}
	r.Hooks.BeforeDelete = func(x *Ctx) error { return Conflict("устгах боломжгүй") }
	app := mountOn(t, "/rooms", r)
	id := mkRoom(t, app, f.orgA, "ok")
	mkRoom(t, app, f.orgA, "dup")
	do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"code": "C1", "version": 1}) // code=C1 (+version 2)

	get(t, app, "/rooms", f.orgA, 1)
	get(t, app, "/rooms/"+id, f.orgA, 1)
	bumps := f.c.Stats().Bumps
	rows0 := f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgA)

	fails := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"422 required", "POST", "/rooms", map[string]any{"code": "X"}, 422},
		{"422 enum", "POST", "/rooms", map[string]any{"name": "n", "status": "nope"}, 422},
		{"422 unique", "POST", "/rooms", map[string]any{"name": "n", "code": "C1"}, 422},
		{"500 hook", "POST", "/rooms", map[string]any{"name": "hook-boom"}, 500},
		{"409 AfterCreate (rollback)", "POST", "/rooms", map[string]any{"name": "after-boom"}, 409},
		{"409 version", "PUT", "/rooms/" + id, map[string]any{"name": "zzz", "version": 99}, 409},
		{"404 update", "PUT", "/rooms/" + newUUID(), map[string]any{"name": "zzz", "version": 1}, 404},
		{"422 update", "PUT", "/rooms/" + id, map[string]any{"name": "", "version": 2}, 422},
		{"409 delete hook", "DELETE", "/rooms/" + id, nil, 409},
		{"404 delete", "DELETE", "/rooms/" + newUUID(), nil, 404},
		{"400 bad id", "DELETE", "/rooms/zzz", nil, 400},
		{"403 update", "PUT", "/rooms/" + id, map[string]any{"name": "x", "version": 2}, 403},
		{"400 bulk", "POST", "/rooms/bulk", map[string]any{"action": "nope", "ids": []string{id}}, 400},
		{"409 bulk delete hook", "POST", "/rooms/bulk", map[string]any{"action": "delete", "ids": []string{id}}, 409},
	}
	for _, tc := range fails {
		role := 1
		if tc.want == 403 {
			role = 2
		}
		got := do(t, app, tc.method, tc.path, f.orgA, role, tc.body)
		if got.Status != tc.want {
			t.Errorf("%s: status=%d, хүлээсэн %d: %s", tc.name, got.Status, tc.want, got.Raw)
		}
	}
	if f.c.Stats().Bumps != bumps {
		t.Errorf("амжилтгүй бичилт bump хийсэн: %d → %d", bumps, f.c.Stats().Bumps)
	}
	if n := f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgA); n != rows0 {
		t.Errorf("rollback хийгдээгүй: %d → %d", rows0, n)
	}
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "hit" {
		t.Errorf("амжилтгүй бичилтийн дараа cache алдагдсан: %q", l.cache())
	}
	if d := get(t, app, "/rooms/"+id, f.orgA, 1); d.cache() != "hit" {
		t.Errorf("detail: %q", d.cache())
	}
}

// ─── 4xx/5xx cache-д орохгүй ───

func TestCacheErrorsNeverCached(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	mkRoom(t, app, f.orgA, "e")
	sets := f.c.Stats().Sets

	missing := newUUID()
	for _, p := range []string{
		"/rooms/" + missing,                           // 404
		"/rooms/not-a-uuid",                           // 400
		`/rooms?filters={"code":{"op":"eq","v":"x"}}`, // 400 (зарлаагүй багана)
		`/rooms?filters={bad`,                         // 400
		"/rooms/options/owner_id?ids=" + manyIDs(201), // 400 (options параметр)
		"/rooms/options/nope",                         // 404
	} {
		a := get(t, app, p, f.orgA, 1)
		q := f.n.Load()
		b := get(t, app, p, f.orgA, 1)
		if a.Status < 400 || b.Status != a.Status || !bytes.Equal(a.Raw, b.Raw) {
			t.Errorf("%s: %d / %d: %s", p, a.Status, b.Status, a.Raw)
		}
		if a.etag() != "" || a.cache() != "" || b.cache() != "" {
			t.Errorf("%s: алдааны хариунд cache толгой: etag=%q cache=%q", p, a.etag(), a.cache())
		}
		if strings.HasPrefix(p, "/rooms/"+missing) && f.n.Load() == q {
			t.Errorf("%s: 404 хоёр дахь удаа DB-д хүрэхгүй байна → cache-д орсон", p)
		}
	}
	if f.c.Stats().Sets != sets {
		t.Errorf("алдаа cache-д орсон: Sets %d → %d", sets, f.c.Stats().Sets)
	}

	// 500: ListQuery алдаатай SQL → generic, cache-д орохгүй, нууц задрахгүй
	calls := 0
	bad := f.room(nil)
	bad.Name = "test.badlist"
	bad.ListQuery = func(db *gorm.DB, orgID string) *gorm.DB {
		calls++
		return db.Table(f.tbl("room") + " t").Select("t.nonexistent_col")
	}
	badApp := mountOn(t, "/bad", bad)
	for i := 0; i < 2; i++ {
		r := get(t, badApp, "/bad", f.orgA, 1)
		if r.Status != 500 || strings.Contains(string(r.Raw), "nonexistent") || r.etag() != "" {
			t.Fatalf("500: %d %s etag=%q", r.Status, r.Raw, r.etag())
		}
	}
	if calls < 2 {
		t.Errorf("500 хариу cache-д орсон: ListQuery %d удаа", calls)
	}
	// эвдэрсэн хүсэлтийн дараа хэвийн хүсэлт ажиллана
	if l := get(t, app, "/rooms", f.orgA, 1); l.Status != 200 {
		t.Errorf("%d", l.Status)
	}
}

func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = newUUID()
	}
	return strings.Join(ids, ",")
}

// ─── options ───

func TestCacheOptions(t *testing.T) {
	f := newCfx(t)
	room := f.room(&CacheSpec{Cache: f.c, DependsOn: []string{"test.person"}})
	app := mountOn(t, "/rooms", room)
	f.person(f.orgA, "Бат", 1)
	f.person(f.orgA, "Болд", 2)
	f.person(f.orgB, "Дорж", 1)

	a := get(t, app, "/rooms/options/owner_id", f.orgA, 2)
	mustStatus(t, a.resp, 200)
	if a.cache() != "miss" || len(a.list("data")) != 2 || a.etag() == "" {
		t.Fatalf("options: %q %s", a.cache(), a.Raw)
	}
	q0 := f.n.Load()
	a2 := get(t, app, "/rooms/options/owner_id", f.orgA, 2)
	if a2.cache() != "hit" || !bytes.Equal(a.Raw, a2.Raw) || f.n.Load() != q0 {
		t.Fatalf("options hit: %q queries %d→%d", a2.cache(), q0, f.n.Load())
	}
	// org тусгаарлалт
	b := get(t, app, "/rooms/options/owner_id", f.orgB, 2)
	if b.cache() != "miss" || len(b.list("data")) != 1 || strings.Contains(string(b.Raw), "Бат") {
		t.Fatalf("B options: %q %s", b.cache(), b.Raw)
	}
	// параметр бүр өөр entry; ижил параметр hit
	s1 := get(t, app, "/rooms/options/owner_id?search=Бат", f.orgA, 2)
	s2 := get(t, app, "/rooms/options/owner_id?search=Бат", f.orgA, 2)
	pr := get(t, app, "/rooms/options/owner_id?parent=2", f.orgA, 2)
	if s1.cache() != "miss" || s2.cache() != "hit" || len(s1.list("data")) != 1 || pr.cache() != "miss" || len(pr.list("data")) != 1 || strings.Contains(string(pr.Raw), "Бат") {
		t.Errorf("params: %q %q %q %s | %s", s1.cache(), s2.cache(), pr.cache(), s1.Raw, pr.Raw)
	}
	// 304
	q1 := f.n.Load()
	if r := doH(t, app, "GET", "/rooms/options/owner_id", f.orgA, 2, nil, map[string]string{"If-None-Match": a.etag()}); r.Status != 304 || f.n.Load() != q1 {
		t.Errorf("options 304: %d queries %d→%d", r.Status, q1, f.n.Load())
	}
	// DependsOn (ref хүснэгт) өөрчлөгдөх: гадаад bump
	f.person(f.orgA, "Шинэ", 1)
	if r := get(t, app, "/rooms/options/owner_id", f.orgA, 2); r.cache() != "hit" || len(r.list("data")) != 2 {
		t.Errorf("bump-гүй өөрчлөлт TTL хүртэл харагдахгүй (гэрээ): %q", r.cache())
	}
	_ = f.c.Bump(context.Background(), f.orgA, "test.person")
	if r := get(t, app, "/rooms/options/owner_id", f.orgA, 2); r.cache() != "miss" || len(r.list("data")) != 3 {
		t.Errorf("DependsOn bump-ийн дараа: %q %s", r.cache(), r.Raw)
	}
	// B-ийн options тэр bump-д автсангүй
	if r := get(t, app, "/rooms/options/owner_id", f.orgB, 2); r.cache() != "hit" {
		t.Errorf("B: %q", r.cache())
	}
	// ids параметр
	if r := get(t, app, "/rooms/options/owner_id?ids="+newUUID(), f.orgA, 2); r.Status != 200 || len(r.list("data")) != 0 {
		t.Errorf("ids: %s", r.Raw)
	}
}

// ─── fail-open ───

type failGens struct {
	cache.GenStore
	failGen, failBump atomic.Bool
}

func (g *failGens) Gen(ctx context.Context, keys []string) ([]uint64, error) {
	if g.failGen.Load() {
		return nil, errors.New("redis down")
	}
	return g.GenStore.Gen(ctx, keys)
}

func (g *failGens) Bump(ctx context.Context, keys []string) error {
	if g.failBump.Load() {
		return errors.New("redis down")
	}
	return g.GenStore.Bump(ctx, keys)
}

func TestCacheFailOpenBypass(t *testing.T) {
	f := newFx(t)
	gens := &failGens{GenStore: cache.NewMemoryGens()}
	c, err := cache.New(cache.Config{Gens: gens})
	if err != nil {
		t.Fatal(err)
	}
	r := f.roomResource()
	r.Cache = &CacheSpec{Cache: c}
	app := mountOn(t, "/rooms", r)
	id := mkRoom(t, app, f.orgA, "k")
	l := get(t, app, "/rooms", f.orgA, 1)
	if l.cache() != "miss" || l.etag() == "" {
		t.Fatal(l.cache())
	}

	gens.failGen.Store(true) // Redis унав: generation уншигдахгүй
	for i := 0; i < 2; i++ {
		b := doH(t, app, "GET", "/rooms", f.orgA, 1, nil, map[string]string{"If-None-Match": l.etag()})
		if b.Status != 200 || b.cache() != "bypass" || b.etag() != "" || !bytes.Equal(b.Raw, l.Raw) {
			t.Fatalf("bypass list: %d %q etag=%q", b.Status, b.cache(), b.etag())
		}
	}
	d := get(t, app, "/rooms/"+id, f.orgA, 1)
	if d.Status != 200 || d.cache() != "bypass" || d.str("name") != "k" {
		t.Errorf("bypass detail: %d %q", d.Status, d.cache())
	}
	if o := get(t, app, "/rooms/options/owner_id", f.orgA, 1); o.Status != 200 || o.cache() != "bypass" {
		t.Errorf("bypass options: %d %q", o.Status, o.cache())
	}
	if nf := get(t, app, "/rooms/"+newUUID(), f.orgA, 1); nf.Status != 404 {
		t.Errorf("bypass 404: %d", nf.Status)
	}
	if bad := get(t, app, "/rooms?filters={bad", f.orgA, 1); bad.Status != 400 {
		t.Errorf("bypass 400: %d", bad.Status)
	}

	// Бичилт: bump амжилтгүй байсан ч бичилт өөрөө амжилттай (commit хийгдсэн), алдаа логлогдоно
	gens.failBump.Store(true)
	up := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "k2", "version": 1})
	if up.Status != 200 {
		t.Fatalf("bump алдаатай ч бичилт амжилттай байх ёстой: %d %s", up.Status, up.Raw)
	}
	// Redis сэргэсэн: bump алдагдсан тул хуучин generation хэвээр — TTL хязгаар (гэрээ). Өгөгдөл ба hit/miss-ийг зөвхөн
	// шалгана: дараагийн бичилт (bump амжилттай) хуучин entry-г хүчингүй болгоно.
	gens.failGen.Store(false)
	gens.failBump.Store(false)
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "k3", "version": 2}), 200)
	if r := get(t, app, "/rooms/"+id, f.orgA, 1); r.str("name") != "k3" {
		t.Errorf("сэргэсний дараа: %s", r.Raw)
	}
}

// ─── TTL ───

func TestCacheTTLExpiry(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(&CacheSpec{Cache: f.c, TTL: time.Minute}))
	mkRoom(t, app, f.orgA, "t")
	get(t, app, "/rooms", f.orgA, 1)
	f.clk.Advance(59 * time.Second)
	if r := get(t, app, "/rooms", f.orgA, 1); r.cache() != "hit" {
		t.Errorf("TTL дотор: %q", r.cache())
	}
	// bump-гүй, DB-д шууд өөрчлөлт → TTL дуустал харагдахгүй, дууссаны дараа харагдана
	f.exec(`UPDATE `+f.tbl("room")+` SET name = 'direct' WHERE org_id = ?`, f.orgA)
	if r := get(t, app, "/rooms", f.orgA, 1); strings.Contains(string(r.Raw), "direct") {
		t.Error("TTL дотор шууд өөрчлөлт харагдлаа (cache ажиллахгүй байна)")
	}
	f.clk.Advance(2 * time.Second)
	if r := get(t, app, "/rooms", f.orgA, 1); r.cache() != "miss" || !strings.Contains(string(r.Raw), "direct") {
		t.Errorf("TTL-ийн дараа: %q %s", r.cache(), r.Raw)
	}
}

// ─── RLS ───

func TestCacheWithRLS(t *testing.T) {
	f := newCfx(t)
	plain := mountOn(t, "/rooms", f.roomResource())
	pA := f.person(f.orgA, "Бат", 1)
	f.person(f.orgB, "Дорж", 1)
	do(t, plain, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a-room", "owner_id": pA, "price": 12.5})
	do(t, plain, "POST", "/rooms", f.orgB, 1, map[string]any{"name": "b-room"})

	rdb := rlsDB(t, f.fx)
	n := countQueries(rdb)
	r := f.room(nil)
	r.RLS, r.Events, r.DB = true, false, func() *gorm.DB { return rdb } // role-д outbox эрх олгоогүй
	app := mountOn(t, "/rooms", r)

	a := get(t, app, "/rooms", f.orgA, 1)
	mustStatus(t, a.resp, 200)
	wantNames(t, a.resp, "a-room")
	if a.cache() != "miss" || !strings.Contains(string(a.Raw), `"price":12.50`) {
		t.Fatalf("RLS list: %q %s", a.cache(), a.Raw)
	}
	q := n.Load()
	if a2 := get(t, app, "/rooms", f.orgA, 1); a2.cache() != "hit" || n.Load() != q || !bytes.Equal(a.Raw, a2.Raw) {
		t.Errorf("RLS hit: %q", a2.cache())
	}
	b := get(t, app, "/rooms", f.orgB, 1)
	wantNames(t, b.resp, "b-room")
	if b.cache() != "miss" {
		t.Errorf("B: %q", b.cache())
	}
	if o := get(t, app, "/rooms/options/owner_id", f.orgA, 1); o.Status != 200 || len(o.list("data")) != 1 {
		t.Errorf("RLS options: %s", o.Raw)
	} else if o2 := get(t, app, "/rooms/options/owner_id", f.orgA, 1); o2.cache() != "hit" {
		t.Errorf("RLS options hit: %q", o2.cache())
	}
	// RLS write → bump
	id := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a2"})
	mustStatus(t, id, 201)
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "miss" || len(l.list("data")) != 2 {
		t.Errorf("RLS create-ийн дараа: %q %s", l.cache(), l.Raw)
	}
	if d := get(t, app, "/rooms/"+id.str("id"), f.orgB, 1); d.Status != 404 {
		t.Errorf("RLS detail B: %d", d.Status)
	}
}

// ─── events ───

func TestCacheBumpOnEventsDispatcher(t *testing.T) {
	f := newCfx(t)
	// Resource Name = "<prefix>.room" → event topic "<prefix>.room.changed" нь BumpOnEvents-ээр яг энэ resource-ийг bump хийнэ.
	prefix := fmt.Sprintf("khancache%d", time.Now().UnixNano()%1000000000)
	topic := prefix + ".room.changed"
	r := f.room(nil)
	r.Name = prefix + ".room"
	app := mountOn(t, "/rooms", r)
	mkRoom(t, app, f.orgA, "ev")
	mkRoom(t, app, f.orgB, "ev-b")
	t.Cleanup(func() { f.db.Exec(`DELETE FROM khan.outbox_events WHERE topic LIKE ?`, prefix+".%") })

	// OnlyRegistered: зөвхөн энэ тестийн topic-уудыг claim хийнэ (бусад тестийн outbox-д хүрэхгүй).
	d := &events.Dispatcher{DB: f.db, OnlyRegistered: true}
	if err := d.Handle(prefix+".*", cache.BumpOnEvents(f.c)); err != nil {
		t.Fatal(err)
	}
	drain := func() int {
		t.Helper()
		total := 0
		for {
			n, err := d.ProcessOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				return total
			}
			total += n
		}
	}
	// Resource-ийн өөрийн created event-үүд (2) мөн энэ prefix-д таарна.
	if n := drain(); n != 2 {
		t.Fatalf("created event: %d", n)
	}
	get(t, app, "/rooms", f.orgA, 1)
	get(t, app, "/rooms", f.orgB, 1)

	// Crud-ийн гадуурх бичилт (service код): мөр өөрчилж, event-ийг нэг транзакцад нийтэлнэ.
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE `+f.tbl("room")+` SET name = 'svc' WHERE org_id = ?`, f.orgA).Error; err != nil {
			return err
		}
		return events.Publish(tx, events.Event{OrgID: f.orgA, Topic: topic, AggregateType: "room", AggregateID: "1", Payload: map[string]any{"x": 1}})
	})
	if err != nil {
		t.Fatal(err)
	}
	// dispatcher ажиллаагүй хугацаанд cache хэвээр (bump-гүй өөрчлөлт TTL хүртэл харагдахгүй гэсэн гэрээ)
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "hit" || strings.Contains(string(l.Raw), "svc") {
		t.Fatalf("dispatcher-ийн өмнө: %q", l.cache())
	}
	if n := drain(); n != 1 {
		t.Fatalf("changed event: %d", n)
	}
	if l := get(t, app, "/rooms", f.orgA, 1); l.cache() != "miss" || !strings.Contains(string(l.Raw), "svc") {
		t.Errorf("event-ийн дараа: %q %s", l.cache(), l.Raw)
	}
	// Өөр org-ийн cache event-ээр хүчингүй болоогүй
	if l := get(t, app, "/rooms", f.orgB, 1); l.cache() != "hit" {
		t.Errorf("B: %q", l.cache())
	}
}

// ─── зэрэгцээ ───

func TestCacheConcurrentReadsAndWrites(t *testing.T) {
	f := newCfx(t)
	app := mountOn(t, "/rooms", f.room(nil))
	for i := 0; i < 5; i++ {
		mkRoom(t, app, f.orgA, fmt.Sprintf("c%d", i))
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				req := httptest.NewRequest("GET", "/rooms", nil)
				req.Header.Set("X-T-Org", f.orgA)
				req.Header.Set("X-T-Role", "1")
				res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
				if err != nil {
					errs <- err.Error()
					return
				}
				raw, _ := io.ReadAll(res.Body)
				var out struct {
					Data []map[string]any `json:"data"`
				}
				if res.StatusCode != 200 || json.Unmarshal(raw, &out) != nil || len(out.Data) < 5 {
					errs <- fmt.Sprintf("status=%d %s", res.StatusCode, raw)
					return
				}
			}
		}()
	}
	for i := 0; i < 3; i++ {
		mkRoom(t, app, f.orgA, fmt.Sprintf("w%d", i))
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// эцэст нь cache шинэ бичилтүүдийг агуулна
	if l := get(t, app, "/rooms", f.orgA, 1); len(l.list("data")) != 8 {
		t.Errorf("эцсийн жагсаалт: %d мөр", len(l.list("data")))
	}
}

// BumpOnly: dependency resource нь өөрөө cache хийхгүй ч бичилтээрээ DependsOn-той
// resource-ийн cache-ийг хүчингүй болгоно; түүний уншилт cache-гүй (header алга).
func TestCacheBumpOnlyDependency(t *testing.T) {
	f := newCfx(t)
	tag := f.tagResource()
	tag.Cache = &CacheSpec{Cache: f.c, BumpOnly: true}
	room := f.room(&CacheSpec{Cache: f.c, DependsOn: []string{"test.tag"}})
	tagApp := mountOn(t, "/tags", tag)
	roomApp := mountOn(t, "/rooms", room)
	mkRoom(t, roomApp, f.orgA, "r")

	get(t, roomApp, "/rooms", f.orgA, 1)
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "hit" {
		t.Fatal("халаагдсангүй")
	}
	mustStatus(t, do(t, tagApp, "POST", "/tags", f.orgA, 1, map[string]any{"label": "t"}), 201)
	if l := get(t, roomApp, "/rooms", f.orgA, 1); l.cache() != "miss" {
		t.Errorf("BumpOnly dependency-ийн бичилт cache-ийг хүчингүй болгосонгүй: %q", l.cache())
	}
	if l := get(t, tagApp, "/tags", f.orgA, 1); l.cache() != "" || l.etag() != "" {
		t.Errorf("BumpOnly resource уншилтыг cache хийв: %q %q", l.cache(), l.etag())
	}
}

// ambiguousWrite: зөвхөн COMMIT-ийн алдаа (*commitError) тодорхойгүй; fn доторх алдаа биш.
func TestAmbiguousWriteOnlyCommitErrors(t *testing.T) {
	base := errors.New("conn reset")
	if !ambiguousWrite(&commitError{base}) || !ambiguousWrite(fmt.Errorf("wrap: %w", &commitError{base})) {
		t.Error("commitError тодорхойгүй гэж тооцогдох ёстой")
	}
	for _, err := range []error{nil, base, context.DeadlineExceeded, Conflict("x"), validate.NewErrors().Err()} {
		if ambiguousWrite(err) {
			t.Errorf("%v: rollback баталгаатай алдааг тодорхойгүй гэв", err)
		}
	}
}
