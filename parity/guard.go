package parity

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// IsLoopbackURL — base URL нь loopback (127.0.0.0/8, ::1, localhost) эсэх.
func IsLoopbackURL(raw string) (bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return false, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false, fmt.Errorf("scheme http/https байх ёстой: %q", raw)
	}
	h := strings.ToLower(u.Hostname())
	if h == "" {
		return false, fmt.Errorf("host хоосон: %q", raw)
	}
	if h == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback(), nil
}

// safeMethod — remote хаягт зөвшөөрөгдөх цорын ганц method-ууд.
func safeMethod(m string) bool { return m == "GET" || m == "HEAD" }

// CheckTargets — хоёр base URL-ийг шалгана.
//
// Дүрэм (production-д санамсаргүй хүсэлт явуулахаас сэргийлнэ):
//   - loopback биш хаяг -allow-remote байхгүй бол ТАТГАЛЗАНА.
//   - allow-remote байсан ч зөвхөн GET/HEAD (өгөгдөл өөрчлөхгүй) — корпус дотор
//     POST байсан ч remote-д ХЭЗЭЭ Ч илгээхгүй.
//   - allow-mutations нь хоёр URL хоёулаа loopback үед л хүчинтэй (тус бүр өөрийн scratch DB-тэй).
//
// Буцаах утга: remote (аль нэг нь loopback биш) эсэх.
func CheckTargets(a, b string, allowRemote, allowMutations bool) (remote bool, err error) {
	for _, s := range []string{a, b} {
		lo, e := IsLoopbackURL(s)
		if e != nil {
			return false, e
		}
		if !lo {
			remote = true
		}
	}
	if remote && !allowRemote {
		return true, fmt.Errorf("loopback биш хаяг (%s, %s): production руу санамсаргүй хүсэлт явуулахгүйн тулд татгалзлаа; зориуд бол -allow-remote (зөвхөн GET/HEAD)", a, b)
	}
	if remote && allowMutations {
		return true, fmt.Errorf("-allow-mutations нь зөвхөн хоёр URL хоёулаа loopback үед зөвшөөрөгдөнө")
	}
	return remote, nil
}
