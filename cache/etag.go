package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// ETag — `W/"<version>-<hash16>"`. hash16 нь parts-ийн sha256-ийн эхний 8 байт (hex); хэсэг бүр
// урт-угтвартай тул хилийн үүрэг ялгагдана. version нь Cache.Version-ий "g12.g3" хэлбэр.
func ETag(version string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(p)))])
		h.Write([]byte(p))
	}
	return `W/"` + version + "-" + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
}

// ETagMatch — `If-None-Match` толгой нь etag-тай таарч байгаа эсэх (weak харьцуулалт, таслалаар
// тусгаарласан жагсаалт). `*`-ийг ЗӨВШӨӨРӨХГҮЙ (false) — байхгүй мөрийн detail-д 304 өгөхөөс сэргийлнэ.
func ETagMatch(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" || etag == "" {
		return false
	}
	want := strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	for _, tok := range strings.Split(ifNoneMatch, ",") {
		tok = strings.TrimPrefix(strings.TrimSpace(tok), "W/")
		if tok != "" && tok != "*" && tok == want {
			return true
		}
	}
	return false
}
