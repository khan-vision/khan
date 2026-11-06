package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Option {
	return WithLookup(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Port != "8080" || cfg.DB.Port != "5432" || cfg.JWT.TTL != 24*time.Hour ||
		cfg.Upload.MaxBytes != 100<<20 || !cfg.SysAdmin.UUID || !cfg.App.IsProduction() {
		t.Fatalf("defaults буруу: %+v", cfg)
	}
}

func TestLoadLambdaEnvNames(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DB_HOST": "127.0.0.1", "DB_DATABASE": "khan_test", "DB_USERNAME": "u", "DB_PASSWORD": "p w",
		"DB_EXTRA": "sslmode=disable", "DB_DEBUG": "true", "DB_MAX_OPEN_CONNS": "10",
		"DB_CONN_MAX_IDLE_TIME": "5m", "JWT_SECRET": strings.Repeat("s", 32), "JWT_TTL": "2",
		"JWT_DISABLE_COOKIE_SECURE": "1", "FILE_MAX_SIZE": "5", "SYSADMIN_UUID": "false",
		"MAIL_FROM_ADDRESS": "no-reply@example.com", "APP_ENV": "test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.DB.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.JWT.Validate(); err != nil {
		t.Fatal(err)
	}
	want := DB{Host: "127.0.0.1", Port: "5432", Database: "khan_test", User: "u", Password: "p w",
		Extra: "sslmode=disable", Debug: true, Pool: Pool{MaxOpen: 10, MaxIdleTime: 5 * time.Minute}}
	if cfg.DB != want {
		t.Fatalf("DB = %+v\nwant %+v", cfg.DB, want)
	}
	if cfg.JWT.TTL != 2*time.Hour || !cfg.JWT.DisableCookieSecure || cfg.Upload.MaxBytes != 5<<20 ||
		cfg.SysAdmin.UUID || cfg.Mail.FromAddress != "no-reply@example.com" || cfg.App.IsProduction() {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadInvalidValues(t *testing.T) {
	_, err := Load(env(map[string]string{"DB_DEBUG": "maybe", "JWT_TTL": "-1", "DB_CONN_MAX_LIFETIME": "1 hour"}))
	if err == nil {
		t.Fatal("алдаа хүлээсэн")
	}
	for _, key := range []string{"DB_DEBUG", "JWT_TTL", "DB_CONN_MAX_LIFETIME"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("алдаанд %s алга: %v", key, err)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := (DB{}).Validate(); err == nil || !strings.Contains(err.Error(), "DB_HOST") {
		t.Fatalf("DB.Validate = %v", err)
	}
	if err := (JWT{Secret: "short"}).Validate(); err == nil {
		t.Fatal("богино secret зөвшөөрөгдөв")
	}
}

func TestWithEnvFileDoesNotOverrideEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KHAN_CFG_T_A=file\nKHAN_CFG_T_B=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHAN_CFG_T_A", "env")
	if _, err := Load(WithEnvFile(path)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("KHAN_CFG_T_B") })
	if got := os.Getenv("KHAN_CFG_T_A"); got != "env" {
		t.Fatalf("env файлаар дарагдав: %q", got)
	}
	if got := os.Getenv("KHAN_CFG_T_B"); got != "file" {
		t.Fatalf("файлын утга уншигдсангүй: %q", got)
	}
	if _, err := Load(WithEnvFile(filepath.Join(t.TempDir(), "missing.env"))); err == nil {
		t.Fatal("байхгүй файлд алдаа хүлээсэн")
	}
}
