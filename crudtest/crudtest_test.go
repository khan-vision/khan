package crudtest

import (
	"testing"
)

// TestGeneratorDetectsLeaks — зөв in-memory endpoint-д алдаа 0; цоорхой бүр өөрийн subtest-ийг унагана.
func TestGeneratorDetectsLeaks(t *testing.T) {
	t.Run("correct", func(t *testing.T) {
		app, m := newMemApp(t, leaks{})
		rec := newRecorder()
		Check(rec, memConfig(app, m))
		if len(rec.failed()) != 0 {
			t.Fatalf("зөв хэрэгжүүлэлтэд алдаа гарлаа:\n%s", rec.summary())
		}
	})
	cases := []struct {
		name    string
		lk      leaks
		subtest string
	}{
		{"list", leaks{list: true}, "list_excludes_other_org"},
		{"get", leaks{get: true}, "get_other_org_404"},
		{"update", leaks{update: true}, "update_other_org_404"},
		{"delete", leaks{del: true}, "delete_other_org_404"},
		{"bulk", leaks{bulk: true}, "bulk_other_org_affects_nothing"},
		{"body_org", leaks{bodyOrg: true}, "create_ignores_body_org_id"},
		{"ref", leaks{ref: true}, "ref_cross_org_422"},
		{"options", leaks{options: true}, "options_never_leak"},
		{"no_auth", leaks{noAuth: true}, "unauthenticated_401"},
	}
	for _, c := range cases {
		t.Run("leaky_"+c.name, func(t *testing.T) {
			app, m := newMemApp(t, c.lk)
			rec := newRecorder()
			Check(rec, memConfig(app, m))
			failed := rec.failed()
			if len(failed[c.subtest]) == 0 {
				t.Fatalf("%s цоорхойг %s илрүүлсэнгүй. Алдаанууд:\n%s", c.name, c.subtest, rec.summary())
			}
		})
	}
	t.Run("fully_leaky", func(t *testing.T) {
		app, m := newMemApp(t, leaks{true, true, true, true, true, true, true, true, true})
		rec := newRecorder()
		Check(rec, memConfig(app, m))
		if n := len(rec.failed()); n < 7 {
			t.Fatalf("бүрэн цоорхой endpoint-д зөвхөн %d subtest унав:\n%s", n, rec.summary())
		}
	})
}

func TestConfigErrors(t *testing.T) {
	app, m := newMemApp(t, leaks{})
	good := memConfig(app, m)
	for name, mut := range map[string]func(*Config){
		"app nil":      func(c *Config) { c.App = nil },
		"sample nil":   func(c *Config) { c.Sample = nil },
		"no auth":      func(c *Config) { c.Secret = "" },
		"two auth":     func(c *Config) { c.Token = func(string, string) string { return "" } },
		"same orgs":    func(c *Config) { c.OrgA, c.OrgB = "x", "x" },
		"bad endpoint": func(c *Config) { c.Meta.Endpoint = ""; c.Endpoint = "" },
	} {
		cfg := good
		mut(&cfg)
		rec := newRecorder()
		done := make(chan struct{})
		go func() { defer close(done); Check(rec, cfg) }()
		<-done
		if len(rec.failed()[""]) == 0 {
			t.Errorf("%s: тохиргооны алдаа илрэх ёстой", name)
		}
	}
}
