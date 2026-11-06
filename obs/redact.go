package obs

import (
	"log/slog"
	"regexp"
	"strings"
)

// Redacted — далдалсан утгын оронд бичигдэх тэмдэг.
const Redacted = "***"

// secretParam — URL query / form дахь нууц параметрүүд (main/app/logredact-ийн
// дүрэм + нийтлэг password/token/secret).
var secretParam = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|token|password|passwd|secret|appsecret_proof|client_secret|fb_exchange_token|input_token|api_key|apikey)=([^&\s"'\\]+)`)

// bearer — «Authorization: Bearer xxx» / «Bearer xxx».
var bearer = regexp.MustCompile(`(?i)\b(Bearer)\s+[A-Za-z0-9\-._~+/]{16,}=*`)

// fbToken — query-гүй ил FB token (EAA… урт мөр).
var fbToken = regexp.MustCompile(`\bEAA[A-Za-z0-9]{40,}`)

// jwtLike — гурван base64url хэсэгтэй JWT (header нь "eyJ"-ээр эхэлнэ).
var jwtLike = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)

// RedactString — мөр доторх нууцыг «***»-аар солино.
func RedactString(s string) string {
	s = secretParam.ReplaceAllString(s, "$1="+Redacted)
	s = bearer.ReplaceAllString(s, "$1 "+Redacted)
	s = fbToken.ReplaceAllString(s, "EAA"+Redacted)
	s = jwtLike.ReplaceAllString(s, "eyJ"+Redacted)
	return s
}

var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization",
	"cookie", "api_key", "apikey", "credential", "private_key",
}

// SensitiveKey — attr-ийн түлхүүр нууц утга агуулах нэр эсэх (том/жижиг үсэг үл ялгана).
func SensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// ReplaceAttr — slog.HandlerOptions.ReplaceAttr: нууц түлхүүрийн утгыг бүтнээр нь
// «***», бусад string / error утгыг RedactString-ээр шүүнэ (msg ч мөн).
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, RedactString(err.Error()))
		}
	}
	return a
}

// HandlerOptions — opts-ийн хуулбарыг ReplaceAttr-тай буцаана. opts-д өөрийн
// ReplaceAttr байсан бол эхлээд тэр, дараа нь нууц далдлалт ажиллана.
func HandlerOptions(opts *slog.HandlerOptions) *slog.HandlerOptions {
	var cp slog.HandlerOptions
	if opts != nil {
		cp = *opts
	}
	prev := cp.ReplaceAttr
	cp.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		if prev != nil {
			a = prev(groups, a)
		}
		return ReplaceAttr(groups, a)
	}
	return &cp
}
