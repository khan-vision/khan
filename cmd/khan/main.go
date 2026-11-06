// Command khan — Khan library-ийн offline хэрэгсэл.
//
//	khan schema dump [-env .env] [-schema finance,hr] [-ignore t1,t2] [-o catalog.json]
//	khan schema lint [-env .env] [-schema finance] [-tables finance.invoice,...]
//	khan schema diff old.json new.json
//
// Бүх команд DB-ээс ЗӨВХӨН уншина (pg_catalog SELECT).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/schema"
	"gorm.io/gorm/logger"
)

const usage = `khan — Khan library CLI

Хэрэглээ:
  khan schema dump [-env FILE] [-schema a,b] [-exclude a,b] [-ignore t1,t2] [-no-views] [-o FILE]
  khan schema lint [-env FILE] [-schema a,b] [-tables s.t1,s.t2]
  khan schema diff OLD.json NEW.json
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "khan:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "schema" {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("команд буруу")
	}
	switch args[1] {
	case "dump":
		return schemaDump(args[2:], stdout)
	case "lint":
		return schemaLint(args[2:], stdout)
	case "diff":
		return schemaDiff(args[2:], stdout)
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("үл мэдэгдэх schema команд %q", args[1])
	}
}

type dbFlags struct {
	env, schemas, exclude, ignore *string
	noViews                       *bool
}

func addDBFlags(fs *flag.FlagSet) dbFlags {
	return dbFlags{
		env:     fs.String("env", "", "env файл (орчны хувьсагч давуу)"),
		schemas: fs.String("schema", "", "зөвхөн эдгээр schema (таслалаар)"),
		exclude: fs.String("exclude", "", "хасах schema (таслалаар)"),
		ignore:  fs.String("ignore", "", "хасах хүснэгт: schema.name эсвэл name (таслалаар)"),
		noViews: fs.Bool("no-views", false, "view/matview-гүй"),
	}
}

func (f dbFlags) catalog() (*schema.Catalog, error) {
	var opts []config.Option
	if *f.env != "" {
		opts = append(opts, config.WithEnvFile(*f.env))
	}
	cfg, err := config.Load(opts...)
	if err != nil {
		return nil, err
	}
	gdb, err := db.Open(cfg.DB, db.WithLogger(logger.Discard))
	if err != nil {
		return nil, err
	}
	defer db.Close(gdb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return schema.Introspect(ctx, gdb, schema.Filter{
		Schemas:        split(*f.schemas),
		ExcludeSchemas: split(*f.exclude),
		Ignore:         split(*f.ignore),
		SkipViews:      *f.noViews,
	})
}

func schemaDump(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("schema dump", flag.ContinueOnError)
	df := addDBFlags(fs)
	out := fs.String("o", "", "гаралтын файл (хоосон = stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := df.catalog()
	if err != nil {
		return err
	}
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	return cat.WriteJSON(w)
}

func schemaLint(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("schema lint", flag.ContinueOnError)
	df := addDBFlags(fs)
	tables := fs.String("tables", "", "зөвхөн эдгээр хүснэгт (schema.name, таслалаар)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := df.catalog()
	if err != nil {
		return err
	}
	only := split(*tables)
	count := 0
	for _, t := range cat.Tables {
		if len(only) > 0 && !containsStr(only, t.QualifiedName()) {
			continue
		}
		for _, issue := range schema.Lint(t) {
			fmt.Fprintln(stdout, issue)
			count++
		}
	}
	if count > 0 {
		return fmt.Errorf("%d зөрчил", count)
	}
	fmt.Fprintln(stdout, "зөрчилгүй")
	return nil
}

func schemaDiff(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("schema diff OLD.json NEW.json")
	}
	from, err := readCatalog(args[0])
	if err != nil {
		return err
	}
	to, err := readCatalog(args[1])
	if err != nil {
		return err
	}
	changes := schema.Diff(from, to)
	for _, c := range changes {
		fmt.Fprintln(stdout, c)
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "ялгаагүй")
	}
	return nil
}

func readCatalog(path string) (*schema.Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return schema.ReadJSON(f)
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
