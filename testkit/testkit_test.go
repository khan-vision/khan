package testkit

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/tenant"
)

func TestGuardLocal(t *testing.T) {
	ok := []config.DB{
		{Host: "127.0.0.1", Database: "khan_vision_test"},
		{Host: "localhost", Database: "TEST_x"},
		{Host: "/tmp", Database: "khan_test"},
	}
	for _, c := range ok {
		if err := GuardLocal(c); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []config.DB{
		{Host: "db.example.com", Database: "khan_test"},
		{Host: "10.0.0.5", Database: "khan_test"},
		{Host: "127.0.0.1", Database: "khan_vision"},
	}
	for _, c := range bad {
		if err := GuardLocal(c); err == nil {
			t.Errorf("%+v зөвшөөрөгдөв", c)
		}
	}
}

func TestWithClaimsAndTenant(t *testing.T) {
	app := fiber.New()
	app.Get("/", WithClaims(Claims("u1", "aaaaaaaa-0000-4000-8000-00000000000a", 6)), func(c fiber.Ctx) error {
		org, ok := tenant.Require(c)
		if !ok {
			return nil
		}
		return c.SendString(org)
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "aaaaaaaa-0000-4000-8000-00000000000a" {
		t.Fatalf("body = %q", body)
	}
}

func TestSignedToken(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	s, err := SignedToken(secret, Claims("u1", "aaaaaaaa-0000-4000-8000-00000000000a", 6))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Parse(s, func(*jwt.Token) (any, error) { return []byte(secret), nil })
	if err != nil || !tok.Valid {
		t.Fatalf("parse: %v", err)
	}
	if _, err := SignedToken("", nil); err == nil {
		t.Fatal("хоосон secret зөвшөөрөгдөв")
	}
}
