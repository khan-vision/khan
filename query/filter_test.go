package query

import (
	"strings"
	"testing"
)

// testSpec — тестэд хэрэглэх whitelist.
func testSpec() Spec {
	return Spec{
		Filters: map[string]FilterCol{
			"invoice_number": Text("i.invoice_number"),
			"party_name":     Text("p.name"),
			"total_amount":   Number("i.total_amount"),
			"invoice_date":   Date("i.invoice_date"),
			"status":         Enum("i.status"),
			"is_posted":      Bool("i.is_posted"),
			"account_id":     Account("cb.bank_account_id"),
		},
	}
}

func mustParse(t *testing.T, raw string) []condSQL {
	t.Helper()
	got, err := testSpec().parseFilters(raw)
	if err != nil {
		t.Fatalf("parseFilters(%s) алдаа: %v", raw, err)
	}
	return got
}

// ─── Whitelist / injection ───

func TestParseFilters_RejectsUnknownColumn(t *testing.T) {
	raw := `{"i.total_amount = 1 OR 1=1--":{"op":"eq","v":1}}`
	_, err := testSpec().parseFilters(raw)
	if !IsFilterError(err) {
		t.Fatalf("whitelist-д байхгүй баганыг ЗААВАЛ татгалзах ёстой, алдаа: %v", err)
	}
}

func TestParseFilters_ValueIsAlwaysBound(t *testing.T) {
	got := mustParse(t, `{"invoice_number":{"op":"contains","v":"'; DROP TABLE x; --"}}`)
	if len(got) != 1 {
		t.Fatalf("1 нөхцөл хүлээсэн, got %d", len(got))
	}
	if strings.Contains(got[0].sql, "DROP") {
		t.Fatalf("хэрэглэгчийн утга SQL-д ШУУД ОРСОН: %s", got[0].sql)
	}
	if !strings.HasPrefix(got[0].sql, "i.invoice_number ILIKE ?") {
		t.Fatalf("SQL хэлбэр буруу: %s", got[0].sql)
	}
	if got[0].args[0] != `%'; DROP TABLE x; --%` {
		t.Fatalf("args буруу: %#v", got[0].args)
	}
}

func TestParseFilters_NoSpecMeansRejected(t *testing.T) {
	_, err := Spec{}.parseFilters(`{"anything":{"op":"eq","v":"x"}}`)
	if !IsFilterError(err) {
		t.Fatalf("Filters зарлаагүй үед 400 хүлээсэн, got %v", err)
	}
}

// ─── LIKE wildcard escape ───

func TestEscapeLike_WildcardsBecomeLiteral(t *testing.T) {
	got := mustParse(t, `{"party_name":{"op":"contains","v":"50%_х"}}`)
	want := `%50\%\_х%`
	if got[0].args[0] != want {
		t.Fatalf("escape буруу: got %q, want %q", got[0].args[0], want)
	}
	if !strings.Contains(got[0].sql, `ESCAPE '\'`) {
		t.Fatalf("ESCAPE заагаагүй: %s", got[0].sql)
	}
}

func TestEscapeLike_Backslash(t *testing.T) {
	if got := escapeLike(`a\b`); got != `a\\b` {
		t.Fatalf("backslash escape буруу: %q", got)
	}
}

// ─── kind × op матриц ───

func TestParseFilters_KindOpMatrix(t *testing.T) {
	bad := []string{
		`{"total_amount":{"op":"contains","v":"5"}}`,       // тоонд contains
		`{"invoice_number":{"op":"between","v":1,"v2":2}}`, // текстэд between
		`{"status":{"op":"gt","v":"x"}}`,                   // enum-д gt
		`{"account_id":{"op":"contains","v":"x"}}`,         // uuid-д contains
		`{"invoice_date":{"op":"contains","v":"x"}}`,       // огноонд contains
	}
	for _, raw := range bad {
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Errorf("тохирохгүй оператор татгалзах ёстой: %s (err=%v)", raw, err)
		}
	}
}

func TestParseFilters_TextOperators(t *testing.T) {
	cases := map[string]string{
		`{"party_name":{"op":"startsWith","v":"Гоо"}}`: `Гоо%`,
		`{"party_name":{"op":"endsWith","v":"ХХК"}}`:   `%ХХК`,
		`{"party_name":{"op":"eq","v":"Гоовин"}}`:      `Гоовин`,
	}
	for raw, wantArg := range cases {
		got := mustParse(t, raw)
		if got[0].args[0] != wantArg {
			t.Errorf("%s → args %q, want %q", raw, got[0].args[0], wantArg)
		}
	}
	// notContains / neq нь NULL мөрийг ч оруулах ёстой.
	got := mustParse(t, `{"party_name":{"op":"notContains","v":"x"}}`)
	if !strings.Contains(got[0].sql, "IS NULL") || !strings.Contains(got[0].sql, "NOT ILIKE") {
		t.Fatalf("notContains SQL буруу: %s", got[0].sql)
	}
}

// ─── Огнооны инклюзив хязгаар ───

func TestParseFilters_DateInclusiveUpperBound(t *testing.T) {
	got := mustParse(t, `{"invoice_date":{"op":"lte","v":"2026-07-31"}}`)
	if !strings.Contains(got[0].sql, "< ?::date + INTERVAL '1 day'") {
		t.Fatalf("lte нь тухайн өдрийг БҮТНЭЭР багтаах ёстой: %s", got[0].sql)
	}
}

func TestParseFilters_DateEqIsFullDayRange(t *testing.T) {
	got := mustParse(t, `{"invoice_date":{"op":"eq","v":"2026-07-31"}}`)
	if !strings.Contains(got[0].sql, ">= ?::date") || !strings.Contains(got[0].sql, "< ?::date + INTERVAL '1 day'") {
		t.Fatalf("eq нь өдрийн муж байх ёстой: %s", got[0].sql)
	}
	if len(got[0].args) != 2 || got[0].args[0] != "2026-07-31" || got[0].args[1] != "2026-07-31" {
		t.Fatalf("args буруу: %#v", got[0].args)
	}
}

func TestParseFilters_DateTrimsTimestamp(t *testing.T) {
	got := mustParse(t, `{"invoice_date":{"op":"gte","v":"2026-07-31T00:00:00Z"}}`)
	if got[0].args[0] != "2026-07-31" {
		t.Fatalf("ISO timestamp-ыг өдөр болгож тайрах ёстой: %#v", got[0].args)
	}
}

func TestParseFilters_BadDate(t *testing.T) {
	for _, raw := range []string{
		`{"invoice_date":{"op":"gte","v":"2026-13-xx"}}`,
		`{"invoice_date":{"op":"gte","v":"31/07/2026"}}`,
	} {
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Errorf("буруу огноо татгалзах ёстой: %s", raw)
		}
	}
}

// ─── uuid / enum ───

func TestParseFilters_BadUUID(t *testing.T) {
	raw := `{"account_id":{"op":"in","vs":["not-a-uuid"]}}`
	if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
		t.Fatalf("буруу uuid татгалзах ёстой")
	}
}

func TestParseFilters_UUIDIn(t *testing.T) {
	raw := `{"account_id":{"op":"in","vs":["3f1a2b4c-5d6e-4f70-8a91-b2c3d4e5f607"]}}`
	got := mustParse(t, raw)
	if got[0].sql != "cb.bank_account_id IN (?)" {
		t.Fatalf("SQL буруу: %s", got[0].sql)
	}
}

func TestParseFilters_EnumIn(t *testing.T) {
	got := mustParse(t, `{"status":{"op":"in","vs":["posted","draft"]}}`)
	if got[0].sql != "i.status IN (?)" {
		t.Fatalf("SQL буруу: %s", got[0].sql)
	}
	vs, ok := got[0].args[0].([]string)
	if !ok || len(vs) != 2 {
		t.Fatalf("args буруу: %#v", got[0].args)
	}
}

// ─── Хоосон утга = шүүлтгүй ───

func TestParseFilters_EmptyValueIsNoOp(t *testing.T) {
	for _, raw := range []string{
		`{"invoice_number":{"op":"contains","v":""}}`,
		`{"total_amount":{"op":"gt","v":null}}`,
		`{"status":{"op":"in","vs":[]}}`,
	} {
		got, err := testSpec().parseFilters(raw)
		if err != nil {
			t.Errorf("%s → алдаа гарах ёсгүй: %v", raw, err)
		}
		if len(got) != 0 {
			t.Errorf("%s → нөхцөл үүсэх ёсгүй, got %d", raw, len(got))
		}
	}
}

func TestParseFilters_EmptyRawIsNoOp(t *testing.T) {
	for _, raw := range []string{"", "  ", "{}"} {
		got, err := testSpec().parseFilters(raw)
		if err != nil || got != nil {
			t.Errorf("%q → хоосон байх ёстой (got %v, %v)", raw, got, err)
		}
	}
}

// ─── blank / notBlank ───

func TestParseFilters_BlankSemantics(t *testing.T) {
	got := mustParse(t, `{"party_name":{"op":"blank"}}`)
	if !strings.Contains(got[0].sql, "IS NULL") || !strings.Contains(got[0].sql, "= ''") {
		t.Fatalf("текстэд blank нь NULL БА хоосон мөрийг хамрах ёстой: %s", got[0].sql)
	}
	got = mustParse(t, `{"total_amount":{"op":"blank"}}`)
	if got[0].sql != "i.total_amount IS NULL" {
		t.Fatalf("тоонд blank = IS NULL: %s", got[0].sql)
	}
}

// ─── Композит нөхцөл ───

func TestParseFilters_CompositeOr(t *testing.T) {
	raw := `{"party_name":{"j":"or","c":[{"op":"contains","v":"а"},{"op":"contains","v":"б"}]}}`
	got := mustParse(t, raw)
	if !strings.HasPrefix(got[0].sql, "(") || !strings.Contains(got[0].sql, " OR ") {
		t.Fatalf("OR бүлэг үүсээгүй: %s", got[0].sql)
	}
	if len(got[0].args) != 2 {
		t.Fatalf("args 2 байх ёстой: %#v", got[0].args)
	}
}

func TestParseFilters_CompositeDefaultsToAnd(t *testing.T) {
	raw := `{"total_amount":{"c":[{"op":"gte","v":10},{"op":"lte","v":20}]}}`
	got := mustParse(t, raw)
	if !strings.Contains(got[0].sql, " AND ") {
		t.Fatalf("анхдагч нь AND байх ёстой: %s", got[0].sql)
	}
}

func TestParseFilters_NestedCompositeRejected(t *testing.T) {
	raw := `{"party_name":{"c":[{"c":[{"op":"eq","v":"x"}]}]}}`
	if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
		t.Fatalf("давхарласан нөхцөл татгалзах ёстой")
	}
}

// ─── Хязгаарууд ───

func TestParseFilters_Limits(t *testing.T) {
	t.Run("хэт урт payload", func(t *testing.T) {
		raw := `{"party_name":{"op":"contains","v":"` + strings.Repeat("a", maxFilterBytes) + `"}}`
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Fatal("8KB-аас урт payload татгалзах ёстой")
		}
	})

	t.Run("хэт олон багана", func(t *testing.T) {
		parts := make([]string, 0, maxFilterCols+1)
		spec := Spec{Filters: map[string]FilterCol{}}
		for i := 0; i <= maxFilterCols; i++ {
			key := "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			spec.Filters[key] = Text("t." + key)
			parts = append(parts, `"`+key+`":{"op":"eq","v":"x"}`)
		}
		raw := "{" + strings.Join(parts, ",") + "}"
		if _, err := spec.parseFilters(raw); !IsFilterError(err) {
			t.Fatalf("%d-аас олон багана татгалзах ёстой", maxFilterCols)
		}
	})

	t.Run("хэт олон нөхцөл", func(t *testing.T) {
		raw := `{"party_name":{"c":[{"op":"eq","v":"a"},{"op":"eq","v":"b"},{"op":"eq","v":"c"}]}}`
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Fatal("2-оос олон нөхцөл татгалзах ёстой")
		}
	})

	t.Run("хэт олон in утга", func(t *testing.T) {
		vals := make([]string, maxFilterInVals+1)
		for i := range vals {
			vals[i] = `"v"`
		}
		raw := `{"status":{"op":"in","vs":[` + strings.Join(vals, ",") + `]}}`
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Fatalf("%d-аас олон утга татгалзах ёстой", maxFilterInVals)
		}
	})

	t.Run("хэт урт утга", func(t *testing.T) {
		raw := `{"party_name":{"op":"contains","v":"` + strings.Repeat("б", maxFilterValLen+1) + `"}}`
		if _, err := testSpec().parseFilters(raw); !IsFilterError(err) {
			t.Fatal("хэт урт утга татгалзах ёстой")
		}
	})
}

// ─── Тогтвортой дараалал (SQL давтагдах) ───

func TestParseFilters_StableOrder(t *testing.T) {
	raw := `{"status":{"op":"eq","v":"posted"},"party_name":{"op":"contains","v":"а"},"total_amount":{"op":"gt","v":1}}`
	first := mustParse(t, raw)
	for i := 0; i < 20; i++ {
		got := mustParse(t, raw)
		for j := range got {
			if got[j].sql != first[j].sql {
				t.Fatalf("дараалал тогтворгүй: %s vs %s", got[j].sql, first[j].sql)
			}
		}
	}
	// Цагаан толгойн дараалал: party_name, status, total_amount
	if !strings.Contains(first[0].sql, "p.name") ||
		!strings.Contains(first[1].sql, "i.status") ||
		!strings.Contains(first[2].sql, "i.total_amount") {
		t.Fatalf("эрэмбэ буруу: %v", []string{first[0].sql, first[1].sql, first[2].sql})
	}
}

// ─── filterable ───

func TestFilterable_ReportsKinds(t *testing.T) {
	f := testSpec().filterable()
	if f["invoice_number"] != KindText || f["total_amount"] != KindNumber ||
		f["invoice_date"] != KindDate || f["status"] != KindEnum ||
		f["account_id"] != KindAccount {
		t.Fatalf("filterable буруу: %#v", f)
	}
	if len(Spec{}.filterable()) != 0 {
		t.Fatal("Filters зарлаагүй бол filterable хоосон байх ёстой")
	}
}

// ─── Тусламжийн функцууд ───

func TestIsUUID(t *testing.T) {
	ok := []string{"3f1a2b4c-5d6e-4f70-8a91-b2c3d4e5f607", "3F1A2B4C-5D6E-4F70-8A91-B2C3D4E5F607"}
	bad := []string{"", "3f1a2b4c5d6e4f708a91b2c3d4e5f607", "3f1a2b4c-5d6e-4f70-8a91-b2c3d4e5f60", "zzzzzzzz-5d6e-4f70-8a91-b2c3d4e5f607"}
	for _, s := range ok {
		if !isUUID(s) {
			t.Errorf("uuid байх ёстой: %s", s)
		}
	}
	for _, s := range bad {
		if isUUID(s) {
			t.Errorf("uuid БИШ байх ёстой: %s", s)
		}
	}
}

func TestNum_AcceptsStringAndNumber(t *testing.T) {
	got := mustParse(t, `{"total_amount":{"op":"gte","v":"1200.50"}}`)
	if got[0].args[0].(float64) != 1200.50 {
		t.Fatalf("мөр хэлбэрийн тоо задрах ёстой: %#v", got[0].args)
	}
	got = mustParse(t, `{"total_amount":{"op":"gte","v":1200.50}}`)
	if got[0].args[0].(float64) != 1200.50 {
		t.Fatalf("тоо задрах ёстой: %#v", got[0].args)
	}
	if _, err := testSpec().parseFilters(`{"total_amount":{"op":"gte","v":"тоо биш"}}`); !IsFilterError(err) {
		t.Fatal("тоо биш утга татгалзах ёстой")
	}
}
