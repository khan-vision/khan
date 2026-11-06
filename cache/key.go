package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalidKey — Key-ийн org/resource хоосон байна. Cache-ийг алгасахаас өөр зам байхгүй (fail-closed).
var ErrInvalidKey = errors.New("khan/cache: буруу түлхүүр (org/resource хоосон)")

// Key — нэг cache entry-ийг тодорхойлно.
//
// Parts = хүсэлтийг нэг утгатай болгох (method биш — агуулга) нормчилсон хэсгүүд: route, нормчилсон
// query/body г.м. Хэсэг бүр урт-угтвартай хэшлэгдэнэ тул `["a","bc"]` ≠ `["ab","c"]`.
type Key struct {
	OrgID     string
	Resource  string
	DependsOn []string // generation нь түлхүүрт орох нэмэлт resource-ууд
	Parts     []string
}

// resources — generation уншигдах resource-ууд: эхлээд Resource, дараа нь DependsOn (давхцалгүй, эрэмбэлсэн).
func (k Key) resources() ([]string, error) {
	if k.OrgID == "" || k.Resource == "" {
		return nil, ErrInvalidKey
	}
	deps := make([]string, 0, len(k.DependsOn))
	seen := map[string]bool{k.Resource: true}
	for _, d := range k.DependsOn {
		if d == "" {
			return nil, ErrInvalidKey
		}
		if !seen[d] {
			seen[d] = true
			deps = append(deps, d)
		}
	}
	sort.Strings(deps)
	return append([]string{k.Resource}, deps...), nil
}

// hash — Parts + DependsOn-ийн sha256 (hex). DependsOn-ийг оруулсан нь deploy-ийн дундуур dependency
// өөрчлөгдөхөд (gens ижил тоо байсан ч) хуучин entry-тэй мөргөлдөхөөс сэргийлнэ.
func (k Key) hash(res []string) string {
	h := sha256.New()
	frame := func(s string) {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
		h.Write([]byte(s))
	}
	frame("deps")
	for _, r := range res[1:] {
		frame(r)
	}
	frame("parts")
	for _, p := range k.Parts {
		frame(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// escape — `[a-z0-9_.-]`-ээс бусад байт бүрийг `%XX` болгоно ("%" өөрөө ч). Ингэснээр ':' түлхүүрийн
// хэсгийг хуураж өөр org/resource-ийн орон зайд орж чадахгүй.
func escape(s string) string {
	const hexd = "0123456789ABCDEF"
	clean := true
	for i := 0; i < len(s); i++ {
		if !safeByte(s[i]) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if safeByte(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexd[c>>4])
		b.WriteByte(hexd[c&15])
	}
	return b.String()
}

func safeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// versionString — generation-уудыг "g12.g3" хэлбэрт оруулна.
func versionString(gens []uint64) string {
	var b strings.Builder
	for i, g := range gens {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteByte('g')
		b.WriteString(strconv.FormatUint(g, 10))
	}
	return b.String()
}
