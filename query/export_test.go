package query

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

func roomExport() ExportSpec {
	return ExportSpec{
		Columns: []ExportColumn{
			{Key: "room_name", Header: "Хурлын өрөөний нэр", Expr: "r.room_name"},
			{Key: "quantity_fit_number", Header: "Оролцогчийн тоо", Expr: "r.quantity_fit_number", Numeric: true},
		},
		MaxRows:  10,
		FileName: "Хурлын өрөө",
	}
}

func roomSpec() Spec {
	return Spec{
		Search:       []string{"r.room_name", "r.address"},
		Sort:         map[string]string{"room_name": "r.room_name"},
		Filters:      map[string]FilterCol{"room_name": Text("r.room_name"), "quantity_fit_number": Number("r.quantity_fit_number")},
		DefaultOrder: "r.room_name DESC, r.id DESC",
	}
}

// (a) exportQuery: LIMIT cap+1, org предикат, шүүлт/хайлт, whitelist ORDER BY.
func TestExportQuery_SQL(t *testing.T) {
	db, _ := captureDB(t)
	spec := roomSpec()
	ex := roomExport()
	newQuery := func() *gorm.DB {
		return db.Session(&gorm.Session{DryRun: true, NewDB: true}).
			Table("organization.conference_room r").Where("r.org_id = ?", "org-A")
	}
	p, err := spec.prepare(Request{
		Search:  "танхим",
		Sort:    "room_name",
		Dir:     "asc",
		Filters: `{"quantity_fit_number":{"op":"gte","v":10}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]interface{}
	stmt := exportQuery(spec.baseFunc(newQuery, p)(), p.order, ex).Find(&rows).Statement
	sql := stmt.SQL.String()
	for _, want := range []string{
		"r.room_name AS room_name", "r.quantity_fit_number AS quantity_fit_number",
		"r.org_id = $1", "ILIKE", "r.quantity_fit_number >=", "ORDER BY r.room_name ASC", "LIMIT",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL-д %q алга: %s", want, sql)
		}
	}
	if stmt.Vars[0] != "org-A" {
		t.Errorf("org bind буруу: %#v", stmt.Vars)
	}
	if last := stmt.Vars[len(stmt.Vars)-1]; last != 11 {
		t.Errorf("LIMIT нь cap+1=11 байх ёстой, got %#v", last)
	}
	// Default order (sort өгөөгүй / whitelist-гүй).
	p2, _ := spec.prepare(Request{Sort: "address"})
	stmt = exportQuery(spec.baseFunc(newQuery, p2)(), p2.order, ex).Find(&rows).Statement
	if !strings.Contains(stmt.SQL.String(), "ORDER BY r.room_name DESC, r.id DESC") {
		t.Errorf("DefaultOrder алга: %s", stmt.SQL.String())
	}
}

func fakeRows(n int) func() ([]interface{}, bool, error) {
	i := 0
	return func() ([]interface{}, bool, error) {
		if i >= n {
			return nil, false, nil
		}
		i++
		return []interface{}{[]byte(fmt.Sprintf("Өрөө %d", i)), fmt.Sprintf("%d.50", i)}, true, nil
	}
}

// (b) 25 мөр, cap 10 → header + 10 мөр, truncated.
func TestWriteXLSX_CapAndTruncate(t *testing.T) {
	var buf bytes.Buffer
	written, truncated, err := writeXLSX(&buf, roomExport().Columns, fakeRows(25), 10)
	if err != nil {
		t.Fatal(err)
	}
	if written != 10 || !truncated {
		t.Fatalf("written=%d truncated=%v", written, truncated)
	}
	f, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 11 {
		t.Fatalf("1 header + 10 мөр хүлээсэн, got %d", len(rows))
	}
	if rows[0][0] != "Хурлын өрөөний нэр" || rows[1][0] != "Өрөө 1" || rows[10][0] != "Өрөө 10" {
		t.Fatalf("агуулга буруу: %v", rows)
	}
	// Numeric багана тоон нүд болсон.
	if typ, _ := f.GetCellType("Sheet1", "B2"); typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString {
		t.Errorf("numeric багана текст болж бичигдсэн (type=%v)", typ)
	}
	// Header bold.
	sid, _ := f.GetCellStyle("Sheet1", "A1")
	st, _ := f.GetStyle(sid)
	if st == nil || st.Font == nil || !st.Font.Bold {
		t.Errorf("header bold биш")
	}

	// cap-аас бага → truncated=false.
	buf.Reset()
	written, truncated, err = writeXLSX(&buf, roomExport().Columns, fakeRows(3), 10)
	if err != nil || written != 3 || truncated {
		t.Fatalf("3 мөр: written=%d truncated=%v err=%v", written, truncated, err)
	}
	// Яг cap → truncated=false.
	buf.Reset()
	written, truncated, _ = writeXLSX(&buf, roomExport().Columns, fakeRows(10), 10)
	if written != 10 || truncated {
		t.Fatalf("яг cap: written=%d truncated=%v", written, truncated)
	}
}

// (c) Spec юу ч хэлсэн HardMaxExportRows-оос хэтрэхгүй.
func TestExportSpec_Cap(t *testing.T) {
	if got := (ExportSpec{MaxRows: 1_000_000}).Cap(); got != HardMaxExportRows {
		t.Fatalf("cap=%d, want %d", got, HardMaxExportRows)
	}
	if got := (ExportSpec{}).Cap(); got != 5000 {
		t.Fatalf("анхдагч cap=%d, want 5000", got)
	}
	if got := (ExportSpec{MaxRows: 100}).Cap(); got != 100 {
		t.Fatalf("cap=%d, want 100", got)
	}
}

// (d) whitelist-гүй шүүлт → FilterError (ExportXLSX-ийн prepare алхам).
func TestExport_UnknownFilterIsFilterError(t *testing.T) {
	_, err := roomSpec().prepare(Request{Filters: `{"address":{"op":"contains","v":"x"}}`})
	if !IsFilterError(err) {
		t.Fatalf("FilterError хүлээсэн, got %v", err)
	}
}

func TestExportSpec_ValidatePanics(t *testing.T) {
	bad := []ExportSpec{
		{},
		{Columns: []ExportColumn{{Key: "Room Name", Expr: "r.x"}}},
		{Columns: []ExportColumn{{Key: "x; DROP", Expr: "r.x"}}},
		{Columns: []ExportColumn{{Key: "x", Expr: ""}}},
		{Columns: []ExportColumn{{Key: "x", Expr: "r.x"}, {Key: "x", Expr: "r.y"}}},
	}
	for i, ex := range bad {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("#%d panic хүлээсэн", i)
				}
			}()
			ex.Validate()
		}()
	}
}

func TestContentDisposition(t *testing.T) {
	if got := contentDisposition(""); got != `attachment; filename="export.xlsx"` {
		t.Errorf("хоосон: %s", got)
	}
	if got := contentDisposition("rooms"); got != `attachment; filename="rooms.xlsx"` {
		t.Errorf("ascii: %s", got)
	}
	got := contentDisposition("Хурлын өрөө")
	want := `attachment; filename="export.xlsx"; filename*=UTF-8''%D0%A5%D1%83%D1%80%D0%BB%D1%8B%D0%BD%20%D3%A9%D1%80%D3%A9%D3%A9.xlsx`
	if got != want {
		t.Errorf("монгол нэр:\n got %s\nwant %s", got, want)
	}
	if strings.ContainsAny(contentDisposition(`a"b\r\n`), "\r\n") {
		t.Errorf("header injection")
	}
}

// sendXLSX — header-үүд (rows/truncated) + xlsx body.
func TestSendXLSX_Headers(t *testing.T) {
	app := fiber.New()
	app.Get("/x", func(c fiber.Ctx) error {
		return sendXLSX(c, roomExport(), fakeRows(25))
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/x", nil), noTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != xlsxContentType {
		t.Errorf("content-type: %s", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Kv-Export-Rows") != "10" || resp.Header.Get("X-Kv-Export-Truncated") != "1" {
		t.Errorf("rows/truncated header: %s / %s", resp.Header.Get("X-Kv-Export-Rows"), resp.Header.Get("X-Kv-Export-Truncated"))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Errorf("disposition: %s", resp.Header.Get("Content-Disposition"))
	}
	body, _ := io.ReadAll(resp.Body)
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("xlsx уншигдсангүй: %v", err)
	}
	defer f.Close()
	rows, _ := f.GetRows("Sheet1")
	if len(rows) != 11 {
		t.Fatalf("мөр %d", len(rows))
	}
}
