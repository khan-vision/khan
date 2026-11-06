package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options — Run-ийн тохиргоо.
type Options struct {
	A, B           string // base URL-ууд
	Corpus         []Request
	Profiles       Profiles
	Ignore         IgnoreConfig
	Expected       *Expected
	AllowMutations bool
	AllowRemote    bool
	Timeout        time.Duration // нэг хүсэлтийн (default 30s)
	MaxBody        int64         // уншигдах биеийн дээд хэмжээ (default 8MiB)
	HTTPClient     *http.Client  // тестэд; nil бол redirect дагахгүй клиент
}

// Result — нэг хүсэлтийн үр дүн.
type Result struct {
	Label   string `json:"request"`
	Module  string `json:"module,omitempty"`
	Auth    string `json:"auth,omitempty"`
	Status  string `json:"status"` // identical | differing | expected | error | skipped
	StatusA int    `json:"status_a,omitempty"`
	StatusB int    `json:"status_b,omitempty"`
	Note    string `json:"note,omitempty"`
	Diffs   []Diff `json:"diffs,omitempty"`
}

// Summary — нэгтгэл.
type Summary struct {
	Total, Identical, Differing, Expected, Errors, Skipped int
}

// Report — бүрэн тайлан.
type Report struct {
	A, B          string
	Summary       Summary
	Results       []Result
	UnusedExpects []string `json:"unused_expected,omitempty"`
}

// Failed — гэнэтийн ялгаа эсвэл алдаа байвал true (CLI exit != 0).
func (r *Report) Failed() bool { return r.Summary.Differing > 0 || r.Summary.Errors > 0 }

// Run — корпусыг A, B дээр тоглуулж тайлан гаргана.
func Run(ctx context.Context, o Options) (*Report, error) {
	if _, err := CheckTargets(o.A, o.B, o.AllowRemote, o.AllowMutations); err != nil {
		return nil, err
	}
	// Корпус дахь auth нэрсийг эхэлж шалгана.
	for _, q := range o.Corpus {
		if q.Auth != "" {
			if _, ok := o.Profiles[q.Auth]; !ok {
				return nil, fmt.Errorf("%s: auth profile %q profiles.json-д алга", q.Label(), q.Auth)
			}
		}
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxBody == 0 {
		o.MaxBody = 8 << 20
	}
	if o.Expected == nil {
		o.Expected = &Expected{}
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       o.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	red := &Redactor{}
	norm := NewNormalizer(o.Ignore)
	authA := newAuthState(o.A, client, o.Profiles, red)
	authB := newAuthState(o.B, client, o.Profiles, red)
	rep := &Report{A: o.A, B: o.B}

	for _, q := range o.Corpus {
		res := Result{Module: q.Module, Auth: q.Auth}
		res = withQueryRedacted(res, q, red)
		rep.Summary.Total++
		switch {
		case HasPlaceholder(q.Path):
			res.Status, res.Note = "skipped", "бөглөгдөөгүй placeholder (params файлаар бөглөнө)"
		case !safeMethod(q.M()) && !o.AllowMutations:
			res.Status, res.Note = "skipped", q.M()+": -allow-mutations байхгүй"
		default:
			ra := doRequest(ctx, client, o.A, q, authA, o.MaxBody, red)
			rb := doRequest(ctx, client, o.B, q, authB, o.MaxBody, red)
			res.StatusA, res.StatusB = ra.Status, rb.Status
			diffs := norm.Compare(ra, rb)
			for i := range diffs {
				diffs[i].A, diffs[i].B = red.String(diffs[i].A), red.String(diffs[i].B)
			}
			switch {
			case len(diffs) == 0:
				res.Status = "identical"
			case o.Expected.Mark(q, diffs):
				res.Status, res.Diffs = "expected", diffs
			default:
				res.Status, res.Diffs = "differing", diffs
				if ra.Err != "" || rb.Err != "" {
					res.Status = "error"
				}
			}
		}
		switch res.Status {
		case "identical":
			rep.Summary.Identical++
		case "differing":
			rep.Summary.Differing++
		case "expected":
			rep.Summary.Expected++
		case "error":
			rep.Summary.Errors++
		case "skipped":
			rep.Summary.Skipped++
		}
		rep.Results = append(rep.Results, res)
	}
	rep.UnusedExpects = o.Expected.Unused()
	return rep, nil
}

func withQueryRedacted(r Result, q Request, red *Redactor) Result {
	p := q.M() + " " + q.Path
	if q.Query != "" {
		p += "?" + red.Query(q.Query)
	}
	r.Label = red.String(p)
	return r
}

func doRequest(ctx context.Context, c *http.Client, base string, q Request, auth *authState, max int64, red *Redactor) Response {
	base = strings.TrimRight(base, "/")
	u := base + q.Path
	if q.Query != "" {
		u += "?" + q.Query
	}
	// Тоглуулах цэгийн сүүлчийн хамгаалалт: remote бол зөвхөн GET/HEAD.
	if lo, _ := IsLoopbackURL(base); !lo && !safeMethod(q.M()) {
		return Response{Err: "remote хаягт " + q.M() + " хориотой"}
	}
	hdr, err := auth.get(ctx, q.Auth)
	if err != nil {
		return Response{Err: red.String(err.Error())}
	}
	var body io.Reader
	if len(q.Body) > 0 {
		var s string
		if json.Unmarshal(q.Body, &s) == nil {
			body = strings.NewReader(s)
		} else {
			body = bytes.NewReader(q.Body)
		}
	}
	req, err := http.NewRequestWithContext(ctx, q.M(), u, body)
	if err != nil {
		return Response{Err: red.String(err.Error())}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		if ue, ok := err.(*url.Error); ok {
			err = ue.Err
		}
		return Response{Err: red.String(err.Error())}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return Response{Err: red.String(err.Error())}
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// WriteReports — dir/report.json ба dir/report.txt бичнэ.
func WriteReports(dir string, r *Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.txt"), []byte(r.Text()), 0o644)
}

// Text — уншигдахуйц тайлан.
func (r *Report) Text() string {
	var sb strings.Builder
	s := r.Summary
	fmt.Fprintf(&sb, "A = %s\nB = %s\n", r.A, r.B)
	fmt.Fprintf(&sb, "total=%d identical=%d differing=%d expected=%d errors=%d skipped=%d\n\n",
		s.Total, s.Identical, s.Differing, s.Expected, s.Errors, s.Skipped)
	for _, x := range r.Results {
		if x.Status == "identical" {
			continue
		}
		fmt.Fprintf(&sb, "[%s] %s", strings.ToUpper(x.Status), x.Label)
		if x.Auth != "" {
			fmt.Fprintf(&sb, " (auth=%s)", x.Auth)
		}
		sb.WriteByte('\n')
		if x.Note != "" {
			fmt.Fprintf(&sb, "    %s\n", x.Note)
		}
		for _, d := range x.Diffs {
			fmt.Fprintf(&sb, "    %-12s %s\n        A: %s\n        B: %s\n", d.Kind, d.Path, d.A, d.B)
			if d.Expected != "" {
				fmt.Fprintf(&sb, "        (expected: %s)\n", d.Expected)
			}
		}
	}
	for _, u := range r.UnusedExpects {
		fmt.Fprintf(&sb, "\nWARN: ашиглагдаагүй expected мөр: %s\n", u)
	}
	return sb.String()
}
