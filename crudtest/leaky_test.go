package crudtest

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
)

const testSecret = "crudtest-secret-crudtest-secret-crudtest-secret-0123456789"

// leaks — гар бичмэл in-memory endpoint-ийн зориудын цоорхойнууд.
type leaks struct {
	list, get, update, del, bulk, bodyOrg, ref, options, noAuth bool
}

// memApp — DB-гүй, org-ыг баримталдаг (эсвэл leaks-ээр зориуд зөрчдөг) mini CRUD.
// Resource: things {name, owner_id→people}.
type memApp struct {
	mu     sync.Mutex
	things map[string]map[string]any // id → row
	people map[string]string         // id → org
	lk     leaks
	seq    int
}

func memMeta() crud.Meta {
	return crud.Meta{
		Name: "test.thing", Label: "Зүйл", Endpoint: "/things", ID: crud.IDMeta{Column: "id", Kind: crud.IDUUID},
		Fields: []crud.FieldMeta{
			{Name: "name", Label: "Нэр", Kind: validate.String, Required: true},
			{Name: "owner_id", Label: "Эзэн", Kind: validate.UUID, Nullable: true, Ref: &crud.RefMeta{Options: "/things/options/owner_id"}},
		},
		Children: []crud.ChildMeta{}, Bulk: true, BulkDelete: true, BulkFields: []string{"name"},
	}
}

func newMemApp(t testing.TB, lk leaks) (*fiber.App, *memApp) {
	t.Helper()
	m := &memApp{things: map[string]map[string]any{}, people: map[string]string{}, lk: lk}
	kr, err := auth.NewKeyring(auth.Key{ID: "k1", Secret: []byte(testSecret)})
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	mw := auth.Required(kr)
	if lk.noAuth {
		mw = auth.Optional(kr)
	}
	g := app.Group("/things", mw)
	g.Get("/", m.list)
	g.Post("/query", m.list)
	g.Post("/bulk", m.bulk)
	g.Get("/options/:field", m.options)
	g.Get("/:id", m.get)
	g.Post("/", m.create)
	g.Put("/:id", m.update)
	g.Delete("/:id", m.remove)
	return app, m
}

func (m *memApp) org(c fiber.Ctx) (string, bool) {
	if o, err := tenant.OrgID(c); err == nil {
		return o, true
	}
	if m.lk.noAuth {
		return "", true // zориуд: token-гүй хүсэлтийг нэргүй org гэж хүлээн авна
	}
	_ = c.Status(401).JSON(fiber.Map{"error": "Зөвшөөрөлгүй"})
	return "", false
}

func (m *memApp) list(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []any{}
	for _, r := range m.things {
		if r["org_id"] == org || m.lk.list {
			rows = append(rows, r)
		}
	}
	return c.JSON(fiber.Map{"data": rows, "total": len(rows)})
}

func (m *memApp) find(id, org string, leak bool) map[string]any {
	r := m.things[id]
	if r != nil && (r["org_id"] == org || leak) {
		return r
	}
	return nil
}

func (m *memApp) personOK(id, org string) bool {
	o, ok := m.people[id]
	return ok && (o == org || m.lk.ref)
}

func (m *memApp) create(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "JSON буруу"})
	}
	if m.lk.bodyOrg {
		if o, ok := body["org_id"].(string); ok {
			org = o
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := body["owner_id"].(string); ok && o != "" && !m.personOK(o, org) {
		return c.Status(422).JSON(fiber.Map{"error": "Мэдээлэл буруу", "errors": fiber.Map{"owner_id": []string{"Эзэн олдсонгүй"}}})
	}
	m.seq++
	id := newUUID()
	row := map[string]any{"id": id, "org_id": org, "name": body["name"], "owner_id": body["owner_id"]}
	m.things[id] = row
	return c.Status(201).JSON(row)
}

func (m *memApp) get(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(c.Params("id"), org, m.lk.get); r != nil {
		return c.JSON(r)
	}
	return c.Status(404).JSON(fiber.Map{"error": "Зүйл олдсонгүй"})
}

func (m *memApp) update(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	var body map[string]any
	_ = json.Unmarshal(c.Body(), &body)
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(c.Params("id"), org, m.lk.update)
	if r == nil {
		return c.Status(404).JSON(fiber.Map{"error": "Зүйл олдсонгүй"})
	}
	if o, ok := body["owner_id"].(string); ok && o != "" && !m.personOK(o, org) {
		return c.Status(422).JSON(fiber.Map{"error": "Мэдээлэл буруу", "errors": fiber.Map{"owner_id": []string{"Эзэн олдсонгүй"}}})
	}
	if n, ok := body["name"]; ok {
		r["name"] = n
	}
	if o, ok := body["owner_id"]; ok {
		r["owner_id"] = o
	}
	return c.JSON(r)
}

func (m *memApp) remove(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := c.Params("id")
	if m.find(id, org, m.lk.del) == nil {
		return c.Status(404).JSON(fiber.Map{"error": "Зүйл олдсонгүй"})
	}
	delete(m.things, id)
	return c.JSON(fiber.Map{"deleted": id})
}

func (m *memApp) bulk(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	var body struct {
		Action string
		IDs    []string
		Set    map[string]any
	}
	_ = json.Unmarshal(c.Body(), &body)
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range body.IDs {
		r := m.find(id, org, m.lk.bulk)
		if r == nil {
			continue
		}
		n++
		if body.Action == "delete" {
			delete(m.things, id)
		} else if v, ok := body.Set["name"]; ok {
			r["name"] = v
		}
	}
	return c.JSON(fiber.Map{"affected": n})
}

func (m *memApp) options(c fiber.Ctx) error {
	org, ok := m.org(c)
	if !ok {
		return nil
	}
	if c.Params("field") != "owner_id" {
		return c.Status(404).JSON(fiber.Map{"error": "Олдсонгүй"})
	}
	ids := map[string]bool{}
	for _, id := range strings.Split(c.Query("ids"), ",") {
		if id != "" {
			ids[id] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data := []any{}
	for id, o := range m.people {
		if o != org && !m.lk.options {
			continue
		}
		if len(ids) > 0 && !ids[id] {
			continue
		}
		data = append(data, fiber.Map{"value": id, "label": "Хүн " + id[:4]})
	}
	return c.JSON(fiber.Map{"data": data})
}

func (m *memApp) person(org string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := newUUID()
	m.people[id] = org
	return id
}

func memConfig(app *fiber.App, m *memApp) Config {
	return Config{
		App: app, Meta: memMeta(), Secret: testSecret,
		Sample: func(org string) map[string]any {
			return map[string]any{"name": "Зүйл " + newUUID()[:8], "owner_id": m.person(org)}
		},
		RefSample: map[string]func(string) string{"owner_id": m.person},
	}
}

var _ = query.KindText // BulkField-д ашиглагдах төрөл (docs)
