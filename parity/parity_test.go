package parity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer — A/B серверийн хуурамч; variant="b" үед санаатай ялгаатай.
func fakeServer(t *testing.T, variant string) *httptest.Server {
	t.Helper()
	b := variant == "b"
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["login"] != "u" || in["password"] != "s3cret-pass" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"eyJhbGciOi.payloadpayload.sigsigsig-` + variant + `"}`))
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(401)
			return false
		}
		return true
	}
	mux.HandleFunc("/api/same", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ts, id, amt := "2026-01-01T00:00:00Z", "id-a", "12.5"
		if b {
			ts, id, amt = "2026-02-02T00:00:00Z", "id-b", "12.50"
		}
		_, _ = w.Write([]byte(`{"created_at":"` + ts + `","data":{"id":"` + id + `","amount":` + amt + `,"b":1,"a":2}}`))
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if b {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	})
	mux.HandleFunc("/api/value", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		v := "100"
		if b {
			v = "101"
		}
		_, _ = w.Write([]byte(`{"total":` + v + `,"items":[1,2]}`))
	})
	mux.HandleFunc("/api/hdr", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if b {
			w.Header().Set("Cache-Control", "no-store")
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "zzz", Path: "/", HttpOnly: true})
		} else {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "aaa", Path: "/"})
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/ctype", func(w http.ResponseWriter, r *http.Request) {
		if b {
			w.Header().Set("Content-Type", "text/html")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if b {
			_, _ = w.Write([]byte("bbbb"))
		} else {
			_, _ = w.Write([]byte("aaaa"))
		}
	})
	mux.HandleFunc("/api/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if b {
			_, _ = w.Write([]byte(`{"rows":[{"n":2},{"n":1}]}`))
		} else {
			_, _ = w.Write([]byte(`{"rows":[{"n":1},{"n":2}]}`))
		}
	})
	mux.HandleFunc("/api/write", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"method":"` + r.Method + `"}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunDetectsExactlyTheDifferences(t *testing.T) {
	a, b := fakeServer(t, "a"), fakeServer(t, "b")
	dir := t.TempDir()
	creds := writeFile(t, dir, "cred.json", `{"login":"u","password":"s3cret-pass"}`)
	corpus := []Request{
		{Path: "/api/same", Auth: "adm"},
		{Path: "/api/status"},
		{Path: "/api/value"},
		{Path: "/api/hdr"},
		{Path: "/api/ctype"},
		{Path: "/api/file"},
		{Path: "/api/list"},
		{Method: "POST", Path: "/api/write", Body: json.RawMessage(`{"x":1}`)},
		{Path: "/api/x/{id}"},
	}
	rep, err := Run(context.Background(), Options{
		A: a.URL, B: b.URL, Corpus: corpus,
		Profiles: Profiles{"adm": {LoginFile: creds}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rep.Results {
		got[r.Label] = r.Status
	}
	want := map[string]string{
		"GET /api/same":   "identical", // created_at/id ялгаа, 12.5==12.50 -> id л ялгаатай ТУЛ доор шалгана
		"GET /api/status": "differing",
		"GET /api/value":  "differing",
		"GET /api/hdr":    "differing",
		"GET /api/ctype":  "differing",
		"GET /api/file":   "differing",
		"GET /api/list":   "identical", // ordered: ялгаатай байх ёстой
		"POST /api/write": "skipped",
		"GET /api/x/{id}": "skipped",
	}
	// /api/same дотор data.id ялгаатай (default ignore-д ороогүй) -> differing; list эрэмбэтэй -> differing.
	want["GET /api/same"], want["GET /api/list"] = "differing", "differing"
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, хүлээсэн %q", k, got[k], v)
		}
	}
	if rep.Summary.Skipped != 2 || rep.Summary.Identical != 0 || !rep.Failed() {
		t.Errorf("summary: %+v", rep.Summary)
	}
	text := rep.Text()
	for _, leak := range []string{"s3cret-pass", "eyJhbGciOi", "sigsigsig"} {
		if strings.Contains(text, leak) {
			t.Errorf("тайланд нууц гоожлоо: %s", leak)
		}
	}
	// Тусгай ялгаа: statuses, cookie shape, header.
	for _, r := range rep.Results {
		switch r.Label {
		case "GET /api/status":
			if r.StatusA != 200 || r.StatusB != 404 || r.Diffs[0].Kind != KindStatus {
				t.Errorf("status: %+v", r)
			}
		case "GET /api/hdr":
			kinds := ""
			for _, d := range r.Diffs {
				kinds += d.Kind + ":" + d.Path + " "
			}
			if !strings.Contains(kinds, "header:cache-control") || !strings.Contains(kinds, "header:set-cookie") {
				t.Errorf("hdr diffs: %s", kinds)
			}
			for _, d := range r.Diffs {
				if strings.Contains(d.A+d.B, "zzz") || strings.Contains(d.A+d.B, "aaa") {
					t.Errorf("cookie утга гоожлоо: %+v", d)
				}
			}
		case "GET /api/same":
			if len(r.Diffs) != 1 || r.Diffs[0].Path != "$.data.id" {
				t.Errorf("same: %+v", r.Diffs)
			}
		}
	}
}

func TestIgnoreUnorderedAndExpected(t *testing.T) {
	a, b := fakeServer(t, "a"), fakeServer(t, "b")
	dir := t.TempDir()
	creds := writeFile(t, dir, "c.json", `{"login":"u","password":"s3cret-pass"}`)
	exp := writeFile(t, dir, "exp.json", `{"expected":[
	  {"path":"/api/status","reason":"404 санаатай"},
	  {"path":"/api/value","kind":"body","body_path":"$.total","reason":"тооцоо өөрчлөгдсөн"}]}`)
	e, err := LoadExpected(exp)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), Options{
		A: a.URL, B: b.URL,
		Corpus: []Request{
			{Path: "/api/same", Auth: "adm"}, {Path: "/api/status"}, {Path: "/api/value"}, {Path: "/api/list"},
		},
		Profiles: Profiles{"adm": {LoginFile: creds}},
		Ignore:   IgnoreConfig{IgnorePaths: []string{"$..id"}, UnorderedPaths: []string{"$.rows"}},
		Expected: e,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Identical != 2 || rep.Summary.Expected != 2 || rep.Summary.Differing != 0 || rep.Failed() {
		t.Fatalf("summary: %+v\n%s", rep.Summary, rep.Text())
	}
	if len(rep.UnusedExpects) != 0 {
		t.Errorf("unused: %v", rep.UnusedExpects)
	}
}

func TestExpectedRequiresReason(t *testing.T) {
	p := writeFile(t, t.TempDir(), "e.json", `[{"path":"/api/x"}]`)
	if _, err := LoadExpected(p); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("reason-гүй мөр татгалзагдах ёстой: %v", err)
	}
}

func TestMutationsGuardedAndReplayed(t *testing.T) {
	a, b := fakeServer(t, "a"), fakeServer(t, "b")
	q := Request{Method: "POST", Path: "/api/write", Body: json.RawMessage(`{"x":1}`)}
	rep, err := Run(context.Background(), Options{A: a.URL, B: b.URL, Corpus: []Request{q}, AllowMutations: true})
	if err != nil || rep.Summary.Identical != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestLoopbackGuard(t *testing.T) {
	cases := []struct {
		a, b        string
		remote, mut bool
		wantErr     bool
	}{
		{"http://127.0.0.1:1", "http://localhost:2", false, false, false},
		{"http://[::1]:1", "http://127.0.0.2:2", false, true, false},
		{"https://api.example.com", "http://127.0.0.1:2", false, false, true},
		{"http://127.0.0.1:1", "http://10.0.0.5:2", false, false, true},
		{"https://api.example.com", "http://127.0.0.1:2", true, false, false},
		{"https://api.example.com", "http://127.0.0.1:2", true, true, true}, // remote + mutations хориотой
		{"ftp://127.0.0.1", "http://127.0.0.1", false, false, true},
		{"http://127.0.0.1.evil.com", "http://127.0.0.1", false, false, true},
	}
	for _, c := range cases {
		_, err := CheckTargets(c.a, c.b, c.remote, c.mut)
		if (err != nil) != c.wantErr {
			t.Errorf("%v: err=%v", c, err)
		}
	}
	// Run нь remote-д хүсэлт илгээхээс ӨМНӨ татгалзана (сүлжээнд хүрэхгүй).
	if _, err := Run(context.Background(), Options{A: "https://prod.example.com", B: "http://127.0.0.1:1"}); err == nil {
		t.Error("Run remote-г татгалзах ёстой")
	}
	// doRequest-ийн сүүлчийн хамгаалалт: remote-д POST илгээхгүй.
	r := doRequest(context.Background(), http.DefaultClient, "https://prod.example.com", Request{Method: "POST", Path: "/x"}, newAuthState("x", nil, nil, &Redactor{}), 10, &Redactor{})
	if !strings.Contains(r.Err, "хориотой") {
		t.Errorf("remote POST хориглоогүй: %+v", r)
	}
}

func TestRedaction(t *testing.T) {
	r := &Redactor{}
	r.Add("topsecretvalue")
	s := r.String("x topsecretvalue Bearer abc.def.ghi eyJhbGciOiJI.eyJzdWIiOiIx.sig123")
	for _, bad := range []string{"topsecretvalue", "abc.def.ghi", "eyJhbGciOiJI"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q үлдсэн: %s", bad, s)
		}
	}
	if q := r.Query("a=1&token=zzzzzzzz"); strings.Contains(q, "zzzzzzzz") || !strings.Contains(q, "a=1") {
		t.Errorf("query: %s", q)
	}
	n := NewNormalizer(IgnoreConfig{NoDefaults: true})
	v, _ := ParseJSON([]byte(`{"password":"p","x":{"Authorization":"Bearer q"}}`))
	if c := canonString(n.Normalize(v)); strings.Contains(c, `"p"`) || strings.Contains(c, "Bearer q") {
		t.Errorf("body: %s", c)
	}
}

func TestNormalize(t *testing.T) {
	n := NewNormalizer(IgnoreConfig{IgnorePaths: []string{"$.meta.*", "$..items[*].id"}, UnorderedPaths: []string{"$..tags"}})
	cmp := func(x, y string) bool {
		a, _ := ParseJSON([]byte(x))
		b, _ := ParseJSON([]byte(y))
		return len(DiffJSON(n.Normalize(a), n.Normalize(b))) == 0
	}
	ok := []struct{ x, y string }{
		{`{"a":12.5}`, `{"a":12.50}`},
		{`{"a":3}`, `{"a":3.0}`},
		{`{"a":1e2}`, `{"a":100}`},
		{`{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{`{"created_at":"x","v":1}`, `{"created_at":"y","v":1}`},
		{`{"d":[{"updated_at":1}]}`, `{"d":[{"updated_at":2}]}`},
		{`{"meta":{"t":1}}`, `{"meta":{"t":2,"u":3}}`},
		{`{"items":[{"id":1,"n":"a"}]}`, `{"items":[{"id":9,"n":"a"}]}`},
		{`{"o":{"tags":["a","b"]}}`, `{"o":{"tags":["b","a"]}}`},
		{`{"ETag":"1"}`, `{"etag":"2"}`},
	}
	for _, c := range ok {
		if !cmp(c.x, c.y) {
			t.Errorf("тэнцүү байх ёстой: %s vs %s", c.x, c.y)
		}
	}
	bad := []struct{ x, y string }{
		{`{"a":12.5}`, `{"a":12.51}`},
		{`{"a":[1,2]}`, `{"a":[2,1]}`},
		{`{"a":"1"}`, `{"a":1}`},
		{`{"a":null}`, `{"a":0}`},
		{`{"a":1}`, `{"a":1,"b":2}`},
		{`{"o":{"tags":["a"]}}`, `{"o":{"tags":["a","b"]}}`},
		{`{"id":1}`, `{"id":2}`}, // id үндсэн ignore-д ороогүй
	}
	for _, c := range bad {
		if cmp(c.x, c.y) {
			t.Errorf("ялгаатай байх ёстой: %s vs %s", c.x, c.y)
		}
	}
}

func TestContentFamilyAndCookie(t *testing.T) {
	if ContentFamily("application/vnd.api+json; charset=x") != "json" || ContentFamily("") != "none" || ContentFamily("text/plain") != "text" {
		t.Error("family")
	}
	if CookieShape("sid=SECRET; Path=/; HttpOnly; Expires=Wed, 21 Oct 2026 07:28:00 GMT") != "sid; expires; httponly; path=/" {
		t.Errorf("cookie: %s", CookieShape("sid=SECRET; Path=/; HttpOnly; Expires=Wed, 21 Oct 2026 07:28:00 GMT"))
	}
}

func TestLoginFailureDoesNotLeak(t *testing.T) {
	a := fakeServer(t, "a")
	dir := t.TempDir()
	creds := writeFile(t, dir, "c.json", `{"login":"u","password":"wrongpassword"}`)
	rep, err := Run(context.Background(), Options{A: a.URL, B: a.URL, Corpus: []Request{{Path: "/api/same", Auth: "adm"}},
		Profiles: Profiles{"adm": {LoginFile: creds}}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Identical != 1 { // хоёул ижил алдаатай
		t.Logf("%s", rep.Text())
	}
	if strings.Contains(rep.Text(), "wrongpassword") {
		t.Error("нууц үг гоожлоо")
	}
}

func TestExtractSynthetic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "modules/finance/a.vue", "<script>\n"+
		"const r = await axios.get('/api/finance/accounts', { params })\n"+
		"await api.get(`/api/finance/invoices/${invoice.id}/lines?with=1&q=${term}`)\n"+
		"await $fetch('/api/hr/employees')\n"+
		"await $fetch('/api/hr/employees', { method: 'POST' })\n"+
		"await axios.get('/lambda/krud/today')\n"+
		"await axios.get('/api/public/crm/grades')\n"+
		"await axios.get('/api/sale/items/' + id)\n"+
		"await map.get('/not/api')\n"+
		"await axios.get(`/api/x/${config.endpoint}`)\n"+
		"</script>")
	writeFile(t, root, "node_modules/x.js", "axios.get('/api/ignored')")
	res, un, st, err := ExtractFrontend(root, ExtractOptions{Params: map[string]string{"invoice.id": "INV1"}, Auth: "adm"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range res {
		keys = append(keys, r.Path+"?"+r.Query+"|"+r.Module)
	}
	joined := strings.Join(keys, "\n")
	for _, w := range []string{
		"/api/finance/accounts?|finance",
		"/api/finance/invoices/INV1/lines?with=1|finance", // placeholder-тэй query хос хаягдсан
		"/api/hr/employees?|hr",
		"/lambda/krud/today?|lambda",
		"/api/public/crm/grades?|crm",
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("олдсонгүй %q в:\n%s", w, joined)
		}
	}
	if len(res) != 5 || len(un) != 2 || st.Unresolved != 2 || st.FilesScanned != 1 || res[0].Auth != "adm" {
		t.Errorf("res=%d un=%d st=%+v", len(res), len(un), st)
	}
	for _, u := range un {
		if !HasPlaceholder(u.Path) {
			t.Errorf("unresolved placeholder-гүй: %s", u.Path)
		}
	}
	// params-аар бөглөх
	res2, un2, _, _ := ExtractFrontend(root, ExtractOptions{Params: map[string]string{"invoice.id": "I", "endpoint": "e", "dynamic": "5"}})
	if len(un2) != 0 || len(res2) != 7 {
		t.Errorf("params: res=%d un=%d", len(res2), len(un2))
	}
}

func TestCorpusIOAndMerge(t *testing.T) {
	c, err := ReadCorpus(strings.NewReader("# тайлбар\n\n{\"path\":\"/a\"}\n{\"method\":\"post\",\"path\":\"/b\",\"body\":{\"x\":1}}\n"))
	if err != nil || len(c) != 2 || c[1].M() != "POST" {
		t.Fatalf("%v %+v", err, c)
	}
	m := MergeCorpora(c, []Request{{Path: "/a"}, {Path: "/c"}})
	if len(m) != 3 {
		t.Errorf("merge: %d", len(m))
	}
	if _, err := ReadCorpus(strings.NewReader(`{"path":"a"}`)); err == nil {
		t.Error("path шалгалт")
	}
}
