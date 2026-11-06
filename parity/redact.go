package parity

import (
	"net/url"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var (
	jwtRe    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)
	bearerRe = regexp.MustCompile(`(?i)(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	// sensitiveKeys — JSON түлхүүр/header/query нэр (жижиг үсгээр) — утгыг нь хэзээ ч тайланд оруулахгүй.
	sensitiveKeys = map[string]bool{
		"token": true, "access_token": true, "refresh_token": true, "accesstoken": true, "refreshtoken": true,
		"id_token": true, "password": true, "passwd": true, "secret": true, "client_secret": true,
		"authorization": true, "cookie": true, "set-cookie": true, "api_key": true, "apikey": true,
		"x-api-key": true, "session": true, "jwt": true,
	}
)

// Redactor — тайлан руу орох бүх текстээс нууц утгыг арилгана.
type Redactor struct{ secrets []string }

// Add — нууц утга (token, password) бүртгэнэ; богино (<6) утгыг алгасна (худал таарахаас сэргийлнэ).
func (r *Redactor) Add(s string) {
	if len(s) >= 6 {
		r.secrets = append(r.secrets, s)
	}
}

// String — бүртгэлтэй нууц, JWT, Bearer-ийг устгана.
func (r *Redactor) String(s string) string {
	if r != nil {
		for _, x := range r.secrets {
			s = strings.ReplaceAll(s, x, redacted)
		}
	}
	s = jwtRe.ReplaceAllString(s, redacted)
	return bearerRe.ReplaceAllString(s, "$1 "+redacted)
}

// Query — query string дахь мэдрэмтгий параметрийн утгыг нууцлана.
func (r *Redactor) Query(q string) string {
	vals, err := url.ParseQuery(q)
	if err != nil {
		return r.String(q)
	}
	changed := false
	for k := range vals {
		if sensitiveKeys[strings.ToLower(k)] {
			vals[k] = []string{redacted}
			changed = true
		}
	}
	if !changed {
		return r.String(q)
	}
	return r.String(vals.Encode())
}
