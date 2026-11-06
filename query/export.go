// export.go — жагсаалтыг СЕРВЕР талд xlsx болгож татах (KvGrid v2 «Excel татах»).
//
// Run-тэй ЯГ ижил хайлт + шүүлт + whitelist эрэмбэ (prepare/baseFunc-ыг дахин
// ашиглана), гэхдээ хуудаслалтгүй — оронд нь мөрийн ХАТУУ хязгаар:
//
//	effective cap = min(ExportSpec.MaxRows (0→5000), HardMaxExportRows)
//
// `LIMIT cap+1` татаж, cap-аас хэтэрвэл таслаад `X-Kv-Export-Truncated: 1` тавина.
package query

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// HardMaxExportRows — ямар ч spec үүнээс их мөр татуулахгүй.
const HardMaxExportRows = 20000

// defaultExportRows — ExportSpec.MaxRows=0 үеийн хязгаар.
const defaultExportRows = 5000

// xlsxContentType — Excel 2007+ MIME.
const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// exportKeyRe — багана/alias нэр (SQL-д `AS <Key>` болж шууд ордог тул хатуу).
var exportKeyRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ExportColumn — xlsx-ийн нэг багана. Expr нь СЕРВЕР талд бичигдсэн SQL илэрхийлэл
// (`<Expr> AS <Key>` болж SELECT-д орно) — client-ээс хэзээ ч ирэхгүй.
type ExportColumn struct {
	Key, Header, Expr string
	// Numeric — драйвер мөр хэлбэрээр буцаадаг numeric(18,2)-ийг Excel-д тоо болгоно.
	Numeric bool
}

// ExportSpec — татах тохиргоо.
type ExportSpec struct {
	Columns []ExportColumn
	// MaxRows — 0 бол 5000; HardMaxExportRows-оос хэтэрч чадахгүй.
	MaxRows  int
	FileName string // өргөтгөлгүй; хоосон бол "export"
	Sheet    string // хоосон бол "Sheet1"
}

// Validate — spec-ийн програмчлалын алдаа (буруу Key, хоосон Expr) → panic.
// Resource.Mount эхлүүлэх үед дуудна → алдаа deploy-оос өмнө илэрнэ.
func (ex ExportSpec) Validate() {
	if len(ex.Columns) == 0 {
		panic("khan/query: ExportSpec.Columns хоосон")
	}
	seen := map[string]bool{}
	for _, col := range ex.Columns {
		if !exportKeyRe.MatchString(col.Key) {
			panic(fmt.Sprintf("khan/query: ExportColumn.Key буруу: %q", col.Key))
		}
		if strings.TrimSpace(col.Expr) == "" {
			panic(fmt.Sprintf("khan/query: ExportColumn %q-ийн Expr хоосон", col.Key))
		}
		if seen[col.Key] {
			panic(fmt.Sprintf("khan/query: ExportColumn.Key давхардсан: %q", col.Key))
		}
		seen[col.Key] = true
	}
}

// Cap — бодит мөрийн хязгаар.
func (ex ExportSpec) Cap() int {
	n := ex.MaxRows
	if n <= 0 {
		n = defaultExportRows
	}
	if n > HardMaxExportRows {
		n = HardMaxExportRows
	}
	return n
}

// ExportXLSX — хүсэлтийн хайлт/шүүлт/эрэмбээр xlsx үүсгэж хариунд бичнэ.
// Буруу шүүлт → 400 (ямар ч байт бичихээс ӨМНӨ). newQuery нь Run-тэй адил
// org_id-тай ФРЕШ query буцаана.
func ExportXLSX(c fiber.Ctx, req Request, newQuery func() *gorm.DB, spec Spec, ex ExportSpec) error {
	ex.Validate()
	p, err := spec.prepare(req)
	if err != nil {
		return Fail(c, err)
	}
	rows, err := exportQuery(spec.baseFunc(newQuery, p)(), p.order, ex).Rows()
	if err != nil {
		return Fail(c, err)
	}
	defer rows.Close()
	return sendXLSX(c, ex, rowIter(rows, len(ex.Columns)))
}

// exportQuery — SELECT <Expr> AS <Key>, ... ORDER BY <whitelist> LIMIT cap+1.
func exportQuery(base *gorm.DB, order string, ex ExportSpec) *gorm.DB {
	ex.Validate()
	sels := make([]string, len(ex.Columns))
	for i, col := range ex.Columns {
		sels[i] = col.Expr + " AS " + col.Key
	}
	q := base.Select(strings.Join(sels, ", "))
	if order != "" {
		q = q.Order(order)
	}
	return q.Limit(ex.Cap() + 1)
}

// rowIter — *sql.Rows-ийг writeXLSX-ийн iterator болгоно.
func rowIter(rows *sql.Rows, n int) func() ([]interface{}, bool, error) {
	return func() ([]interface{}, bool, error) {
		if !rows.Next() {
			return nil, false, rows.Err()
		}
		vals := make([]interface{}, n)
		ptrs := make([]interface{}, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		return vals, true, nil
	}
}

// sendXLSX — файлыг buffer-т бүтээгээд (мөрийн тоо header-т орох ёстой тул)
// header-үүдтэй нь хариунд илгээнэ. Хэмжээ нь HardMaxExportRows-оор хязгаартай.
func sendXLSX(c fiber.Ctx, ex ExportSpec, next func() ([]interface{}, bool, error)) error {
	var buf bytes.Buffer
	written, truncated, err := writeXLSXSheet(&buf, ex.Sheet, ex.Columns, next, ex.Cap())
	if err != nil {
		return Fail(c, err)
	}
	c.Set(fiber.HeaderContentType, xlsxContentType)
	c.Set(fiber.HeaderContentDisposition, contentDisposition(ex.FileName))
	c.Set("X-Kv-Export-Rows", strconv.Itoa(written))
	if truncated {
		c.Set("X-Kv-Export-Truncated", "1")
	} else {
		c.Set("X-Kv-Export-Truncated", "0")
	}
	c.Set("Access-Control-Expose-Headers", "Content-Disposition, X-Kv-Export-Rows, X-Kv-Export-Truncated")
	return c.Send(buf.Bytes())
}

// writeXLSX — header (bold) + хамгийн ихдээ cap мөр бичнэ. cap+1 дэх мөр ирвэл truncated.
func writeXLSX(w io.Writer, cols []ExportColumn, next func() ([]interface{}, bool, error), cap int) (int, bool, error) {
	return writeXLSXSheet(w, "", cols, next, cap)
}

func writeXLSXSheet(w io.Writer, sheet string, cols []ExportColumn, next func() ([]interface{}, bool, error), cap int) (written int, truncated bool, err error) {
	f := excelize.NewFile()
	defer f.Close()

	name := sheetName(sheet)
	if name != "Sheet1" {
		if err = f.SetSheetName("Sheet1", name); err != nil {
			return 0, false, err
		}
	}
	sw, err := f.NewStreamWriter(name)
	if err != nil {
		return 0, false, err
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return 0, false, err
	}
	if len(cols) > 0 {
		if err = sw.SetColWidth(1, len(cols), 22); err != nil {
			return 0, false, err
		}
	}
	header := make([]interface{}, len(cols))
	for i, col := range cols {
		h := col.Header
		if h == "" {
			h = col.Key
		}
		header[i] = excelize.Cell{StyleID: bold, Value: h}
	}
	if err = sw.SetRow("A1", header); err != nil {
		return 0, false, err
	}

	for {
		vals, ok, nerr := next()
		if nerr != nil {
			return written, false, nerr
		}
		if !ok {
			break
		}
		if written >= cap {
			truncated = true
			break
		}
		row := make([]interface{}, len(cols))
		for i := range cols {
			var v interface{}
			if i < len(vals) {
				v = vals[i]
			}
			row[i] = cellValue(v, cols[i].Numeric)
		}
		cell, cerr := excelize.CoordinatesToCellName(1, written+2)
		if cerr != nil {
			return written, false, cerr
		}
		if err = sw.SetRow(cell, row); err != nil {
			return written, false, err
		}
		written++
	}
	if err = sw.Flush(); err != nil {
		return written, truncated, err
	}
	if _, err = f.WriteTo(w); err != nil {
		return written, truncated, err
	}
	return written, truncated, nil
}

// cellValue — драйверын утгыг Excel нүдэнд тохиромжтой болгоно.
func cellValue(v interface{}, numeric bool) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return cellValue(string(t), numeric)
	case [16]byte: // uuid
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case string:
		if numeric {
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
				return f
			}
		}
		return t
	case time.Time:
		return t
	default:
		return t
	}
}

// sheetName — Excel-ийн хуудасны нэрийн дүрэм (≤31, `:\/?*[]` хориотой).
func sheetName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) {
			return '_'
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > 31 {
		s = string([]rune(s)[:31])
	}
	if s == "" {
		return "Sheet1"
	}
	return s
}

// contentDisposition — ASCII-safe filename + Монгол нэрд filename*=UTF-8”.
func contentDisposition(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".xlsx")
	if name == "" {
		name = "export"
	}
	ascii := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, name)
	if strings.Trim(ascii, "_.") == "" {
		ascii = "export"
	}
	out := `attachment; filename="` + ascii + `.xlsx"`
	if ascii != name {
		out += "; filename*=UTF-8''" + rfc5987(name+".xlsx")
	}
	return out
}

// rfc5987 — attr-char-аас бусад бүх байтыг %XX болгоно.
func rfc5987(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", ch) >= 0 {
			b.WriteByte(ch)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", ch)
	}
	return b.String()
}
