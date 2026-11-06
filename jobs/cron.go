package jobs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTZ — Schedule.TZ хоосон үед хэрэглэгдэх цагийн бүс.
const DefaultTZ = "Asia/Ulaanbaatar"

// ErrInvalidSpec — cron spec задрахгүй байна.
var ErrInvalidSpec = errors.New("jobs: cron spec буруу")

// Spec — задалсан cron хуваарь. Дэмжих хэлбэр:
//
//   - 5 талбар: "минут цаг сар_өдөр сар гараг" ("*/5 * * * *", "0 9 * * MON-FRI")
//     талбар бүрт `*`, `?`, жагсаалт `1,5`, муж `1-5`, алхам `*/10`, `10-30/5`, `5/15`;
//     сар (JAN-DEC), гараг (SUN-SAT; 0 ба 7 хоёул Ням) нэрээр бичиж болно.
//     Сарын өдөр БА гараг хоёулаа хязгаарласан бол (Vixie cron-той адил) аль нэг нь
//     таарахад ажиллана; нэг нь `*` бол хоёул таарах ёстой.
//   - Тодорхойлогч: @yearly/@annually, @monthly, @weekly, @daily/@midnight, @hourly.
//   - "@every 10m": тогтмол интервал (>= 1с). Epoch-д тулгуурласан тул бүх
//     instance-д ижил tick цаг гарна (процесс эхэлсэн цагаас хамаарахгүй).
//
// Нэг удаа үүсгээд олон goroutine-аас зэрэг ашиглаж болно (өөрчлөгддөггүй).
type Spec struct {
	raw   string
	loc   *time.Location
	every time.Duration // > 0 бол @every

	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// String — анхны spec текст.
func (s Spec) String() string { return s.raw }

// Location — spec-ийн цагийн бүс.
func (s Spec) Location() *time.Location { return s.loc }

// LoadLocation — цагийн бүс ачаална. "" → DefaultTZ. Систем tzdata-гүй (жижиг
// container) үед DefaultTZ нь DST-гүй UTC+8 тогтмол бүсээр орлогдоно (Монгол
// 2017 оноос DST-гүй).
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		name = DefaultTZ
	}
	loc, err := time.LoadLocation(name)
	if err == nil {
		return loc, nil
	}
	if name == DefaultTZ {
		return time.FixedZone(DefaultTZ, 8*3600), nil
	}
	return nil, fmt.Errorf("%w: цагийн бүс %q: %v", ErrInvalidSpec, name, err)
}

// ParseSpec — cron spec-ийг tz цагийн бүсэд задлана ("" → DefaultTZ).
func ParseSpec(spec, tz string) (Spec, error) {
	loc, err := LoadLocation(tz)
	if err != nil {
		return Spec{}, err
	}
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return Spec{}, fmt.Errorf("%w: хоосон", ErrInvalidSpec)
	}
	s := Spec{raw: raw, loc: loc}

	if strings.HasPrefix(raw, "@") {
		return parseDescriptor(s)
	}

	return parseFields(s, raw)
}

// parseFields — 5 талбарт cron илэрхийллийг s-д задлана.
func parseFields(s Spec, expr string) (Spec, error) {
	raw := s.raw
	loc := s.loc
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Spec{}, fmt.Errorf("%w: %q — 5 талбар шаардлагатай (минут цаг өдөр сар гараг), %d байна", ErrInvalidSpec, raw, len(fields))
	}
	var e1, e2, e3, e4, e5 error
	s.minute, _, e1 = parseField(fields[0], 0, 59, 59, nil)
	s.hour, _, e2 = parseField(fields[1], 0, 23, 23, nil)
	s.dom, s.domStar, e3 = parseField(fields[2], 1, 31, 31, nil)
	s.month, _, e4 = parseField(fields[3], 1, 12, 12, monthNames)
	s.dow, s.dowStar, e5 = parseField(fields[4], 0, 7, 6, dowNames)
	for i, e := range []error{e1, e2, e3, e4, e5} {
		if e != nil {
			return Spec{}, fmt.Errorf("%w: %q — %s: %v", ErrInvalidSpec, raw, fieldNames[i], e)
		}
	}
	// 7 = Ням = 0.
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	// Хэзээ ч таарахгүй spec (ж: "0 0 30 2 *") ажиллах үед чимээгүй зогсохгүй, эхэндээ татгалзана.
	ref := time.Date(2000, 1, 1, 0, 0, 0, 0, loc)
	if s.Next(ref).IsZero() {
		return Spec{}, fmt.Errorf("%w: %q — хэзээ ч таарахгүй (жнь 2-р сарын 30)", ErrInvalidSpec, raw)
	}
	return s, nil
}

var fieldNames = [...]string{"минут", "цаг", "сарын өдөр", "сар", "гараг"}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseDescriptor(s Spec) (Spec, error) {
	raw := s.raw
	lower := strings.ToLower(raw)
	if rest, ok := strings.CutPrefix(lower, "@every"); ok {
		if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			return Spec{}, fmt.Errorf("%w: %q — @every <хугацаа> хэлбэртэй байх ёстой", ErrInvalidSpec, raw)
		}
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return Spec{}, fmt.Errorf("%w: %q — %v", ErrInvalidSpec, raw, err)
		}
		if d < time.Second {
			return Spec{}, fmt.Errorf("%w: %q — интервал хамгийн багадаа 1с", ErrInvalidSpec, raw)
		}
		s.every = d
		return s, nil
	}
	var expr string
	switch lower {
	case "@yearly", "@annually":
		expr = "0 0 1 1 *"
	case "@monthly":
		expr = "0 0 1 * *"
	case "@weekly":
		expr = "0 0 * * 0"
	case "@daily", "@midnight":
		expr = "0 0 * * *"
	case "@hourly":
		expr = "0 * * * *"
	default:
		return Spec{}, fmt.Errorf("%w: %q — үл мэдэгдэх тодорхойлогч", ErrInvalidSpec, raw)
	}
	return parseFields(s, expr)
}

// parseField — нэг cron талбарыг битмаск болгоно. hi нь зөвшөөрөх дээд утга,
// starHi нь `*`-ийн дээд хязгаар (гаражид 6, хэдий 7 зөвшөөрөгдөх ч).
func parseField(field string, lo, hi, starHi int, names map[string]int) (mask uint64, star bool, err error) {
	if field == "" {
		return 0, false, errors.New("хоосон")
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, false, errors.New("хоосон жагсаалтын элемент")
		}
		expr, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, perr := atoiDigits(stepStr)
			if perr != nil || n < 1 {
				return 0, false, fmt.Errorf("алхам %q буруу", stepStr)
			}
			step = n
		}
		var a, b int
		switch {
		case expr == "*" || expr == "?":
			a, b = lo, starHi
			star = true
		case strings.Contains(expr, "-"):
			l, r, _ := strings.Cut(expr, "-")
			if a, err = parseValue(l, names); err != nil {
				return 0, false, err
			}
			if b, err = parseValue(r, names); err != nil {
				return 0, false, err
			}
		default:
			if a, err = parseValue(expr, names); err != nil {
				return 0, false, err
			}
			b = a
			if hasStep {
				b = starHi // "5/15" = 5-дээс төгсгөл хүртэл
			}
		}
		if a < lo || b > hi {
			return 0, false, fmt.Errorf("%q хүрээнээс (%d-%d) хэтэрсэн", expr, lo, hi)
		}
		if a > b {
			return 0, false, fmt.Errorf("муж %q урвуу", expr)
		}
		for v := a; v <= b; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, star, nil
}

// atoiDigits — зөвхөн ASCII цифрээс бүрдсэн бүхэл тоо ('+', '-', зай, '_' татгалзана).
func atoiDigits(s string) (int, error) {
	if s == "" {
		return 0, errors.New("хоосон")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("цифр биш: %q", s)
		}
	}
	return strconv.Atoi(s)
}

func parseValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	n, err := atoiDigits(s)
	if err != nil {
		return 0, fmt.Errorf("утга %q буруу", s)
	}
	return n, nil
}

// Next — t-ээс ЭНГЭЭС хойших (t өөрөө үл багтана) дараагийн tick. Таарах цаг
// 9 жилийн дотор олдохгүй бол тэг утга. Үр дүн Spec-ийн цагийн бүсэд.
//
// DST-тэй бүсэд оршдоггүй (spring-forward) орон нутгийн цаг алгасагдана.
func (s Spec) Next(t time.Time) time.Time {
	if s.every > 0 {
		iv := int64(s.every)
		n := t.UnixNano()
		// Эерэг epoch (1970+)-д зориулсан; floor хуваалт сөрөг утгад ч зөв.
		q := n / iv
		if n%iv < 0 {
			q--
		}
		return time.Unix(0, (q+1)*iv).In(s.loc)
	}
	t = t.In(s.loc)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, s.loc).Add(time.Minute)
	limit := t.Year() + 9
	for t.Year() <= limit {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = advance(t, time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc), 24*time.Hour)
		case !s.dayMatches(t):
			t = advance(t, time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc), time.Hour)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = advance(t, time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, s.loc), time.Hour)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = advance(t, time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, s.loc), time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// advance — DST ambiguity-д time.Date хойш бус цаг буцаахаас хамгаалж, заавал урагшлуулна.
func advance(cur, next time.Time, minStep time.Duration) time.Time {
	if next.After(cur) {
		return next
	}
	return cur.Add(minStep)
}

func (s Spec) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domOK && dowOK
	}
	return domOK || dowOK
}
