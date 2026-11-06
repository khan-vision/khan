package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestWrite(t *testing.T) {
	app := fiber.New()
	app.Get("/422", func(c fiber.Ctx) error {
		e := NewErrors()
		e.Add("name", "заавал")
		return Write(c, fmt.Errorf("wrap: %w", e))
	})
	app.Get("/other", func(c fiber.Ctx) error {
		if err := Write(c, errors.New("boom")); err == nil {
			t.Error("Errors биш алдааг буцаах ёстой")
		}
		return c.SendStatus(204)
	})
	app.Get("/nil", func(c fiber.Ctx) error {
		if err := Write(c, nil); err != nil {
			t.Error("nil → nil")
		}
		return c.SendStatus(204)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/422", nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 422 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	var body struct {
		Error  string              `json:"error"`
		Errors map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "Мэдээлэл буруу" || body.Errors["name"][0] != "заавал" {
		t.Fatalf("%s", b)
	}
	for _, p := range []string{"/other", "/nil"} {
		r, err := app.Test(httptest.NewRequest("GET", p, nil), fiber.TestConfig{Timeout: 0})
		if err != nil || r.StatusCode != 204 {
			t.Fatalf("%s: %v %v", p, err, r)
		}
	}
}
