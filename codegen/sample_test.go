package codegen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
)

var update = flag.Bool("update", false, "golden файлуудыг шинэчлэх")

func f64(v float64) *float64 { return &v }

// sampleMetas — бүх Kind, enum, ref (parent-тэй), children, Кирилл шошго, тусгай тэмдэгттэй meta.
func sampleMetas() []crud.Meta {
	room := crud.Meta{
		Name: "organization.conference_room", Label: "Хурлын өрөө", Endpoint: "/api/organization/conference/rooms",
		Table: "organization.conference_room", ID: crud.IDMeta{Column: "id", Kind: crud.IDUUID},
		Version: "version", SoftDelete: true, StrictUnknown: true,
		Fields: []crud.FieldMeta{
			{Name: "room_name", Label: "Нэр", Kind: validate.String, Required: true, MaxLen: 500, MinLen: 2},
			{Name: "code", Label: "Код \"A\" */ <b>", Kind: validate.String, Nullable: true, Unique: true, MaxLen: 20,
				Pattern: `(?i)^[a-z0-9-]+$`, PatternMsg: "Код зөвхөн латин үсэг, тоо"},
			{Name: "description", Label: "Тайлбар", Kind: validate.Text, Nullable: true, MaxLen: 10000},
			{Name: "capacity", Label: "Багтаамж", Kind: validate.Int, Nullable: true, Min: f64(0), Max: f64(100000)},
			{Name: "price", Label: "Үнэ", Kind: validate.Money, Nullable: true, Min: f64(0), Scale: 2},
			{Name: "area", Label: "Талбай (м2)", Kind: validate.Number, Required: true, Scale: 3, Min: f64(0.5), Max: f64(9999.5)},
			{Name: "active", Label: "Идэвхтэй", Kind: validate.Bool, Default: true},
			{Name: "opens", Label: "Нээх огноо", Kind: validate.Date, Nullable: true},
			{Name: "starts_at", Label: "Эхлэх цаг", Kind: validate.DateTime},
			{Name: "open_time", Label: "Нээх цаг", Kind: validate.Time, Nullable: true},
			{Name: "status", Label: "Төлөв", Kind: validate.Enum, Default: "draft", Enum: []string{"draft", "open", "closed"}},
			{Name: "meta", Label: "Мета", Kind: validate.JSON, Required: true},
			{Name: "phone", Label: "Утас", Kind: validate.Phone, Nullable: true},
			{Name: "email", Label: "Имэйл", Kind: validate.Email, Nullable: true, MaxLen: 120},
			{Name: "secret", Label: "Нууц үг", Kind: validate.Password, Required: true, CreateOnly: true, MinLen: 8},
			{Name: "ref_code", Label: "Лавлах UUID", Kind: validate.UUID, Nullable: true},
			{Name: "owner_id", Label: "Эзэмшигч", Kind: validate.UUID, Nullable: true,
				Ref: &crud.RefMeta{Options: "/api/organization/conference/rooms/options/owner_id", Parent: true}},
			{Name: "manager_id", Label: "Менежер", Kind: validate.UUID, Nullable: true, Input: "employee",
				Ref: &crud.RefMeta{Options: "/api/organization/conference/rooms/options/manager_id"}},
			{Name: "note", Label: "Тэмдэглэл", Kind: validate.Text, Nullable: true, CreateOnly: true},
		},
		Children: []crud.ChildMeta{{
			Name: "equipment", Label: "Тоног төхөөрөмж", MaxRows: 50,
			Fields: []crud.FieldMeta{
				{Name: "item_id", Label: "Бараа", Kind: validate.UUID, Required: true,
					Ref: &crud.RefMeta{Options: "/api/organization/conference/rooms/options/equipment.item_id"}},
				{Name: "qty", Label: "Тоо", Kind: validate.Int, Required: true, Min: f64(1)},
				{Name: "unit_price", Label: "Нэгж үнэ", Kind: validate.Money, Required: true},
			},
		}},
		Filterable: map[string]query.FilterKind{"room_name": query.KindText, "status": query.KindEnum, "capacity": query.KindNumber},
		Sort:       []string{"capacity", "room_name"},
		Search:     true, Totals: []string{"capacity"},
		Export: true, Bulk: true, BulkFields: []string{"capacity", "status"}, BulkDelete: true,
	}
	tag := crud.Meta{
		Name: "organization.tag", Label: "Шошго", Endpoint: "/api/organization/tags", Table: "organization.tag",
		ID:         crud.IDMeta{Column: "id", Kind: crud.IDInt},
		Fields:     []crud.FieldMeta{{Name: "label", Label: "Нэр", Kind: validate.String, Required: true, MaxLen: 255}},
		Filterable: map[string]query.FilterKind{},
	}
	kind := crud.Meta{
		Name: "attendance.request_kind", Label: "Хүсэлтийн төрөл", Endpoint: "/api/attendance/request-kinds", Table: "attendance.lut_attendance_request",
		ID: crud.IDMeta{Column: "id", Kind: crud.IDUUID}, Scope: "shared", SystemWrites: true,
		Fields:     []crud.FieldMeta{{Name: "name", Label: "Нэр", Kind: validate.String, Required: true, MaxLen: 100}},
		Filterable: map[string]query.FilterKind{},
	}
	country := crud.Meta{
		Name: "platform.country", Label: "Улс", Endpoint: "/api/platform/countries", Table: "platform.country",
		ID: crud.IDMeta{Column: "id", Kind: crud.IDUUID}, Scope: "global",
		Fields:     []crud.FieldMeta{{Name: "name", Label: "Нэр", Kind: validate.String, Required: true, MaxLen: 100}},
		Filterable: map[string]query.FilterKind{},
	}
	return []crud.Meta{tag, room, kind, country} // зориуд эрэмбэгүй
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./codegen -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s golden-оос зөрөв (go test ./codegen -update):\n%s", name, got)
	}
}
