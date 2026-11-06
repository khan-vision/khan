package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadSortsAndIgnores(t *testing.T) {
	fsys := fstest.MapFS{
		"0010_later.sql":      {Data: []byte("SELECT 10;")},
		"0002_second.sql":     {Data: []byte("SELECT 2;")},
		"1_first.sql":         {Data: []byte("\n\n  -- khan:no-transaction\nCREATE INDEX CONCURRENTLY i ON t(a);")},
		"README.md":           {Data: []byte("# not sql")},
		"sub/0003_nested.sql": {Data: []byte("SELECT 3;")},
	}
	ms, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("len = %d", len(ms))
	}
	want := []int64{1, 2, 10}
	for i, v := range want {
		if ms[i].Version != v {
			t.Errorf("ms[%d].Version = %d, want %d", i, ms[i].Version, v)
		}
	}
	if !ms[0].NoTx || ms[1].NoTx {
		t.Errorf("NoTx = %v,%v", ms[0].NoTx, ms[1].NoTx)
	}
	if ms[1].Name != "second" || ms[1].File() != "0002_second" {
		t.Errorf("name = %q file = %q", ms[1].Name, ms[1].File())
	}
}

func TestLoadNoTxMarkerMustBeFirstLine(t *testing.T) {
	ms, err := Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1;\n-- khan:no-transaction\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].NoTx {
		t.Fatal("marker эхний мөр биш бол NoTx=false байх ёстой")
	}
}

func TestLoadChecksum(t *testing.T) {
	a, _ := Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1;")}})
	b, _ := Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1;")}})
	c, _ := Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1; ")}})
	if a[0].Checksum != b[0].Checksum || a[0].Checksum == c[0].Checksum || len(a[0].Checksum) != 64 {
		t.Fatalf("checksum: %s %s %s", a[0].Checksum, b[0].Checksum, c[0].Checksum)
	}
	// SHA-256("SELECT 1;") тогтмол утга биш — гэхдээ hex урт 64.
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]struct {
		fsys fstest.MapFS
		want string
	}{
		"давхардсан": {fstest.MapFS{"0001_a.sql": {Data: []byte("x")}, "1_b.sql": {Data: []byte("x")}}, "давхардсан"},
		"нэр буруу":  {fstest.MapFS{"init.sql": {Data: []byte("x")}}, "нэр буруу"},
		"тайлбаргүй": {fstest.MapFS{"0001.sql": {Data: []byte("x")}}, "нэр буруу"},
		"хоосон":     {fstest.MapFS{"0001_a.sql": {Data: []byte(" \n")}}, "хоосон"},
		"хэт том":    {fstest.MapFS{"99999999999999999999_a.sql": {Data: []byte("x")}}, "хэт том"},
	}
	for name, c := range cases {
		_, err := Load(c.fsys)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestOptionsTable(t *testing.T) {
	q, s, err := Options{}.table()
	if err != nil || q != `"khan"."schema_migrations"` || s != "khan" {
		t.Fatalf("default: %q %q %v", q, s, err)
	}
	q, s, err = Options{Table: "migs"}.table()
	if err != nil || q != `"migs"` || s != "" {
		t.Fatalf("plain: %q %q %v", q, s, err)
	}
	for _, bad := range []string{"a;DROP TABLE x", "a.b.c", "a b", `a"b`, "1abc", "a.", "-- x"} {
		if _, _, err := (Options{Table: bad}).table(); err == nil {
			t.Errorf("%q: алдаа хүлээсэн", bad)
		}
	}
}

func TestTimeouts(t *testing.T) {
	l, s := Options{}.timeouts()
	if l != DefaultLockTimeout || s != DefaultStatementTimeout {
		t.Fatalf("default = %v %v", l, s)
	}
	l, s = Options{LockTimeout: -1, StatementTimeout: -1}.timeouts()
	if l != 0 || s != 0 {
		t.Fatalf("disabled = %v %v", l, s)
	}
}

func TestPlanDriftAndMissing(t *testing.T) {
	ms := []Migration{{Version: 1, Name: "a", Checksum: "aaa"}, {Version: 2, Name: "b", Checksum: "bbb"}}
	st, err := plan(ms, map[int64]appliedRow{1: {Version: 1, Checksum: "aaa"}}, Options{})
	if err != nil || !st[0].Applied || st[1].Applied {
		t.Fatalf("st = %+v err = %v", st, err)
	}
	if _, err := plan(ms, map[int64]appliedRow{1: {Version: 1, Checksum: "zzz"}}, Options{}); err == nil || !strings.Contains(err.Error(), "өөрчлөгдсөн") {
		t.Fatalf("drift err = %v", err)
	}
	gone := map[int64]appliedRow{9: {Version: 9, Name: "gone", Checksum: "x"}}
	if _, err := plan(ms, gone, Options{}); err == nil {
		t.Fatal("missing алдаа хүлээсэн")
	}
	if _, err := plan(ms, gone, Options{AllowMissing: true}); err != nil {
		t.Fatal(err)
	}
}

func TestNilDB(t *testing.T) {
	if _, err := Up(t.Context(), nil, nil, Options{}); err != ErrNilDB {
		t.Fatalf("err = %v", err)
	}
}
