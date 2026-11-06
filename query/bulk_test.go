package query

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

const (
	idA = "11111111-1111-1111-1111-111111111111"
	idB = "22222222-2222-2222-2222-222222222222"
)

func roomBulk() BulkSpec {
	return BulkSpec{
		Table:       "organization.conference_room",
		AllowDelete: true,
		Fields: map[string]BulkField{
			"quantity_fit_number": {Col: "quantity_fit_number", Kind: KindNumber, Nullable: true},
			"room_name":           {Col: "room_name", Kind: KindText},
			"status":              {Col: "status", Kind: KindEnum, Enum: []string{"open", "closed"}},
			"active":              {Col: "is_active", Kind: KindBool},
			"opened":              {Col: "opened_on", Kind: KindDate},
			"owner_id":            {Col: "owner_id", Kind: KindUUID, Nullable: true},
		},
	}
}

func bulkSQL(t *testing.T, body string, spec BulkSpec, org string) (captured, int) {
	t.Helper()
	req, err := ParseBulk([]byte(body), spec)
	if err != nil {
		t.Fatalf("ParseBulk(%s): %v", body, err)
	}
	db, log := captureDB(t)
	if _, err := ExecBulk(db.Session(&gorm.Session{DryRun: true, NewDB: true}), org, req, spec); err != nil {
		t.Fatalf("ExecBulk: %v", err)
	}
	all := log.all()
	if len(all) != 1 {
		t.Fatalf("1 statement хүлээсэн, got %d: %#v", len(all), all)
	}
	return all[0], len(req.IDs)
}

func TestBulk_DeleteSQL(t *testing.T) {
	c, n := bulkSQL(t, `{"action":"delete","ids":["`+idA+`","`+strings.ToUpper(idB)+`","`+idA+`"]}`, roomBulk(), "org-A")
	if n != 2 {
		t.Fatalf("давхардсан id хасагдах ёстой, got %d", n)
	}
	if !strings.HasPrefix(c.sql, "DELETE FROM organization.conference_room WHERE org_id = $1 AND id IN ($2,$3)") {
		t.Fatalf("DELETE SQL буруу: %s", c.sql)
	}
	if c.vars[0] != "org-A" || c.vars[1] != idA || c.vars[2] != idB {
		t.Fatalf("args буруу: %#v", c.vars)
	}
	if strings.Contains(c.sql, idA) {
		t.Fatalf("id SQL-д шууд орсон: %s", c.sql)
	}
}

func TestBulk_SoftDeleteSQL(t *testing.T) {
	spec := roomBulk()
	spec.SoftDelete = true
	c, _ := bulkSQL(t, `{"action":"delete","ids":["`+idA+`"]}`, spec, "org-A")
	if !strings.HasPrefix(c.sql, "UPDATE organization.conference_room SET deleted_at=now() WHERE") ||
		!strings.Contains(c.sql, "org_id = $1") || !strings.Contains(c.sql, "deleted_at IS NULL") {
		t.Fatalf("soft delete SQL буруу: %s", c.sql)
	}
	if strings.Contains(c.sql, "DELETE") {
		t.Fatalf("soft delete нь DELETE хийх ёсгүй: %s", c.sql)
	}
}

func TestBulk_UpdateOnlyWhitelistedColumns(t *testing.T) {
	c, _ := bulkSQL(t, `{"action":"update","ids":["`+idA+`"],"set":{"quantity_fit_number":"12","status":"open","active":true}}`, roomBulk(), "org-A")
	if !strings.HasPrefix(c.sql, "UPDATE organization.conference_room SET ") {
		t.Fatalf("UPDATE SQL буруу: %s", c.sql)
	}
	set := c.sql[:strings.Index(c.sql, " WHERE ")]
	for _, col := range []string{"quantity_fit_number=", "status=", "is_active="} {
		if !strings.Contains(set, col) {
			t.Errorf("SET-д %s алга: %s", col, set)
		}
	}
	for _, col := range []string{"org_id", "room_name", " id="} {
		if strings.Contains(set, col) {
			t.Errorf("SET-д %s орох ёсгүй: %s", col, set)
		}
	}
	if !strings.Contains(c.sql, "WHERE org_id = $") || !varsContain(c.vars, "org-A") {
		t.Fatalf("org предикат алга: %s %#v", c.sql, c.vars)
	}
	foundNum := false
	for _, v := range c.vars {
		if v == int64(12) {
			foundNum = true
		}
	}
	if !foundNum {
		t.Errorf("тоон мөр \"12\" → int64(12) bind байх ёстой: %#v", c.vars)
	}
}

func TestBulk_IntIDs(t *testing.T) {
	spec := roomBulk()
	spec.IntIDs = true
	c, _ := bulkSQL(t, `{"action":"delete","ids":[5,"7"]}`, spec, "org-A")
	if c.vars[1] != int64(5) || c.vars[2] != int64(7) {
		t.Fatalf("int id bind буруу: %#v", c.vars)
	}
}

func TestBulk_NullOnlyWhenNullable(t *testing.T) {
	req, err := ParseBulk([]byte(`{"action":"update","ids":["`+idA+`"],"set":{"quantity_fit_number":null}}`), roomBulk())
	if err != nil || req.Set["quantity_fit_number"] != nil {
		t.Fatalf("nullable талбар null авах ёстой: %v %#v", err, req.Set)
	}
}

func TestParseBulk_Rejections(t *testing.T) {
	many := make([]string, 201)
	for i := range many {
		many[i] = `"` + strings.Replace(idA, "1111-1111-1111-111111111111", "1111-1111-1111-"+padHex(i), 1) + `"`
	}
	long := strings.Repeat("я", maxBulkTextLen+1)
	noDel := roomBulk()
	noDel.AllowDelete = false
	ids := `"ids":["` + idA + `"]`

	cases := map[string]struct {
		body string
		spec BulkSpec
	}{
		"unknown action":      {`{"action":"drop",` + ids + `}`, roomBulk()},
		"delete not allowed":  {`{"action":"delete",` + ids + `}`, noDel},
		"empty ids":           {`{"action":"delete","ids":[]}`, roomBulk()},
		"no ids":              {`{"action":"delete"}`, roomBulk()},
		"too many ids":        {`{"action":"delete","ids":[` + strings.Join(many, ",") + `]}`, roomBulk()},
		"malformed uuid":      {`{"action":"delete","ids":["1 OR 1=1"]}`, roomBulk()},
		"numeric id as uuid":  {`{"action":"delete","ids":[5]}`, roomBulk()},
		"update empty set":    {`{"action":"update",` + ids + `,"set":{}}`, roomBulk()},
		"set not whitelisted": {`{"action":"update",` + ids + `,"set":{"org_id":"` + idB + `"}}`, roomBulk()},
		"set id":              {`{"action":"update",` + ids + `,"set":{"id":"` + idB + `"}}`, roomBulk()},
		"number wrong kind":   {`{"action":"update",` + ids + `,"set":{"quantity_fit_number":"abc"}}`, roomBulk()},
		"number bool":         {`{"action":"update",` + ids + `,"set":{"quantity_fit_number":true}}`, roomBulk()},
		"number inf":          {`{"action":"update",` + ids + `,"set":{"quantity_fit_number":1e400}}`, roomBulk()},
		"bool wrong":          {`{"action":"update",` + ids + `,"set":{"active":"yes"}}`, roomBulk()},
		"date wrong":          {`{"action":"update",` + ids + `,"set":{"opened":"2026-13-40"}}`, roomBulk()},
		"uuid wrong":          {`{"action":"update",` + ids + `,"set":{"owner_id":"abc"}}`, roomBulk()},
		"enum not in list":    {`{"action":"update",` + ids + `,"set":{"status":"deleted"}}`, roomBulk()},
		"text too long":       {`{"action":"update",` + ids + `,"set":{"room_name":"` + long + `"}}`, roomBulk()},
		"text not string":     {`{"action":"update",` + ids + `,"set":{"room_name":5}}`, roomBulk()},
		"null not nullable":   {`{"action":"update",` + ids + `,"set":{"room_name":null}}`, roomBulk()},
		"bad json":            {`{"action":`, roomBulk()},
		"delete with set":     {`{"action":"delete",` + ids + `,"set":{"room_name":"x"}}`, roomBulk()},
	}
	for name, tc := range cases {
		if _, err := ParseBulk([]byte(tc.body), tc.spec); !IsFilterError(err) {
			t.Errorf("%s: FilterError хүлээсэн, got %v", name, err)
		}
	}
}

func padHex(i int) string {
	const hex = "0123456789abcdef"
	out := []byte("111111111111")
	for p := len(out) - 1; i > 0; p-- {
		out[p] = hex[i%16]
		i /= 16
	}
	return string(out)
}

func TestBulk_MaxIDsCeiling(t *testing.T) {
	if (BulkSpec{MaxIDs: 5000}).maxIDs() != 1000 || (BulkSpec{}).maxIDs() != 200 || (BulkSpec{MaxIDs: 3}).maxIDs() != 3 {
		t.Fatal("MaxIDs анхдагч/дээд хязгаар буруу")
	}
}

// id / org_id-г whitelist-д бүртгэсэн ч spec хүлээн авахгүй (panic).
func TestBulkSpec_ForbiddenColumnsPanic(t *testing.T) {
	bad := []BulkSpec{
		{Table: "t", Fields: map[string]BulkField{"x": {Col: "org_id", Kind: KindUUID}}},
		{Table: "t", Fields: map[string]BulkField{"x": {Col: "id", Kind: KindUUID}}},
		{Table: "t", IDCol: "room_id", Fields: map[string]BulkField{"x": {Col: "room_id", Kind: KindUUID}}},
		{Table: "t", OrgCol: "company_id", Fields: map[string]BulkField{"x": {Col: "company_id", Kind: KindUUID}}},
		{Table: "t", Fields: map[string]BulkField{"x": {Col: "deleted_at", Kind: KindDate}}},
		{Table: "t", Fields: map[string]BulkField{"x": {Col: "a; DROP", Kind: KindText}}},
		{Table: "t; DROP", Fields: map[string]BulkField{}},
		{Table: "t", Fields: map[string]BulkField{"x": {Col: "a", Kind: KindEnum}}},
	}
	for i, spec := range bad {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("#%d panic хүлээсэн", i)
				}
			}()
			spec.Validate()
		}()
	}
}

func TestExecBulk_EmptyOrgRejected(t *testing.T) {
	db, log := captureDB(t)
	req := BulkRequest{Action: "delete", IDs: []string{idA}}
	if _, err := ExecBulk(db, "", req, roomBulk()); err == nil {
		t.Fatal("org хоосон үед алдаа хүлээсэн")
	}
	if len(log.all()) != 0 {
		t.Fatal("org хоосон үед SQL бүтээгдэх ёсгүй")
	}
}

func TestBulkQuery_Scope(t *testing.T) {
	db, _ := captureDB(t)
	var rows []map[string]interface{}
	stmt := BulkQuery(db.Session(&gorm.Session{DryRun: true, NewDB: true}), "org-A",
		BulkRequest{Action: "delete", IDs: []string{idA, idB}}, roomBulk()).Find(&rows).Statement
	if !strings.Contains(stmt.SQL.String(), "WHERE org_id = $1 AND id IN ($2,$3)") {
		t.Fatalf("scope буруу: %s", stmt.SQL.String())
	}
}

// Bulk-ийн тоон хязгаар — create/update-ийн шалгалттай ижил (review: -5 / 12.5 / 1e12 → 400).
func TestParseBulk_NumberBounds(t *testing.T) {
	spec := BulkSpec{Table: "organization.conference_room", Fields: map[string]BulkField{
		"qty": {Col: "quantity_fit_number", Kind: KindNumber, Nullable: true, Integer: true, Min: Bound(0), Max: Bound(100000)},
	}}
	id := "11111111-1111-1111-1111-111111111111"
	for _, v := range []string{"-5", "12.5", "1e12", "100001"} {
		_, err := ParseBulk([]byte(`{"action":"update","ids":["`+id+`"],"set":{"qty":`+v+`}}`), spec)
		if !IsFilterError(err) {
			t.Fatalf("qty=%s → FilterError байх ёстой, got %v", v, err)
		}
	}
	for _, v := range []string{"0", "100000", "null", `"42"`} {
		if _, err := ParseBulk([]byte(`{"action":"update","ids":["`+id+`"],"set":{"qty":`+v+`}}`), spec); err != nil {
			t.Fatalf("qty=%s зөвшөөрөгдөх ёстой: %v", v, err)
		}
	}
}
