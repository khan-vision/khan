package crud

import (
	"strings"
	"testing"
	"time"

	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
)

// Аюулгүй байдлын review-ийн олдвор бүрийн регресс тест.

func TestReviewDefaultOrderWithoutListSpec(t *testing.T) {
	f := newFx(t)
	r := f.tagResource()
	r.List = query.Spec{} // DefaultOrder хоосон → өмнө нь "t. ASC" болж 500
	app := mountOn(t, "/tags", r)
	mustStatus(t, do(t, app, "POST", "/tags", f.orgA, 1, map[string]any{"label": "x"}), 201)
	mustStatus(t, do(t, app, "GET", "/tags", f.orgA, 2, nil), 200)
}

func TestReviewNULInputsAre4xx(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "meta": "\u0000"})
	mustStatus(t, r, 422)
	if _, ok := r.errs()["meta"]; !ok {
		t.Fatalf("meta талбарын алдаа хүлээсэн: %s", r.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/rooms?search=%00", f.orgA, 2, nil), 200)
	mustStatus(t, do(t, app, "GET", "/rooms/options/owner_id?search=%00", f.orgA, 2, nil), 200)
	mustStatus(t, do(t, app, "GET", `/rooms?filters={"name":{"op":"contains","v":"\u0000"}}`, f.orgA, 2, nil), 400)
}

func TestReviewIntIDBeyondInt4Is404(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/accounts", f.accountResource()) // serial = int4
	mustStatus(t, do(t, app, "GET", "/accounts/2147483648", f.orgA, 1, nil), 404)
	mustStatus(t, do(t, app, "DELETE", "/accounts/9223372036854775807", f.orgA, 1, nil), 404)
	mustStatus(t, do(t, app, "PUT", "/accounts/3000000000", f.orgA, 1, map[string]any{"display": "x"}), 404)

	spec := lookup.Spec{Table: f.tbl("account"), Kind: validate.Int, Label: []string{"login"}}
	missing, err := spec.Exists(f.db, f.orgA, []any{int64(3000000000)})
	if err != nil || len(missing) != 1 {
		t.Fatalf("Exists(int4-өөс их) = %v, %v; «олдсонгүй» хүлээсэн", missing, err)
	}
}

func TestReviewHookCannotOverrideProtected(t *testing.T) {
	f := newFx(t)
	r := f.roomResource()
	r.Bulk = nil // BeforeUpdate-тэй resource-д bulk update хориотой (fail-closed)
	r.Hooks.BeforeUpdate = func(x *Ctx, v Values) error {
		v["org_id"] = f.orgB
		v["deleted_at"] = time.Now()
		v["version"] = 100
		return nil
	}
	app := mountOn(t, "/rooms", r)
	id := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a"}).str("id")
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "b", "version": 1}), 200)
	if n := f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE id = ? AND org_id = ? AND deleted_at IS NULL AND version = 2 AND name = 'b'`, id, f.orgA); n != 1 {
		t.Fatal("hook хамгаалагдсан баганыг (org_id/deleted_at/version) дарж бичив")
	}
}

func TestReviewMetaHidesTable(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	m := do(t, app, "GET", "/rooms/meta", f.orgA, 2, nil)
	mustStatus(t, m, 200)
	if strings.Contains(string(m.Raw), f.schema) || strings.Contains(string(m.Raw), `"table"`) {
		t.Fatalf("meta нь DB хүснэгтийн нэрийг ил гаргав: %s", m.Raw)
	}
}

func TestReviewBulkDeleteRunsDeleteHooks(t *testing.T) {
	f := newFx(t)
	r := f.roomResource()
	var after []string
	r.Hooks.BeforeDelete = func(x *Ctx) error {
		if x.Old["name"] == "locked" {
			return Conflict("Түгжигдсэн өрөөг устгах боломжгүй")
		}
		return nil
	}
	r.Hooks.AfterDelete = func(x *Ctx) error { after = append(after, x.ID); return nil }
	app := mountOn(t, "/rooms", r)
	free := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "free"}).str("id")
	locked := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "locked"}).str("id")

	// Түгжигдсэн мөр орсон bulk → бүхэлдээ 409, юу ч устгагдахгүй (нэг транзакц).
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": []string{free, locked}}), 409)
	if n := f.count(`SELECT count(*) FROM ` + f.tbl("room") + ` WHERE deleted_at IS NOT NULL`); n != 0 {
		t.Fatalf("BeforeDelete-ийг тойрч %d мөр устгав", n)
	}
	after = nil // 409-ийн транзакц rollback болсон (DB), Go slice-ийг л цэвэрлэнэ
	d := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": []string{free}})
	mustStatus(t, d, 200)
	if d.JSON["affected"] != float64(1) || len(after) != 1 || after[0] != free {
		t.Fatalf("bulk delete: %s after=%v", d.Raw, after)
	}
}

func TestReviewMountFailClosed(t *testing.T) {
	f := newFx(t)
	bulkUpdateWithValidate := f.roomResource()
	bulkUpdateWithValidate.Validate = func(x *Ctx, v Values) error { return nil }

	childPassword := f.invoiceResource()
	childPassword.Children[1].Fields = append(childPassword.Children[1].Fields,
		Field{Name: "secret", Label: "Нууц", Kind: validate.Password})

	protectedBulk := f.roomResource()
	protectedBulk.Bulk.Fields = map[string]query.BulkField{"owner": {Col: "created_by", Kind: query.KindUUID, Nullable: true}}

	for name, err := range map[string]error{
		"bulk update + Validate": bulkUpdateWithValidate.Check(),
		"child Password":         childPassword.Check(),
		"bulk protected column":  protectedBulk.Check(),
	} {
		if err == nil {
			t.Errorf("%s: Mount алдаа хүлээсэн", name)
		}
	}
}

func TestReviewRequestTimeout(t *testing.T) {
	f := newFx(t)
	r := f.tagResource()
	r.Timeout = 100 * time.Millisecond
	r.Hooks.BeforeCreate = func(x *Ctx, v Values) error { return x.Tx.Exec("SELECT pg_sleep(3)").Error }
	app := mountOn(t, "/tags", r)
	start := time.Now()
	mustStatus(t, do(t, app, "POST", "/tags", f.orgA, 1, map[string]any{"label": "x"}), 500)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("timeout ажиллаагүй: %s", el)
	}
	if n := f.count(`SELECT count(*) FROM ` + f.tbl("tag")); n != 0 {
		t.Fatal("цуцлагдсан хүсэлт мөр үлдээв")
	}
}
