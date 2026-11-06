package upload

import (
	"strconv"
	"strings"
)

// cyrillic — Монгол кирилл үсгийн латин галиг (уншигдахуйц файлын нэр гаргахад).
var cyrillic = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j",
	'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'ө': "u", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ү': "u", 'ф': "f",
	'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sh", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

const (
	maxBaseLen    = 80
	fallbackName  = "file"
	thumbPrefix   = "thumb_"
	counterTries  = 100 // `name-1` … `name-99`
	randomTries   = 20  // дараа нь санамсаргүй дагавар
	maxNameTrials = counterTries + randomTries
)

// splitFileName — клиентийн нэрийг (`/`, `\` замыг хасч) цэвэр base ба жижиг
// өргөтгөл (цэггүй) болгоно. Base нь зөвхөн [a-z0-9_-], хоосон бол "file".
// `shell.php.jpg` → base "shell_php", ext "jpg" (давхар өргөтгөл үлдэхгүй).
func splitFileName(client string) (base, ext string) {
	if i := strings.LastIndexAny(client, `/\`); i >= 0 {
		client = client[i+1:]
	}
	client = strings.ToLower(client)
	if i := strings.LastIndexByte(client, '.'); i > 0 {
		ext = client[i+1:]
		client = client[:i]
	} else if i == 0 {
		client = client[1:]
	}
	ext = keepASCII(ext)

	var b strings.Builder
	lastSep := true // эхэнд тусгаарлагч бичихгүй
	for _, r := range client {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '-':
			if !lastSep {
				b.WriteByte('-')
			}
			lastSep = true
		default:
			if tr, ok := cyrillic[r]; ok {
				b.WriteString(tr)
				lastSep = lastSep && tr == ""
				continue
			}
			if !lastSep {
				b.WriteByte('_')
			}
			lastSep = true
		}
	}
	base = strings.Trim(b.String(), "_-")
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "_-")
	}
	// `thumb_` угтвар нь thumbnail-д нөөцлөгдсөн (Serve-ийн fallback-тай зөрчилдөхгүй).
	for strings.HasPrefix(base, thumbPrefix) {
		base = strings.TrimLeft(strings.TrimPrefix(base, thumbPrefix), "_-")
	}
	if base == "" {
		base = fallbackName
	}
	return base, ext
}

// keepASCII — өргөтгөлд зөвхөн [a-z0-9] үлдээнэ (үлдсэн нь allowlist-д таарахгүй).
func keepASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			return "" // сэжигтэй өргөтгөл: allowlist-д таарахгүй
		}
	}
	return b.String()
}

// candidateName — i-р оролдлогын нэр: 0 → `base.ext`, 1..99 → `base-N.ext`,
// түүнээс хойш `base-<random>.ext` (өндөр зэрэгцээ ачаалалд хурдан шийдэгдэнэ).
// candidateName — хадгалах нэрийн i дэх хувилбар. Анхдагч: `<base>-<16 hex>.<ext>`
// (таашгүй, давхцал бараг үгүй). LambdaCompat: `<base>.<ext>`, `<base>-1.<ext>` …
func (u *Uploader) candidateName(base, ext string, i int, compat bool) string {
	if !compat {
		return base + "-" + randomHex(8) + "." + ext
	}
	switch {
	case i == 0:
		return base + "." + ext
	case i < counterTries:
		return base + "-" + strconv.Itoa(i) + "." + ext
	default:
		return base + "-" + randomHex(4) + "." + ext
	}
}
