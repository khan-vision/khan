package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParityCLI(t *testing.T) {
	mk := func(v string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"v":"` + v + `"}`))
		}))
		t.Cleanup(s.Close)
		return s
	}
	same, other := mk("1"), mk("2")
	dir := t.TempDir()
	corpus := filepath.Join(dir, "c.jsonl")
	if err := os.WriteFile(corpus, []byte(`{"path":"/api/x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if err := run([]string{"parity", "run", "-a", same.URL, "-b", same.URL, "-corpus", corpus, "-o", filepath.Join(dir, "r")}, &out, &errb); err != nil {
		t.Fatalf("ижил: %v", err)
	}
	if err := run([]string{"parity", "run", "-a", same.URL, "-b", other.URL, "-corpus", corpus, "-o", filepath.Join(dir, "r2")}, &out, &errb); err == nil {
		t.Fatal("ялгаатай үед алдаа (exit != 0) хүлээсэн")
	}
	if _, err := os.Stat(filepath.Join(dir, "r2", "report.json")); err != nil {
		t.Error("report.json алга")
	}
	if err := run([]string{"parity", "run", "-a", "https://prod.example.com", "-b", same.URL, "-corpus", corpus}, &out, &errb); err == nil || !strings.Contains(err.Error(), "allow-remote") {
		t.Errorf("remote татгалзах ёстой: %v", err)
	}
	// corpus дэд команд
	fe := filepath.Join(dir, "fe")
	_ = os.MkdirAll(fe, 0o755)
	_ = os.WriteFile(filepath.Join(fe, "a.ts"), []byte("axios.get('/api/hr/x'); axios.get(`/api/hr/y/${id}`)"), 0o600)
	out.Reset()
	if err := run([]string{"parity", "corpus", "-frontend", fe, "-o", filepath.Join(dir, "o.jsonl")}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "resolved=1 unresolved=1") {
		t.Errorf("stats: %s", out.String())
	}
}
