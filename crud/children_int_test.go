package crud

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// ticketFx — бүхэл тоон (bigserial / int4 serial) id-тай child хүснэгтүүд (legacy subform загвар).
func ticketFx(t *testing.T, f *fx) {
	t.Helper()
	s := f.schema
	f.exec(`CREATE TABLE ` + s + `.ticket (id uuid PRIMARY KEY, org_id uuid NOT NULL, title text NOT NULL,
		version int NOT NULL DEFAULT 1, created_at timestamptz, updated_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.ticket_note (id bigserial PRIMARY KEY, org_id uuid NOT NULL,
		ticket_id uuid NOT NULL REFERENCES ` + s + `.ticket(id), body text NOT NULL, pos int,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.ticket_tag (tag_id serial PRIMARY KEY, org_id uuid NOT NULL,
		ticket_id uuid NOT NULL REFERENCES ` + s + `.ticket(id), label text NOT NULL,
		created_at timestamptz, updated_at timestamptz)`)
}

func (f *fx) ticketResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.ticket", Label: "Тасалбар", Endpoint: "/tickets", Table: f.tbl("ticket"),
		Fields: []Field{{Name: "title", Label: "Гарчиг", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 50}}},
		Children: []Child{
			{Name: "notes", Label: "Тэмдэглэл", Table: f.tbl("ticket_note"), FK: "ticket_id", ID: IDSpec{Kind: IDInt},
				MaxRows: 3, SoftDelete: true,
				Fields: []Field{
					{Name: "body", Label: "Агуулга", Kind: validate.Text, Rules: validate.Rules{Required: true}},
					{Name: "pos", Label: "Дараалал", Kind: validate.Int, Rules: validate.Rules{Nullable: true}},
				}},
			{Name: "tags", Label: "Шошго", Table: f.tbl("ticket_tag"), FK: "ticket_id", ID: IDSpec{Column: "tag_id", Kind: IDInt},
				Fields: []Field{{Name: "label", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}}},
		},
		Version: "version",
		List:    queryDefault(),
		Can:     canByRole, DB: func() *gorm.DB { return f.db },
	}
}

func note(body string) map[string]any { return map[string]any{"body": body} }
func tag(label string) map[string]any { return map[string]any{"label": label} }

func (f *fx) ids(table, col, ticket string) []int64 {
	f.t.Helper()
	var out []int64
	if err := f.db.Raw(`SELECT `+col+` FROM `+f.tbl(table)+` WHERE ticket_id = ? ORDER BY `+col, ticket).Scan(&out).Error; err != nil {
		f.t.Fatal(err)
	}
	return out
}

func idNum(t *testing.T, row map[string]any, key string) float64 {
	t.Helper()
	n, ok := row[key].(float64)
	if !ok {
		t.Fatalf("%s JSON тоо байх ёстой: %#v", key, row[key])
	}
	return n
}

func TestChildIntIDLifecycle(t *testing.T) {
	f := newFx(t)
	ticketFx(t, f)
	app := mountOn(t, "/tickets", f.ticketResource())

	// meta: child id төрөл
	m := f.ticketResource().Meta()
	if m.Children[0].ID.Kind != IDInt || m.Children[0].ID.Column != "id" || m.Children[1].ID.Kind != IDInt || m.Children[1].ID.Column != "tag_id" {
		t.Fatalf("meta child id: %+v", m.Children)
	}

	// create: id-г DB өгнө (payload-ийн id үл тооно); хариуд JSON тоо; id-аар эрэмблэгдэнэ
	n1 := note("нэг")
	n1["id"] = 777
	c := do(t, app, "POST", "/tickets", f.orgA, 1, map[string]any{
		"title": "T1", "notes": []any{n1, note("хоёр")}, "tags": []any{tag("a"), tag("b")},
	})
	mustStatus(t, c, 201)
	tid := c.str("id")
	notes, tags := c.list("notes"), c.list("tags")
	if len(notes) != 2 || len(tags) != 2 {
		t.Fatalf("%s", c.Raw)
	}
	if idNum(t, notes[0], "id") >= idNum(t, notes[1], "id") || idNum(t, notes[0], "id") == 777 {
		t.Errorf("notes id: %s", c.Raw)
	}
	if notes[0]["body"] != "нэг" || tags[0]["label"] != "a" || tags[1]["label"] != "b" || idNum(t, tags[0], "tag_id") >= idNum(t, tags[1], "tag_id") {
		t.Errorf("%s", c.Raw)
	}
	if notes[0]["org_id"] != f.orgA || notes[0]["ticket_id"] != tid {
		t.Errorf("org/fk сервер тавина: %v", notes[0])
	}
	if _, has := tags[0]["id"]; has {
		t.Errorf("tags-д id түлхүүр байх ёсгүй: %v", tags[0])
	}
	nid1, nid2 := int64(idNum(t, notes[0], "id")), int64(idNum(t, notes[1], "id"))
	tg1, tg2 := int64(idNum(t, tags[0], "tag_id")), int64(idNum(t, tags[1], "tag_id"))

	g := do(t, app, "GET", "/tickets/"+tid, f.orgA, 1, nil)
	if len(g.list("notes")) != 2 || idNum(t, g.list("notes")[1], "id") != float64(nid2) {
		t.Errorf("GET: %s", g.Raw)
	}

	// replace: нэгийг засна (JSON тоо), нөгөөг цифртэй мөрөөр, нэгийг жагсаалтаас хасна, шинийг нэмнэ
	u := do(t, app, "PUT", "/tickets/"+tid, f.orgA, 1, map[string]any{
		"version": 1,
		"notes":   []any{map[string]any{"id": nid1, "body": "нэг*"}, map[string]any{"id": fmt.Sprint(nid2), "pos": 4}, note("гурав")},
		"tags":    []any{map[string]any{"tag_id": tg1, "label": "a*"}, tag("c")},
	})
	mustStatus(t, u, 200)
	un, ut := u.list("notes"), u.list("tags")
	if len(un) != 3 || un[0]["body"] != "нэг*" || un[1]["body"] != "хоёр" || un[1]["pos"] != float64(4) || un[2]["body"] != "гурав" {
		t.Fatalf("notes replace: %s", u.Raw)
	}
	if idNum(t, un[0], "id") != float64(nid1) || idNum(t, un[1], "id") != float64(nid2) {
		t.Errorf("id хадгалагдах ёстой: %s", u.Raw)
	}
	if len(ut) != 2 || ut[0]["label"] != "a*" || idNum(t, ut[0], "tag_id") != float64(tg1) || ut[1]["label"] != "c" {
		t.Fatalf("tags replace: %s", u.Raw)
	}
	// tg2 hard delete
	if got := f.ids("ticket_tag", "tag_id", tid); len(got) != 2 || got[0] != tg1 || got[1] == tg2 {
		t.Errorf("tags: %v (tg2=%d)", got, tg2)
	}

	// жагсаалтаас хасвал soft delete; [] бүгдийг устгана; null өөрчлөхгүй
	u = do(t, app, "PUT", "/tickets/"+tid, f.orgA, 1, map[string]any{"version": 2, "notes": []any{map[string]any{"id": nid1}}, "tags": nil})
	mustStatus(t, u, 200)
	if len(u.list("notes")) != 1 || len(u.list("tags")) != 2 {
		t.Fatalf("%s", u.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("ticket_note")+` WHERE ticket_id = ? AND deleted_at IS NOT NULL`, tid) != 2 {
		t.Error("notes soft delete")
	}
	u = do(t, app, "PUT", "/tickets/"+tid, f.orgA, 1, map[string]any{"version": 3, "tags": []any{}})
	mustStatus(t, u, 200)
	if len(u.list("tags")) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("ticket_tag")) != 0 {
		t.Errorf("tags []: %s", u.Raw)
	}

	// эцгийг устгахад (hard) int child-ууд мөн устна
	mustStatus(t, do(t, app, "DELETE", "/tickets/"+tid, f.orgA, 1, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("ticket_note")) != 0 {
		t.Error("parent delete: child үлдсэн")
	}
}

func TestChildIntIDValidation(t *testing.T) {
	f := newFx(t)
	ticketFx(t, f)
	app := mountOn(t, "/tickets", f.ticketResource())
	mine := do(t, app, "POST", "/tickets", f.orgA, 1, map[string]any{"title": "M", "notes": []any{note("m")}, "tags": []any{tag("m")}})
	other := do(t, app, "POST", "/tickets", f.orgA, 1, map[string]any{"title": "O", "notes": []any{note("o")}, "tags": []any{tag("o")}})
	bOrg := do(t, app, "POST", "/tickets", f.orgB, 1, map[string]any{"title": "B", "notes": []any{note("b")}, "tags": []any{tag("b")}})
	mustStatus(t, mine, 201)
	mustStatus(t, other, 201)
	mustStatus(t, bOrg, 201)
	mid := mine.str("id")
	ownNote := int64(idNum(t, mine.list("notes")[0], "id"))
	otherNote := int64(idNum(t, other.list("notes")[0], "id"))
	bNote := int64(idNum(t, bOrg.list("notes")[0], "id"))
	otherTag := int64(idNum(t, other.list("tags")[0], "tag_id"))
	bTag := int64(idNum(t, bOrg.list("tags")[0], "tag_id"))

	put := func(body string) resp {
		t.Helper()
		return do(t, app, "PUT", "/tickets/"+mid, f.orgA, 1, `{"version":1,`+body+`}`)
	}

	// foreign / байхгүй / муу id → 422 (500 биш), өөрчлөлт үлдэхгүй
	bad := map[string]string{
		"өөр эцэг":       fmt.Sprint(otherNote),
		"өөр org":        fmt.Sprint(bNote),
		"байхгүй":        "987654321",
		"int64 дээд":     "9223372036854775807",
		"int64 халилт":   "9223372036854775808",
		"маш урт":        "99999999999999999999999",
		"0":              "0",
		"сөрөг":          "-3",
		"бутархай":       "1.5",
		"экспонент":      "1e3",
		"цифр биш мөр":   `"12a"`,
		"хоосон биш үг":  `"abc"`,
		"сөрөг мөр":      `"-1"`,
		"зай":            `" 5"`,
		"bool":           "true",
		"массив":         "[1]",
		"объект":         "{}",
		"UUID хэлбэртэй": `"` + newUUID() + `"`,
	}
	for name, id := range bad {
		r := put(`"notes":[{"id":` + id + `,"body":"HACK"}]`)
		mustStatus(t, r, 422)
		if r.errs()["notes.0.id"] == nil {
			t.Errorf("%s: %v", name, r.errs())
		}
	}
	// int4 багана (tags): int4-өөс их id → 422 (pgx encode алдаа/500 биш)
	for name, id := range map[string]string{"foreign": fmt.Sprint(otherTag), "b org": fmt.Sprint(bTag), "int4 дээд": "2147483648", "их": "9223372036854775807"} {
		r := put(`"tags":[{"tag_id":` + id + `,"label":"HACK"}]`)
		mustStatus(t, r, 422)
		if r.errs()["tags.0.tag_id"] == nil {
			t.Errorf("tags %s: %v", name, r.errs())
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("ticket_note")+` WHERE body = 'HACK'`) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("ticket_tag")+` WHERE label = 'HACK'`) != 0 {
		t.Fatal("foreign/буруу id-аар бичигдсэн")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("ticket_note")+` WHERE body = 'o' AND deleted_at IS NULL`) != 1 {
		t.Fatal("өөр эцгийн мөр хөндөгдсөн")
	}

	// 422-ын дараа эцэг нь хөндөгдөөгүй (version өөрчлөгдөөгүй)
	if f.count(`SELECT version FROM `+f.tbl("ticket")+` WHERE id = ?`, mid) != 1 {
		t.Error("version өөрчлөгдсөн")
	}

	// давхардсан id: 7 ба "7" ижил
	r := put(fmt.Sprintf(`"notes":[{"id":%d,"body":"x"},{"id":"%d","body":"y"}]`, ownNote, ownNote))
	mustStatus(t, r, 422)
	if r.errs()["notes.1.id"] == nil {
		t.Errorf("давхардал: %v", r.errs())
	}
	// тэг дүүргэсэн цифр ("0123") каноник болж таарна
	ok := put(fmt.Sprintf(`"notes":[{"id":"%019d","body":"z"}]`, ownNote))
	mustStatus(t, ok, 200)
	if l := ok.list("notes"); len(l) != 1 || l[0]["body"] != "z" {
		t.Errorf("%s", ok.Raw)
	}

	// MaxRows (notes = 3)
	r = put(`"notes":[{"body":"1"},{"body":"2"},{"body":"3"},{"body":"4"}]`)
	mustStatus(t, r, 422)
	if r.errs()["notes"] == nil || !strings.Contains(r.errs()["notes"][0], "3") {
		t.Errorf("%v", r.errs())
	}
	// create-д id үл тооно (хэтэрхий том ч гэсэн)
	c := do(t, app, "POST", "/tickets", f.orgA, 1, `{"title":"C","notes":[{"id":"zzz","body":"q"},{"id":99999999999999999999,"body":"w"}]}`)
	mustStatus(t, c, 201)
}

func TestParseChildID(t *testing.T) {
	type tc struct {
		raw  string
		kind IDKind
		id   string
		val  any
		ok   bool
	}
	u := "ABCDEF01-2345-6789-ABCD-EF0123456789"
	lu := strings.ToLower(u)
	for _, c := range []tc{
		{``, IDInt, "", nil, true},
		{`null`, IDInt, "", nil, true},
		{`""`, IDInt, "", nil, true},
		{`5`, IDInt, "5", int64(5), true},
		{`"5"`, IDInt, "5", int64(5), true},
		{`"007"`, IDInt, "7", int64(7), true},
		{`9223372036854775807`, IDInt, "9223372036854775807", int64(9223372036854775807), true},
		{`9223372036854775808`, IDInt, "", nil, false},
		{`0`, IDInt, "", nil, false},
		{`-1`, IDInt, "", nil, false},
		{`1.0`, IDInt, "", nil, false},
		{`"1 "`, IDInt, "", nil, false},
		{`true`, IDInt, "", nil, false},
		{`"` + u + `"`, IDInt, "", nil, false},
		{`"` + u + `"`, IDUUID, lu, lu, true},
		{`5`, IDUUID, "", nil, false},
		{`"5"`, IDUUID, "", nil, false},
		{`""`, IDUUID, "", nil, true},
		{`"\u00`, IDUUID, "", nil, false},
		{`"\u00`, IDInt, "", nil, false},
	} {
		errs := validate.Errors{}
		id, val, ok := parseChildID(json.RawMessage(c.raw), c.kind, errs, "f")
		if id != c.id || val != c.val || ok != c.ok || (!ok && errs["f"] == nil) {
			t.Errorf("%s %s: id=%q val=%v ok=%v errs=%v", c.kind, c.raw, id, val, ok, errs)
		}
	}
}

// Cache + audit: int child өөрчлөлт detail cache-ийг шинэчилж, audit-д id-тай (тоо) diff бичигдэнэ.
func TestChildIntIDCacheAndAudit(t *testing.T) {
	f := newCfx(t)
	ticketFx(t, f.fx)
	f.exec(audit.SchemaSQL)
	t.Cleanup(func() { purgeAuditOrgs(f.db, f.orgA, f.orgB) })
	r := f.ticketResource()
	r.Cache = f.spec()
	r.Audit = &AuditSpec{Enabled: true, Snapshot: true}
	var app *fiber.App = auditApp(t, "/tickets", r)

	c := do(t, app, "POST", "/tickets", f.orgA, 1, map[string]any{"title": "T", "notes": []any{note("a")}, "tags": []any{tag("x")}})
	mustStatus(t, c, 201)
	tid := c.str("id")
	nid := int64(idNum(t, c.list("notes")[0], "id"))

	d1 := get(t, app, "/tickets/"+tid, f.orgA, 1)
	d2 := get(t, app, "/tickets/"+tid, f.orgA, 1)
	if d1.cache() != "miss" || d2.cache() != "hit" || string(d1.Raw) != string(c.Raw) {
		t.Fatalf("cache: %q %q\n%s\n%s", d1.cache(), d2.cache(), d1.Raw, c.Raw)
	}

	u := do(t, app, "PUT", "/tickets/"+tid, f.orgA, 1, map[string]any{"version": 1, "notes": []any{map[string]any{"id": nid, "body": "b"}, note("c")}})
	mustStatus(t, u, 200)
	d3 := get(t, app, "/tickets/"+tid, f.orgA, 1)
	if d3.cache() != "miss" || string(d3.Raw) != string(u.Raw) || !strings.Contains(string(d3.Raw), `"body":"b"`) {
		t.Fatalf("PUT-ийн дараа cache: %q\n%s\n%s", d3.cache(), d3.Raw, u.Raw)
	}

	af := &afx{f.fx}
	es := af.entries(f.orgA, "test.ticket")
	if len(es) != 2 {
		t.Fatalf("audit: %d", len(es))
	}
	cs := es[1].changes(t)
	oldN, newN := cs["notes"].Old.([]any), cs["notes"].New.([]any)
	if len(cs) != 1 || len(oldN) != 1 || len(newN) != 2 {
		t.Fatalf("notes diff: %v", cs)
	}
	if oldN[0].(map[string]any)["id"] != jn(fmt.Sprint(nid)) || newN[0].(map[string]any)["id"] != jn(fmt.Sprint(nid)) || newN[0].(map[string]any)["body"] != "b" {
		t.Errorf("audit child id: %v %v", oldN, newN)
	}
	if _, has := cs["tags"]; has {
		t.Error("tags илгээгээгүй: diff-д орох ёсгүй")
	}
	// өөрчлөлтгүй replace → entry нэмэгдэхгүй
	same := d3.list("notes")
	rows := make([]any, 0, len(same))
	for _, n := range same {
		rows = append(rows, map[string]any{"id": n["id"], "body": n["body"], "pos": n["pos"]})
	}
	mustStatus(t, do(t, app, "PUT", "/tickets/"+tid, f.orgA, 1, map[string]any{"version": 2, "notes": rows}), 200)
	if n := len(af.entries(f.orgA, "test.ticket")); n != 2 {
		t.Errorf("өөрчлөлтгүй: entries=%d", n)
	}
}
