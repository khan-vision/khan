package gen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGeneratedCodeCompiles — golden тохиолдол бүрийн үүсгэсэн эхийг түр module-д (khan-г локал repo-д
// `replace`-лэн) бичиж `go run`-аар компайл хийж, resource бүрийн `Check()`-ийг ажиллуулна.
func TestGeneratedCodeCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go олдсонгүй")
	}
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gosum, _ := os.ReadFile(filepath.Join(repo, "go.sum"))

	dir := t.TempDir()
	mod := strings.Replace(string(gomod), "module github.com/khan-vision/khan", "module example.com/gencheck", 1)
	mod += fmt.Sprintf("\nrequire github.com/khan-vision/khan v0.0.0\n\nreplace github.com/khan-vision/khan => %s\n", repo)
	write := func(rel string, b []byte) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(mod))
	write("go.sum", gosum)

	var calls bytes.Buffer
	for _, tc := range goldenCases(t) {
		r := mustGen(t, tc.opts)
		pkg := "resources"
		if tc.opts.Package != "" {
			pkg = tc.opts.Package
		}
		write(filepath.Join(pkg, tc.name+".go"), r.Source)
		fmt.Fprintf(&calls, "\tcheck(%q, %s.%s(nil, nil))\n", tc.name, pkg, r.Func)
	}
	// Password + Force (org_id-гүй) тохиолдол: ListQuery-тэй эх ч компайл хийгдэнэ.
	form := legacyForm(t, "account", `{"model":"username","label":"Нэр","formType":"Text","rules":[]},{"model":"password_hash","label":"Нууц үг","formType":"Password","rules":[]},{"model":"email","label":"И-мэйл","formType":"Email","rules":[{"type":"required"}]}`)
	acc := mustGen(t, Options{Table: "core.account", Form: form, Name: "Account", Package: "core"})
	write("core/account.go", acc.Source)
	imports := "\t\"example.com/gencheck/resources\"\n\t\"example.com/gencheck/finance\"\n\t\"example.com/gencheck/core\"\n"
	main := "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n" + imports + ")\n\n" +
		"type checker interface{ Check() error }\n\n" +
		"var failed bool\n\n" +
		"func check(name string, r checker) {\n\tif err := r.Check(); err != nil {\n\t\tfmt.Println(name, \"FAIL:\", err)\n\t\tfailed = true\n\t\treturn\n\t}\n\tfmt.Println(name, \"ok\")\n}\n\n" +
		"func main() {\n" + calls.String() + "\tcheck(\"account\", core.AccountResource(nil, nil))\n\tif failed {\n\t\tos.Exit(1)\n\t}\n}\n"
	write("main.go", []byte(main))

	run := func(args ...string) string {
		cmd := exec.Command(goBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off", "GO111MODULE=on")
		done := make(chan struct{})
		var out []byte
		var cerr error
		go func() { out, cerr = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			_ = cmd.Process.Kill()
			t.Fatalf("go %v: хугацаа хэтэрлээ", args)
		}
		if cerr != nil {
			t.Fatalf("go %v: %v\n%s", args, cerr, out)
		}
		return string(out)
	}
	out := run("vet", "./...")
	_ = out
	out = run("run", ".")
	for _, tc := range goldenCases(t) {
		if !strings.Contains(out, tc.name+" ok") {
			t.Errorf("%s Check() ok биш:\n%s", tc.name, out)
		}
	}
	if !strings.Contains(out, "account ok") {
		t.Errorf("account Check() ok биш:\n%s", out)
	}
}
