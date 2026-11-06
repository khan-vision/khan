package jobs

import (
	"errors"
	"testing"
	"time"
)

var ub = time.FixedZone("UB", 8*3600)

func at(t *testing.T, s string, loc *time.Location) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSpecNext(t *testing.T) {
	cases := []struct {
		name, spec, from, want string
	}{
		{"every 5 min", "*/5 * * * *", "2026-11-06 12:03:20", "2026-11-06 12:05:00"},
		{"exclusive of from", "*/5 * * * *", "2026-11-06 12:05:00", "2026-11-06 12:10:00"},
		{"seconds ignored", "*/5 * * * *", "2026-11-06 12:04:59", "2026-11-06 12:05:00"},
		{"hour rollover", "*/5 * * * *", "2026-11-06 12:58:00", "2026-11-06 13:00:00"},
		{"year rollover", "0 0 * * *", "2026-12-31 23:59:00", "2027-01-01 00:00:00"},
		{"specific time", "30 9 * * *", "2026-11-06 09:30:00", "2026-11-07 09:30:00"},
		{"weekday range from Friday", "0 9 * * MON-FRI", "2026-11-06 10:00:00", "2026-11-09 09:00:00"},
		{"weekday numeric range", "0 9 * * 1-5", "2026-11-06 08:00:00", "2026-11-06 09:00:00"},
		{"sunday as 0", "0 0 * * 0", "2026-11-06 00:00:00", "2026-11-08 00:00:00"},
		{"sunday as 7", "0 0 * * 7", "2026-11-06 00:00:00", "2026-11-08 00:00:00"},
		{"range ending in 7", "0 0 * * 5-7", "2026-11-06 00:00:00", "2026-11-07 00:00:00"},
		{"dow list", "0 0 * * SAT,SUN", "2026-11-06 12:00:00", "2026-11-07 00:00:00"},
		{"leap day", "0 0 29 2 *", "2025-03-01 00:00:00", "2028-02-29 00:00:00"},
		{"day 31 skips short months", "0 0 31 * *", "2026-01-31 00:00:00", "2026-03-31 00:00:00"},
		{"day 31 after april", "0 0 31 * *", "2026-04-30 12:00:00", "2026-05-31 00:00:00"},
		{"day 30 skips february", "0 12 30 * *", "2026-01-30 12:00:00", "2026-03-30 12:00:00"},
		{"month names", "0 0 1 JAN,JUN *", "2026-02-01 00:00:00", "2026-06-01 00:00:00"},
		{"month list + step", "0 0 1 */3 *", "2026-02-01 00:00:00", "2026-04-01 00:00:00"},
		{"dom OR dow (both restricted): friday wins", "0 0 13 * FRI", "2026-11-01 00:00:00", "2026-11-06 00:00:00"},
		{"dom OR dow: dom hits later", "0 0 13 * FRI", "2026-11-07 00:00:00", "2026-11-13 00:00:00"},
		{"dom OR dow: dom on non-friday", "0 0 13 * FRI", "2026-11-13 00:00:00", "2026-11-20 00:00:00"},
		{"dom star: dow only", "0 0 * * MON", "2026-11-06 00:00:00", "2026-11-09 00:00:00"},
		{"dow star: dom only", "0 0 15 * *", "2026-11-06 00:00:00", "2026-11-15 00:00:00"},
		{"star with step on dom is still a star (AND)", "0 0 */2 * MON", "2026-11-06 00:00:00", "2026-11-09 00:00:00"},
		{"start/step", "5/15 * * * *", "2026-11-06 12:00:00", "2026-11-06 12:05:00"},
		{"start/step 2", "5/15 * * * *", "2026-11-06 12:05:00", "2026-11-06 12:20:00"},
		{"range/step", "10-30/10 * * * *", "2026-11-06 12:30:00", "2026-11-06 13:10:00"},
		{"list", "0,30 8,17 * * *", "2026-11-06 08:30:00", "2026-11-06 17:00:00"},
		{"question mark", "0 0 ? * MON", "2026-11-06 00:00:00", "2026-11-09 00:00:00"},
		{"@hourly", "@hourly", "2026-11-06 12:00:00", "2026-11-06 13:00:00"},
		{"@daily", "@daily", "2026-11-06 12:00:00", "2026-11-07 00:00:00"},
		{"@midnight", "@midnight", "2026-11-06 12:00:00", "2026-11-07 00:00:00"},
		{"@weekly", "@weekly", "2026-11-06 12:00:00", "2026-11-08 00:00:00"},
		{"@monthly", "@monthly", "2026-11-06 12:00:00", "2026-12-01 00:00:00"},
		{"@yearly", "@yearly", "2026-11-06 12:00:00", "2027-01-01 00:00:00"},
		{"@annually upper-case", "@ANNUALLY", "2026-11-06 12:00:00", "2027-01-01 00:00:00"},
		{"extra whitespace", "  */15   *  * * * ", "2026-11-06 12:00:00", "2026-11-06 12:15:00"},
		{"feb 29 only on leap day", "0 0 29 2 *", "2096-03-01 00:00:00", "2104-02-29 00:00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSpec(tc.spec, "Asia/Ulaanbaatar")
			if err != nil {
				t.Fatal(err)
			}
			got := s.Next(at(t, tc.from, ub))
			want := at(t, tc.want, ub)
			if !got.Equal(want) {
				t.Fatalf("Next(%s) = %s, want %s", tc.from, got.Format(time.DateTime), tc.want)
			}
		})
	}
}

func TestSpecEvery(t *testing.T) {
	s, err := ParseSpec("@every 10m", "")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 6, 12, 3, 0, 0, time.UTC)
	if got, want := s.Next(from), time.Date(2026, 11, 6, 12, 10, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	// Epoch-д тулгуурласан: яг tick дээр бол дараагийнх; процессын эхлэлээс хамаарахгүй.
	if got, want := s.Next(time.Date(2026, 11, 6, 12, 10, 0, 0, time.UTC)), time.Date(2026, 11, 6, 12, 20, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	s2, _ := ParseSpec("@every 1h30m", "")
	a := s2.Next(from)
	b := s2.Next(from.Add(5 * time.Minute))
	if a.Before(from) || b.Before(from) || a.Sub(from) > 90*time.Minute {
		t.Fatalf("bad every: %s %s", a, b)
	}
	// Ижил хоёр "instance" ижил tick-ийг тооцоолно.
	other, _ := ParseSpec("@every 1h30m", "Asia/Tokyo")
	if !other.Next(from).Equal(a) {
		t.Fatal("@every нь цагийн бүснээс хамаарах ёсгүй")
	}
}

func TestSpecTimezone(t *testing.T) {
	s, err := ParseSpec("0 8 * * *", "Asia/Ulaanbaatar") // UB 08:00 = 00:00 UTC
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC))
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC(), want)
	}
	if got.Location().String() != "Asia/Ulaanbaatar" {
		t.Fatalf("location=%v", got.Location())
	}
	// Анхдагч TZ нь Улаанбаатар (UTC+8).
	d, err := ParseSpec("0 8 * * *", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, off := d.Next(time.Now()).Zone(); off != 8*3600 {
		t.Fatalf("default offset=%d", off)
	}
}

func TestSpecDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata алга")
	}
	// Spring forward 2026-03-08: 02:30 орон нутгийн цаг оршдоггүй — тэр өдөр алгасаж, дараа өдөр.
	s, _ := ParseSpec("30 2 * * *", "America/New_York")
	got := s.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if got.Day() != 9 || got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("spring forward: %s", got)
	}
	// Fall back 2026-11-01: 01:30 хоёр удаа тохиолддог — нэг л удаа ажиллана.
	f, _ := ParseSpec("30 1 * * *", "America/New_York")
	first := f.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	second := f.Next(first)
	if first.Day() != 1 || second.Day() != 2 {
		t.Fatalf("fall back: first=%s second=%s", first, second)
	}
	// Тогтмол цагийн tick-үүд үргэлж урагшилна.
	h, _ := ParseSpec("*/30 * * * *", "America/New_York")
	cur := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	for i := 0; i < 200; i++ {
		n := h.Next(cur)
		if !n.After(cur) {
			t.Fatalf("урагшилсангүй: %s -> %s", cur, n)
		}
		cur = n
	}
}

func TestSpecInvalid(t *testing.T) {
	bad := []string{
		"", "   ", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 0 *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "*/-1 * * * *", "*/x * * * *", "5-1 * * * *",
		"a * * * *", "@every", "@every500ms", "@every 500ms", "@every abc", "@every -5m", "@bogus",
		"0 0 30 2 *", "0 0 31 4 *", ",1 * * * *", "1,, * * * *", "-5 * * * *", "1-2-3 * * * *",
		"1- * * * *", "+5 * * * *", "*/+5 * * * *", "1-+5 * * * *", "+1-5 * * * *", "5/+2 * * * *", "* * * +1 *",
		"* * * * FOO", "* * * FOO *", "* * * * FUNDAY", "0 0 31 2,4,6,9,11 *",
	}
	for _, spec := range bad {
		if _, err := ParseSpec(spec, ""); err == nil {
			t.Errorf("ParseSpec(%q) алдаагүй", spec)
		} else if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ParseSpec(%q): ErrInvalidSpec биш: %v", spec, err)
		}
	}
	if _, err := ParseSpec("* * * * *", "Mars/Base"); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("буруу tz: %v", err)
	}
}

// bruteNext — Next-ийн бие даасан шалгуур: минут бүрийг шалгана.
func bruteNext(s Spec, from time.Time) time.Time {
	t := from.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 60*24*800; i++ {
		domOK := s.dom&(1<<uint(t.Day())) != 0
		dowOK := s.dow&(1<<uint(t.Weekday())) != 0
		day := domOK || dowOK
		if s.domStar || s.dowStar {
			day = domOK && dowOK
		}
		if s.month&(1<<uint(t.Month())) != 0 && day &&
			s.hour&(1<<uint(t.Hour())) != 0 && s.minute&(1<<uint(t.Minute())) != 0 {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func TestSpecNextMatchesBruteForce(t *testing.T) {
	specs := []string{
		"*/7 */5 * * *", "15 3 29-31 * *", "0 0 29 2 *", "59 23 31 12 *", "0 12 * * 1,3,5",
		"30 6 1,15 * MON", "0 0 1 */2 *", "*/20 9-17 * * 1-5", "0 0 ? 2 SUN", "5 4 31 */3 *",
	}
	starts := []string{"2026-01-01 00:00:00", "2026-02-27 23:58:00", "2027-12-31 23:59:59", "2028-02-28 12:00:00"}
	for _, sp := range specs {
		s, err := ParseSpec(sp, "Asia/Ulaanbaatar")
		if err != nil {
			t.Fatalf("%s: %v", sp, err)
		}
		for _, st := range starts {
			from := at(t, st, ub)
			if got, want := s.Next(from), bruteNext(s, from); !got.Equal(want) {
				t.Errorf("%s from %s: Next=%s brute=%s", sp, st, got, want)
			}
		}
	}
}

func TestSpecString(t *testing.T) {
	s, _ := ParseSpec("  @every 5m ", "")
	if s.String() != "@every 5m" {
		t.Fatalf("String=%q", s.String())
	}
}
