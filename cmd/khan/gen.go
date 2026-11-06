package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/khan-vision/khan/gen"
	"github.com/khan-vision/khan/schema"
)

// genCmd — `khan gen resource|audit`.
func genCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("gen: дэд команд заавал (resource|audit)")
	}
	switch args[0] {
	case "resource":
		return genResource(args[1:], stdout, stderr)
	case "audit":
		return genAudit(args[1:], stdout)
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("үл мэдэгдэх gen команд %q", args[0])
}

// refLabels — давтагддаг `-ref-label field=col1+col2` flag.
type refLabels map[string][]string

func (r refLabels) String() string { return fmt.Sprint(map[string][]string(r)) }
func (r refLabels) Set(v string) error {
	name, cols, ok := strings.Cut(v, "=")
	if !ok || name == "" || cols == "" {
		return fmt.Errorf("-ref-label field=col1+col2 хэлбэртэй байх ёстой: %q", v)
	}
	r[name] = strings.Split(cols, "+")
	return nil
}

// catalogFor — `-catalog` snapshot файл, эс бөгөөс DB-ээс.
func catalogFor(path string, df dbFlags) (*schema.Catalog, error) {
	if path != "" {
		return readCatalog(path)
	}
	return df.catalog()
}

func readForm(path string) (*gen.LegacyForm, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return gen.ParseForm(f)
}

func readGrid(path string) (*gen.LegacyGrid, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return gen.ParseGrid(f)
}

func genResource(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen resource", flag.ContinueOnError)
	df := addDBFlags(fs)
	table := fs.String("table", "", "хүснэгт: schema.name (хоосон бол -form-ын model)")
	form := fs.String("form", "", "хуучин Lambda form spec (main/lambda/schemas/form/<id>.json)")
	grid := fs.String("grid", "", "хуучин Lambda grid spec (main/lambda/schemas/grid/<id>.json)")
	name := fs.String("name", "", "Go нэрийн угтвар (<name>Resource); анхдагч хүснэгтийн нэр")
	label := fs.String("label", "", "Монгол нэр")
	endpoint := fs.String("endpoint", "", "анхдагч /api/<schema>/<table>")
	pkg := fs.String("pkg", "resources", "гаралтын package")
	out := fs.String("o", "", "гаралтын файл (хоосон = stdout)")
	catPath := fs.String("catalog", "", "`khan schema dump`-ийн snapshot (өгвөл DB-д холбогдохгүй)")
	force := fs.Bool("force", false, "lint-ийн хаах зөрчил (org_id/PK) байсан ч үүсгэх")
	hidden := fs.String("hidden", "createonly", "hidden талбар: createonly|keep|exclude")
	labels := refLabels{}
	fs.Var(labels, "ref-label", "FK талбарын lookup Label: field=col1+col2 (давтаж болно)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := gen.Options{
		Table: *table, Name: *name, Label: *label, Endpoint: *endpoint, Package: *pkg,
		Force: *force, HiddenMode: gen.HiddenMode(*hidden), RefLabels: labels,
	}
	var err error
	if *form != "" {
		if opts.Form, err = readForm(*form); err != nil {
			return err
		}
	}
	if *grid != "" {
		if opts.Grid, err = readGrid(*grid); err != nil {
			return err
		}
	}
	if opts.Table == "" && opts.Form == nil {
		return fmt.Errorf("-table эсвэл -form заавал")
	}
	if opts.Catalog, err = catalogFor(*catPath, df); err != nil {
		return err
	}
	res, err := gen.Generate(opts)
	if err != nil {
		return err
	}
	if *out == "" {
		if _, err := stdout.Write(res.Source); err != nil {
			return err
		}
	} else if err := os.WriteFile(*out, res.Source, 0o644); err != nil {
		return err
	}
	for _, t := range res.Todos {
		fmt.Fprintln(stderr, t)
	}
	fmt.Fprintf(stderr, "gen: %s → %s: %d талбар, %d child, %d TODO\n", res.Table, res.Func, res.Fields, res.Children, len(res.Todos))
	return nil
}

func genAudit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("gen audit", flag.ContinueOnError)
	df := addDBFlags(fs)
	forms := fs.String("forms", "", "form spec-үүдийн хавтас (заавал)")
	models := fs.String("models", "", "Lambda formModels хавтас (form id → schema.table; хоосон бол model нэрээр)")
	grids := fs.String("grids", "", "grid spec-үүдийн хавтас (заавал биш)")
	catPath := fs.String("catalog", "", "`khan schema dump`-ийн snapshot (өгвөл DB-д холбогдохгүй)")
	force := fs.Bool("force", false, "lint-ийн хаах зөрчилтэй ч үүсгэх")
	verbose := fs.Bool("v", false, "форм тус бүрийн мөр + TODO")
	outDir := fs.String("o", "", "өгвөл үүссэн Go файлуудыг (<id>_<table>.go) энэ хавтсанд бичнэ")
	pkg := fs.String("pkg", "resources", "-o-той үед package нэр")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *forms == "" {
		return fmt.Errorf("-forms заавал")
	}
	cat, err := catalogFor(*catPath, df)
	if err != nil {
		return err
	}
	rep, err := gen.Audit(gen.AuditOptions{Catalog: cat, FormsDir: *forms, ModelsDir: *models, GridsDir: *grids, Force: *force, OutDir: *outDir, Package: *pkg})
	if err != nil {
		return err
	}
	rep.Write(stdout, *verbose)
	return nil
}
