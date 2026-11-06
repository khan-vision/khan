// Command khan — Khan library-ийн offline хэрэгсэл.
//
//	khan schema dump [-env .env] [-schema finance,hr] [-ignore t1,t2] [-o catalog.json]
//	khan schema lint [-env .env] [-schema finance] [-tables finance.invoice,...]
//	khan schema diff old.json new.json
//	khan schema rls [-env .env] -schema finance [-mode soft|strict] [-o 0007_rls_finance.sql]
//	khan gen resource -table schema.name [-form F.json] [-grid G.json] [-name N] [-label L] [-endpoint E] [-pkg P]
//	                  [-o FILE] [-catalog catalog.json | -env .env] [-force] [-hidden createonly|keep|exclude] [-ref-label f=a+b]
//	khan gen audit -forms DIR [-models DIR] [-grids DIR] [-catalog catalog.json | -env .env] [-force] [-v]
//	khan migrate status -dir DIR [-env .env] [-table khan.schema_migrations]
//	khan migrate up -dir DIR [-env .env] [-lock-timeout 5s] [-statement-timeout 15m] [-dry-run] [-lint-schema a,b]
//	khan parity corpus -frontend DIR [-o corpus.jsonl] ...  /  khan parity run -a URL -b URL -corpus C.jsonl ...
//
// `schema` команд DB-ээс ЗӨВХӨН уншина (pg_catalog SELECT). `migrate up` нь DB-д бичнэ
// (-dry-run нь зөвхөн pending жагсаана).
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
	"github.com/khan-vision/khan/migrate"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const usage = `khan — Khan library CLI

Хэрэглээ:
  khan schema dump [-env FILE] [-schema a,b] [-exclude a,b] [-ignore t1,t2] [-no-views] [-o FILE]
  khan schema lint [-env FILE] [-schema a,b] [-tables s.t1,s.t2]
  khan schema diff OLD.json NEW.json
  khan schema rls [-env FILE] -schema a,b [-mode soft|strict] [-o FILE]   (org_id-тэй хүснэгтийн RLS policy SQL)
  khan gen resource -table s.t [-form F.json] [-grid G.json] [-name N] [-label L] [-endpoint E] [-pkg P] [-o FILE]
                    [-catalog catalog.json | -env FILE] [-force] [-hidden createonly|keep|exclude] [-ref-label f=a+b]
                    (хуучин Lambda form/grid + DB бүтцээс crud.Resource-ийн Go ноорог; docs/GEN.md)
  khan gen audit -forms DIR [-models DIR] [-grids DIR] [-catalog catalog.json | -env FILE] [-force] [-v]
                    (олон формыг файл бичихгүй шалгаж, TODO-ийн статистик гаргана)
  khan migrate status -dir DIR [-env FILE] [-table NAME] [-allow-missing]
  khan migrate up -dir DIR [-env FILE] [-table NAME] [-lock-timeout 5s] [-statement-timeout 15m]
                  [-allow-missing] [-dry-run] [-lint-schema a,b]
  khan parity corpus -frontend DIR [-o corpus.jsonl] [-params P.json] [-merge a.jsonl,b.jsonl] [-auth PROFILE]
  khan parity run -a URL -b URL -corpus C.jsonl [-profiles P.json] [-ignore I.json] [-expect E.json]
                  [-allow-mutations] [-allow-remote] [-o DIR]   (A/B behaviour parity; docs/PARITY.md)
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "khan:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || (args[0] != "schema" && args[0] != "migrate" && args[0] != "gen" && args[0] != "parity") {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("команд буруу")
	}
	if args[0] == "parity" {
		return parityCmd(args[1:], stdout, stderr)
	}
	if args[0] == "gen" {
		return genCmd(args[1:], stdout, stderr)
	}
	if args[0] == "migrate" {
		switch args[1] {
		case "status":
			return migrateStatus(args[2:], stdout)
		case "up":
			return migrateUp(args[2:], stdout)
		default:
			fmt.Fprint(stderr, usage)
			return fmt.Errorf("үл мэдэгдэх migrate команд %q", args[1])
		}
	}
	switch args[1] {
	case "dump":
		return schemaDump(args[2:], stdout)
	case "lint":
		return schemaLint(args[2:], stdout)
	case "diff":
		return schemaDiff(args[2:], stdout)
	case "rls":
		return schemaRLS(args[2:], stdout)
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

// openDB — env файл (хоосон бол зөвхөн орчны хувьсагч)-аар DB нээнэ.
func openDB(env string) (*gorm.DB, error) {
	var opts []config.Option
	if env != "" {
		opts = append(opts, config.WithEnvFile(env))
	}
	cfg, err := config.Load(opts...)
	if err != nil {
		return nil, err
	}
	return db.Open(cfg.DB, db.WithLogger(logger.Discard))
}

func (f dbFlags) catalog() (*schema.Catalog, error) {
	gdb, err := openDB(*f.env)
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

// schemaRLS — catalog-ийн org_id баганатай хүснэгт бүрт RLS policy DDL (migration
// файл болгон хадгалж, `khan migrate up`-аар хэрэглэнэ).
func schemaRLS(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("schema rls", flag.ContinueOnError)
	df := addDBFlags(fs)
	mode := fs.String("mode", "soft", "soft (app.org_id тавиагүй бол бүгд) | strict (тавиагүй бол юу ч үгүй)")
	out := fs.String("o", "", "гаралтын файл (хоосон = stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var pm tenant.PolicyMode
	switch *mode {
	case "soft":
		pm = tenant.PolicySoft
	case "strict":
		pm = tenant.PolicyStrict
	default:
		return fmt.Errorf("-mode soft|strict байх ёстой: %q", *mode)
	}
	if len(split(*df.schemas)) == 0 {
		return fmt.Errorf("-schema заавал (бүх DB-д нэг дор RLS асаахгүй)")
	}
	*df.noViews = true
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
	tables := tenant.TenantTables(cat)
	fmt.Fprintf(w, "-- khan schema rls -mode %s: %d хүснэгт\n", *mode, len(tables))
	for _, t := range tables {
		sql, err := tenant.PolicySQLForTable(t, pm)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "\n-- %s\n%s", t.QualifiedName(), sql)
	}
	return nil
}

type migrateFlags struct {
	env, dir, table *string
	allowMissing    *bool
}

func addMigrateFlags(fs *flag.FlagSet) migrateFlags {
	return migrateFlags{
		env:          fs.String("env", "", "env файл (орчны хувьсагч давуу)"),
		dir:          fs.String("dir", "", "migration SQL файлуудын хавтас (заавал)"),
		table:        fs.String("table", "", "хяналтын хүснэгт (анхдагч "+migrate.DefaultTable+")"),
		allowMissing: fs.Bool("allow-missing", false, "DB-д байгаа боловч хавтсанд алга болсон хувилбарыг зөвшөөрөх"),
	}
}

// load — хавтсаас migration унших ба DB нээх.
func (f migrateFlags) load() ([]migrate.Migration, *gorm.DB, migrate.Options, error) {
	opts := migrate.Options{Table: *f.table, AllowMissing: *f.allowMissing}
	if *f.dir == "" {
		return nil, nil, opts, fmt.Errorf("-dir заавал")
	}
	ms, err := migrate.Load(os.DirFS(*f.dir))
	if err != nil {
		return nil, nil, opts, err
	}
	gdb, err := openDB(*f.env)
	if err != nil {
		return nil, nil, opts, err
	}
	return ms, gdb, opts, nil
}

func migrateStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate status", flag.ContinueOnError)
	mf := addMigrateFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ms, gdb, opts, err := mf.load()
	if err != nil {
		return err
	}
	defer db.Close(gdb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := migrate.StatusOf(ctx, gdb, ms, opts)
	if err != nil {
		return err
	}
	pending := 0
	for _, s := range st {
		if s.Applied {
			fmt.Fprintf(stdout, "applied  %s  %s\n", s.File(), s.AppliedAt.Format(time.RFC3339))
		} else {
			fmt.Fprintf(stdout, "pending  %s\n", s.File())
			pending++
		}
	}
	fmt.Fprintf(stdout, "%d migration, %d pending\n", len(st), pending)
	return nil
}

func migrateUp(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate up", flag.ContinueOnError)
	mf := addMigrateFlags(fs)
	lockTimeout := fs.Duration("lock-timeout", migrate.DefaultLockTimeout, "migration бүрийн lock_timeout (<0 = унтраана)")
	stmtTimeout := fs.Duration("statement-timeout", migrate.DefaultStatementTimeout, "migration бүрийн statement_timeout (<0 = унтраана)")
	dryRun := fs.Bool("dry-run", false, "ажиллуулахгүй, зөвхөн pending жагсаах")
	lint := fs.String("lint-schema", "", "up-ийн дараа эдгээр schema-г lint хийх (таслалаар); зөрчил = алдаа")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ms, gdb, opts, err := mf.load()
	if err != nil {
		return err
	}
	defer db.Close(gdb)
	opts.LockTimeout, opts.StatementTimeout = *lockTimeout, *stmtTimeout
	opts.Logf = func(format string, a ...any) { fmt.Fprintf(stdout, format+"\n", a...) }
	ctx := context.Background()

	if *dryRun {
		pend, err := migrate.Pending(ctx, gdb, ms, opts)
		if err != nil {
			return err
		}
		for _, m := range pend {
			fmt.Fprintf(stdout, "pending  %s\n", m.File())
		}
		fmt.Fprintf(stdout, "%d pending (dry-run, юу ч ажиллуулаагүй)\n", len(pend))
		return nil
	}
	applied, err := migrate.Up(ctx, gdb, ms, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d migration хэрэглэгдлээ\n", len(applied))

	if schemas := split(*lint); len(schemas) > 0 {
		lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cat, err := schema.Introspect(lctx, gdb, schema.Filter{Schemas: schemas})
		if err != nil {
			return err
		}
		count := 0
		for _, t := range cat.Tables {
			for _, issue := range schema.Lint(t) {
				fmt.Fprintln(stdout, issue)
				count++
			}
		}
		if count > 0 {
			return fmt.Errorf("%d lint зөрчил", count)
		}
		fmt.Fprintln(stdout, "lint: зөрчилгүй")
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
