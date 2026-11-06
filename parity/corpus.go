// Package parity — хоёр backend (A = одоогийн, B = нэр дэвшигч)-ийг ижил хүсэлтээр
// дахин тоглуулж, хариуг normalise хийж харьцуулдаг "behaviour-parity" хэрэгсэл.
// Зорилго: refactor хийсний дараа ажиллаж буй системийг эвдээгүйг баталгаажуулах.
//
// Аюулгүй байдал: loopback биш хаягт зөвхөн -allow-remote-тэй, тэгэхдээ зөвхөн
// GET/HEAD илгээнэ (guard.go).
package parity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Request — корпусын нэг мөр (JSONL).
type Request struct {
	Name    string            `json:"name,omitempty"`
	Method  string            `json:"method,omitempty"` // хоосон = GET
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"` // "a=1&b=2" (тэргүүн ? байхгүй)
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"` // JSON утга; string бол түүхийгээр нь илгээнэ
	Auth    string            `json:"auth,omitempty"` // profiles.json доторх нэр; хоосон = нэвтрэлтгүй
	Module  string            `json:"module,omitempty"`
	Source  string            `json:"source,omitempty"` // extractor-ийн олсон файл:мөр
}

// M — том үсгээр хэвийн болгосон method.
func (r Request) M() string {
	if r.Method == "" {
		return "GET"
	}
	return strings.ToUpper(r.Method)
}

// Key — давхардал арилгах түлхүүр.
func (r Request) Key() string {
	return r.M() + " " + r.Path + "?" + r.Query + " @" + r.Auth + " #" + string(r.Body)
}

// Label — тайланд харуулах товч нэр.
func (r Request) Label() string {
	s := r.M() + " " + r.Path
	if r.Query != "" {
		s += "?" + r.Query
	}
	return s
}

// LoadCorpus — JSONL файлаас корпус уншина (хоосон мөр, "#" эсвэл "//"-ээр эхэлсэн мөрийг алгасна).
func LoadCorpus(path string) ([]Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadCorpus(f)
}

// ReadCorpus — io.Reader-ээс JSONL уншина.
func ReadCorpus(r io.Reader) ([]Request, error) {
	var out []Request
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		var q Request
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("корпус мөр %d: %w", n, err)
		}
		if !strings.HasPrefix(q.Path, "/") {
			return nil, fmt.Errorf("корпус мөр %d: path нь '/'-ээр эхлэх ёстой (%q)", n, q.Path)
		}
		out = append(out, q)
	}
	return out, sc.Err()
}

// WriteCorpus — JSONL болгон бичнэ.
func WriteCorpus(w io.Writer, reqs []Request) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, q := range reqs {
		if err := enc.Encode(q); err != nil {
			return err
		}
	}
	return nil
}

// MergeCorpora — корпусуудыг нэгтгэж давхардлыг арилгана; түрүүлж орсон нь (гараар бичсэн) давуу.
func MergeCorpora(lists ...[]Request) []Request {
	seen := map[string]bool{}
	var out []Request
	for _, l := range lists {
		for _, q := range l {
			if seen[q.Key()] {
				continue
			}
			seen[q.Key()] = true
			out = append(out, q)
		}
	}
	return out
}
