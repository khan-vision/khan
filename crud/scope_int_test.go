package crud

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/validate"
)

// IDInt child × ScopeShared / ScopeGlobal хослол. Child-ийн эзэмшигч эцгийг дагана (тусдаа org_id биш):
// тенант → JWT org; shared системийн эцэг → NULL; global → org_id багана алга.

func notesChild(f *sfx) Child {
	return Child{Name: "notes", Label: "Тэмдэглэл", Table: f.tbl("kind_note"), FK: "kind_id", ID: IDSpec{Kind: IDInt}, SoftDelete: true,
		Fields: []Field{{Name: "body", Label: "Агуулга", Kind: validate.Text, Rules: validate.Rules{Required: true}}}}
}

func (f *sfx) mountSharedInt(t *testing.T) *fiber.App {
	return f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Children = append(r.Children, notesChild(f))
		r.Bulk = nil
	})
}

// noteRow — kind_note шууд DB-д (org="" → NULL); id-г DB өгнө.
func (f *sfx) noteRow(org, kindID, body string) int64 {
	var o any
	if org != "" {
		o = org
	}
	var id int64
	if err := f.db.Raw(`INSERT INTO `+f.tbl("kind_note")+` (org_id, kind_id, body) VALUES (?,?,?) RETURNING id`, o, kindID, body).Scan(&id).Error; err != nil {
		f.t.Fatal(err)
	}
	return id
}

func noteIDs(t *testing.T, r resp) []int64 {
	t.Helper()
	var out []int64
	for _, n := range r.list("notes") {
		out = append(out, int64(idNum(t, n, "id")))
	}
	return out
}

func TestSharedIntChildrenSystemParent(t *testing.T) {
	f := newSfx(t)
	app := f.mountSharedInt(t)

	// sysadmin: системийн эцэг + int child; id-г DB өгнө, org_id NULL, хариунд JSON тоо
	c := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Систем", "notes": []map[string]any{{"body": "n1"}, {"body": "n2"}}})
	mustStatus(t, c, 201)
	sys := c.str("id")
	ns := noteIDs(t, c)
	if len(ns) != 2 || ns[0] <= 0 || ns[0] == ns[1] {
		t.Fatalf("create: %s", c.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ? AND org_id IS NULL`, sys) != 2 {
		t.Fatal("системийн int child org_id NULL биш")
	}
	// бүх tenant харна (JSON тоо)
	for _, org := range []string{f.orgA, f.orgB} {
		d := do(t, app, "GET", "/kinds/"+sys, org, roleRead, nil)
		mustStatus(t, d, 200)
		if got := noteIDs(t, d); len(got) != 2 {
			t.Fatalf("%s: %s", org, d.Raw)
		}
	}
	// tenant системийн int child-ыг засах/устгах/нэмэх бүгд 403, DB өөрчлөгдөхгүй
	for _, body := range []map[string]any{
		{"version": 1, "notes": []map[string]any{{"id": ns[0], "body": "hack"}, {"id": ns[1], "body": "n2"}}},
		{"version": 1, "notes": []map[string]any{}},
		{"version": 1, "notes": []map[string]any{{"id": ns[0], "body": "n1"}, {"id": ns[1], "body": "n2"}, {"body": "new"}}},
	} {
		r := do(t, app, "PUT", "/kinds/"+sys, f.orgA, roleAdmin, body)
		if r.Status != 403 || r.str("error") != "Системийн мөрийг өөрчлөх боломжгүй" {
			t.Fatalf("PUT: %d %s", r.Status, r.Raw)
		}
	}
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgA, roleAdmin, nil), 403)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ? AND org_id IS NULL AND deleted_at IS NULL AND body IN ('n1','n2')`, sys) != 2 {
		t.Fatal("системийн int child өөрчлөгдсөн")
	}

	// sysadmin: update + insert + delete (бүрэн жагсаалт); org_id NULL хэвээр, шинэ id DB-ээс
	up := do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"version": 1,
		"notes": []map[string]any{{"id": ns[0], "body": "n1-new"}, {"body": "n3"}}})
	mustStatus(t, up, 200)
	got := noteIDs(t, up)
	if len(got) != 2 || got[0] != ns[0] || got[1] == ns[1] {
		t.Fatalf("update: %s", up.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ? AND org_id IS NULL AND deleted_at IS NULL`, sys) != 2 ||
		f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE id = ? AND deleted_at IS NOT NULL`, ns[1]) != 1 ||
		f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE id = ? AND body = 'n1-new'`, ns[0]) != 1 {
		t.Fatal("sysadmin update буруу")
	}
	// id тэмдэглэгээ: мөр string цифр, int4-өөс том id → 422 (500 биш)
	ok := do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"version": 2,
		"notes": []map[string]any{{"id": fmt.Sprint(got[0]), "body": "s"}, {"id": got[1], "body": "t"}}})
	mustStatus(t, ok, 200)
	big := do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"version": 3,
		"notes": []map[string]any{{"id": 9999999999, "body": "x"}}})
	mustStatus(t, big, 422)
	if _, has := big.errs()["notes.0.id"]; !has {
		t.Fatalf("errs: %s", big.Raw)
	}
}

func TestSharedIntChildrenTenantParentOwnership(t *testing.T) {
	f := newSfx(t)
	app := f.mountSharedInt(t)

	sys := f.kindRow("", "Систем", "S")
	sysNote := f.noteRow("", sys, "системийн")
	b := f.kindRow(f.orgB, "Б", "B")
	bNote := f.noteRow(f.orgB, b, "б")

	// tenant А: өөрийн эцэг + int child; org_id = JWT org
	c := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А", "notes": []map[string]any{{"body": "a1"}, {"body": "a2"}}})
	mustStatus(t, c, 201)
	a := c.str("id")
	an := noteIDs(t, c)
	if len(an) != 2 || f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ? AND org_id = ?`, a, f.orgA) != 2 {
		t.Fatalf("create: %s", c.Raw)
	}
	// create дээр id үл тооно
	c2 := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А2", "notes": []map[string]any{{"id": sysNote, "body": "x"}}})
	mustStatus(t, c2, 201)
	if n := noteIDs(t, c2); len(n) != 1 || n[0] == sysNote {
		t.Fatalf("create id: %s", c2.Raw)
	}
	// tenant өөрийн эцгийн дор системийн / өөр org-ийн / өөр эцгийн child id-г заавал 422 (эзэмшил шалгагдана)
	other := f.noteRow(f.orgA, c2.str("id"), "өөр эцэг")
	for name, id := range map[string]int64{"системийн": sysNote, "өөр org": bNote, "өөр эцэг": other} {
		r := do(t, app, "PUT", "/kinds/"+a, f.orgA, roleAdmin, map[string]any{"version": 1,
			"notes": []map[string]any{{"id": an[0], "body": "a1"}, {"id": id, "body": "hack"}}})
		mustStatus(t, r, 422)
		if _, has := r.errs()["notes.1.id"]; !has {
			t.Fatalf("%s: %s", name, r.Raw)
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE body = 'hack'`) != 0 ||
		f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE id IN (?,?,?) AND deleted_at IS NULL AND body IN ('системийн','б','өөр эцэг')`, sysNote, bNote, other) != 3 {
		t.Fatal("гадны child өөрчлөгдсөн")
	}
	// амжилттай update: өөрийн id хадгална, нэгийг устгаж, нэгийг нэмнэ
	up := do(t, app, "PUT", "/kinds/"+a, f.orgA, roleAdmin, map[string]any{"version": 1,
		"notes": []map[string]any{{"id": an[1], "body": "a2-new"}, {"body": "a3"}}})
	mustStatus(t, up, 200)
	if n := noteIDs(t, up); len(n) != 2 || n[0] != an[1] {
		t.Fatalf("update: %s", up.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ? AND org_id = ? AND deleted_at IS NULL`, a, f.orgA) != 2 {
		t.Fatal("шинэ child org_id")
	}
	// өөр tenant А-ийн мөрийг харахгүй/засахгүй (404), системийн child хэвээр
	mustStatus(t, do(t, app, "GET", "/kinds/"+a, f.orgB, roleRead, nil), 404)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+a, f.orgB, roleAdmin, map[string]any{"version": 2, "notes": []map[string]any{}}), 404)
	// sysadmin (орг S) системийн int child-тай эцэг дээр ажиллахдаа тенант эцгийн child id-г заах боломжгүй
	r := do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"version": 1,
		"notes": []map[string]any{{"id": sysNote, "body": "ok"}, {"id": an[1], "body": "hack"}}})
	mustStatus(t, r, 422)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE id = ? AND body = 'a2-new'`, an[1]) != 1 {
		t.Fatal("тенант child sysadmin-аар өөрчлөгдсөн")
	}
	// delete (soft): child-ууд хэвээр, hard бол cascade (доор)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+a, f.orgA, roleAdmin, nil), 200)
}

func TestSharedIntChildrenHardDelete(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Children = append(r.Children, notesChild(f))
		r.Children = r.Children[1:]
		r.SoftDelete, r.Bulk = false, nil
	})
	f.exec(`ALTER TABLE ` + f.tbl("kind") + ` DROP COLUMN deleted_at`)
	sys := f.kindRow("", "Систем", "S")
	f.noteRow("", sys, "с")
	a := f.kindRow(f.orgA, "А", "A")
	f.noteRow(f.orgA, a, "а")
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgA, roleAdmin, nil), 403)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgS, roleSys, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ?`, sys) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ?`, a) != 1 {
		t.Fatal("hard delete + int child cascade")
	}
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+a, f.orgA, roleAdmin, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_note")+` WHERE kind_id = ?`, a) != 0 {
		t.Fatal("tenant hard delete cascade")
	}
}

func TestSharedIntChildrenMetaAndAudit(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Children = append(r.Children, notesChild(f))
		r.Bulk = nil
		r.Audit = &AuditSpec{Enabled: true, Snapshot: true}
	})
	m := do(t, app, "GET", "/kinds/meta", f.orgA, roleRead, nil)
	mustStatus(t, m, 200)
	if !strings.Contains(string(m.Raw), `"kind":"int"`) {
		t.Fatalf("meta child id kind=int: %s", m.Raw)
	}
	c := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Аудит", "notes": []map[string]any{{"body": "x"}}})
	mustStatus(t, c, 201)
	id := c.str("id")
	ns := noteIDs(t, c)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+id, f.orgS, roleSys, map[string]any{"version": 1, "notes": []map[string]any{{"id": ns[0], "body": "y"}}}), 200)
	// audit нь үйлдэл хийгчийн org-д; child diff-д int id (тоо/мөр) орно
	if f.count(`SELECT count(*) FROM khan.audit_log WHERE record_id = ? AND org_id = ?`, id, f.orgS) != 2 ||
		f.count(`SELECT count(*) FROM khan.audit_log WHERE record_id = ? AND org_id <> ?`, id, f.orgS) != 0 {
		t.Fatal("audit мөр/org буруу")
	}
	if f.count(`SELECT count(*) FROM khan.audit_log WHERE record_id = ? AND action = 'update' AND changes::text LIKE ?`, id, fmt.Sprintf(`%%"id": %d%%`, ns[0])) != 1 &&
		f.count(`SELECT count(*) FROM khan.audit_log WHERE record_id = ? AND action = 'update' AND changes::text LIKE ?`, id, fmt.Sprintf(`%%"id": "%d"%%`, ns[0])) != 1 {
		t.Fatal("audit child diff-д int id алга")
	}
}

func TestGlobalIntChildren(t *testing.T) {
	f := newSfx(t)
	app := f.mountGlobal(t, true, func(r *Resource[map[string]any]) {
		r.Children = append(r.Children, Child{Name: "tags", Label: "Шошго", Table: f.tbl("country_tag"), FK: "country_id",
			ID:     IDSpec{Column: "tag_id", Kind: IDInt},
			Fields: []Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}}})
		r.SoftDelete, r.Bulk, r.Children[0].SoftDelete = false, nil, false
	})
	f.exec(`ALTER TABLE ` + f.tbl("country") + ` DROP COLUMN deleted_at`)
	f.exec(`ALTER TABLE ` + f.tbl("country_city") + ` DROP COLUMN deleted_at`)
	tagIDs := func(r resp) []int64 {
		var out []int64
		for _, n := range r.list("tags") {
			out = append(out, int64(idNum(t, n, "tag_id")))
		}
		return out
	}
	c := do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Монгол", "tags": []map[string]any{{"name": "t1"}, {"name": "t2"}}})
	mustStatus(t, c, 201)
	id := c.str("id")
	ts := tagIDs(c)
	if len(ts) != 2 || ts[0] <= 0 {
		t.Fatalf("create: %s", c.Raw)
	}
	// бүх org шууд харна, JSON тоо
	for _, org := range []string{f.orgA, f.orgB} {
		if d := do(t, app, "GET", "/countries/"+id, org, roleRead, nil); d.Status != 200 || len(tagIDs(d)) != 2 {
			t.Fatalf("%s: %s", org, d.Raw)
		}
	}
	// Can(update) байхгүй → 403
	mustStatus(t, do(t, app, "PUT", "/countries/"+id, f.orgA, roleRead, map[string]any{"version": 1, "tags": []map[string]any{}}), 403)
	// өөр эцгийн tag id → 422
	other := f.country("Япон", "JP")
	var otherTag int64
	if err := f.db.Raw(`INSERT INTO `+f.tbl("country_tag")+` (country_id, name) VALUES (?, 'япон') RETURNING tag_id`, other).Scan(&otherTag).Error; err != nil {
		t.Fatal(err)
	}
	bad := do(t, app, "PUT", "/countries/"+id, f.orgS, roleSys, map[string]any{"version": 1, "tags": []map[string]any{{"tag_id": otherTag, "name": "hack"}}})
	mustStatus(t, bad, 422)
	if _, has := bad.errs()["tags.0.tag_id"]; !has {
		t.Fatalf("errs: %s", bad.Raw)
	}
	// update + insert + delete
	up := do(t, app, "PUT", "/countries/"+id, f.orgS, roleSys, map[string]any{"version": 1,
		"tags": []map[string]any{{"tag_id": ts[1], "name": "t2-new"}, {"name": "t3"}}})
	mustStatus(t, up, 200)
	got := tagIDs(up)
	if len(got) != 2 || got[0] != ts[1] || f.count(`SELECT count(*) FROM `+f.tbl("country_tag")+` WHERE tag_id = ?`, ts[0]) != 0 ||
		f.count(`SELECT count(*) FROM `+f.tbl("country_tag")+` WHERE tag_id = ? AND name = 'япон'`, otherTag) != 1 {
		t.Fatalf("update: %s", up.Raw)
	}
	// hard delete: зөвхөн энэ эцгийн child cascade
	mustStatus(t, do(t, app, "DELETE", "/countries/"+id, f.orgS, roleSys, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("country_tag")+` WHERE country_id = ?`, id) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("country_tag")+` WHERE country_id = ?`, other) != 1 {
		t.Fatal("global int child cascade")
	}
}
