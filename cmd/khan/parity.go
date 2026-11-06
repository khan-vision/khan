package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/khan-vision/khan/parity"
)

// parityCmd — `khan parity run|corpus`. Дэлгэрэнгүй: docs/PARITY.md.
func parityCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("parity: дэд команд хэрэгтэй (run|corpus)")
	}
	switch args[0] {
	case "run":
		return parityRun(args[1:], stdout)
	case "corpus":
		return parityCorpus(args[1:], stdout)
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("үл мэдэгдэх parity команд %q", args[0])
}

func parityRun(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("parity run", flag.ContinueOnError)
	a := fs.String("a", "", "A = одоогийн систем (base URL)")
	b := fs.String("b", "", "B = нэр дэвшигч (base URL)")
	corpus := fs.String("corpus", "", "корпус JSONL")
	profiles := fs.String("profiles", "", "auth profiles.json")
	ignore := fs.String("ignore", "", "ignore.json (нэмэлт ignore/unordered зам)")
	expect := fs.String("expect", "", "expected.json (санаатай ялгааны allowlist)")
	mut := fs.Bool("allow-mutations", false, "POST/PUT/PATCH/DELETE тоглуулна (зөвхөн хоёул loopback)")
	remote := fs.Bool("allow-remote", false, "loopback биш хаягийг зөвшөөрнө (зөвхөн GET/HEAD)")
	out := fs.String("o", "parity-report", "тайлангийн хавтас")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *a == "" || *b == "" || *corpus == "" {
		return fmt.Errorf("-a, -b, -corpus заавал")
	}
	reqs, err := parity.LoadCorpus(*corpus)
	if err != nil {
		return err
	}
	var prof parity.Profiles
	if *profiles != "" {
		if prof, err = parity.LoadProfiles(*profiles); err != nil {
			return err
		}
	}
	ign, err := parity.LoadIgnore(*ignore)
	if err != nil {
		return err
	}
	exp, err := parity.LoadExpected(*expect)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := parity.Run(ctx, parity.Options{
		A: *a, B: *b, Corpus: reqs, Profiles: prof, Ignore: ign, Expected: exp,
		AllowMutations: *mut, AllowRemote: *remote,
	})
	if err != nil {
		return err
	}
	if err := parity.WriteReports(*out, rep); err != nil {
		return err
	}
	s := rep.Summary
	fmt.Fprintf(stdout, "total=%d identical=%d differing=%d expected=%d errors=%d skipped=%d\nтайлан: %s\n",
		s.Total, s.Identical, s.Differing, s.Expected, s.Errors, s.Skipped, filepath.Join(*out, "report.txt"))
	if rep.Failed() {
		return fmt.Errorf("parity: %d гэнэтийн ялгаа, %d алдаа", s.Differing, s.Errors)
	}
	return nil
}

func parityCorpus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("parity corpus", flag.ContinueOnError)
	front := fs.String("frontend", "", "front-end эх кодын хавтас (жишээ нь ../front_end/src)")
	out := fs.String("o", "corpus.jsonl", "гаралтын JSONL")
	params := fs.String("params", "", "placeholder -> бодит id JSON (жишээ нь {\"id\":\"...\",\"chosen.id\":\"...\"})")
	merge := fs.String("merge", "", "гараар бичсэн корпус(ууд) (таслалаар); давхардалд эдгээр давуу")
	auth := fs.String("auth", "", "үүсгэсэн мөрөнд оноох auth profile")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *front == "" {
		return fmt.Errorf("-frontend заавал")
	}
	opt := parity.ExtractOptions{Auth: *auth}
	if *params != "" {
		bs, err := os.ReadFile(*params)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(bs, &opt.Params); err != nil {
			return fmt.Errorf("params: %w", err)
		}
	}
	resolved, unresolved, st, err := parity.ExtractFrontend(*front, opt)
	if err != nil {
		return err
	}
	var lists [][]parity.Request
	for _, m := range strings.Split(*merge, ",") {
		if m = strings.TrimSpace(m); m != "" {
			l, err := parity.LoadCorpus(m)
			if err != nil {
				return err
			}
			lists = append(lists, l)
		}
	}
	lists = append(lists, resolved)
	all := parity.MergeCorpora(lists...)
	if err := writeCorpusFile(*out, all); err != nil {
		return err
	}
	unPath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".unresolved.jsonl"
	if len(unresolved) > 0 {
		if err := writeCorpusFile(unPath, unresolved); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "файл=%d дуудлага=%d endpoint=%d resolved=%d unresolved=%d\n",
		st.FilesScanned, st.Matches, st.Endpoints, st.Resolved, st.Unresolved)
	fmt.Fprintf(stdout, "корпус (%d мөр, merge-тэй): %s\n", len(all), *out)
	if len(unresolved) > 0 {
		fmt.Fprintf(stdout, "бөглөгдөөгүй: %s (params файлаар бөглөнө)\n", unPath)
	}
	mods := make([]string, 0, len(st.ByModule))
	for m := range st.ByModule {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		fmt.Fprintf(stdout, "  %-20s %d\n", m, st.ByModule[m])
	}
	return nil
}

func writeCorpusFile(path string, reqs []parity.Request) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := parity.WriteCorpus(f, reqs); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
