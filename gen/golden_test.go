package gen

import (
	"strings"
	"testing"
)

// goldenCases — golden тест + compile тестийн нийтлэг жагсаалт.
func goldenCases(t *testing.T) []struct {
	name string
	opts Options
} {
	t.Helper()
	return []struct {
		name string
		opts Options
	}{
		// Энгийн: нийлмэл PK (org_id,id), int identity, composite unique, формоос шошго.
		{"lut_period", Options{Table: "attendance.lut_period", Form: testForm(t, "form_795_lut_period.json"), Label: "Чөлөөний хугацаа"}},
		// Relation + статик options + grid (export, sort, filter, paging).
		{"beacon_request", Options{Table: "attendance.beacon_request", Form: testForm(t, "form_892_beacon_request.json"), Grid: testGrid(t, "grid_894_beacon_request.json"), Label: "Beacon хүсэлт"}},
		// Subform (Children), hidden/disabled, fillByUser, custom элемент, cascade Ref.
		{"tasks", Options{Table: "planning.tasks", Form: testForm(t, "form_1018_tasks_subform.json"), Label: "Ажил", RefLabels: map[string][]string{"emp_id": {"lastname", "firstname"}}}},
		// Форм/grid-гүй: бүх төрөл, FK, unique, version, soft delete, track user.
		{"invoice", Options{Table: "finance.invoice", Package: "finance"}},
	}
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			r := mustGen(t, tc.opts)
			golden(t, tc.name, r.Source)
		})
	}
}

// Ижил оролт → ижил гаралт (тогтвортой, map-ийн дараалалгүй).
func TestDeterministic(t *testing.T) {
	for _, tc := range goldenCases(t) {
		a := mustGen(t, tc.opts)
		for i := 0; i < 5; i++ {
			b := mustGen(t, tc.opts)
			if string(a.Source) != string(b.Source) {
				t.Fatalf("%s: гаралт тогтвортой биш", tc.name)
			}
		}
	}
}

func TestGoldenSemantics(t *testing.T) {
	t.Run("composite PK → ID id", func(t *testing.T) {
		r := mustGen(t, Options{Table: "attendance.lut_period"})
		src := squash(string(r.Source))
		for _, want := range []string{"ID: crud.IDSpec{Kind: crud.IDInt}", `Unique: true`, "SoftDelete: true", "нийлмэл"} {
			if !strings.Contains(src, want) {
				t.Errorf("%q алга:\n%s", want, src)
			}
		}
	})
	t.Run("invoice mapping", func(t *testing.T) {
		src := squash(string(mustGen(t, Options{Table: "finance.invoice"}).Source))
		for _, want := range []string{
			"Kind: validate.Money", "Kind: validate.Int,", "Scale: 4", "validate.Bound(-32768)",
			`Version: "version"`, "TrackUser: true", "validate.Enum", `"draft", "posted", "void"`,
			"Tenant: lookup.TenantOff()", `Label: "Нэхэмжлэхийн дугаар"`, "validate.JSON", "validate.Time",
			"DB-д глобал unique", "[column-type] tags", "[column-type] shape",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%q алга", want)
			}
		}
		if strings.Contains(src, `Name: "search_vector"`) || strings.Contains(src, `"version": "t.version"`) {
			t.Error("generated/version багана бичигдэх талбар эсвэл sort болсон")
		}
	})
}

// squash — gofmt-ийн зэрэгцүүлсэн хоосон зайг нэгэн төрөлд оруулна.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
