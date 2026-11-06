package crudtest

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
)

// recorder — T-ийн хуурамч хэрэгжүүлэлт: алдаануудыг subtest-ийн нэрээр цуглуулна
// (Fatalf/Skipf нь жинхэнэ testing шиг goroutine-ийг зогсооно).
type recorder struct {
	name     string
	root     *recState
	cleanups []func()
	skipped  bool
}

type recState struct {
	mu   sync.Mutex
	errs map[string][]string // subtest нэр → алдаанууд
	skip map[string]bool
}

func newRecorder() *recorder {
	return &recorder{root: &recState{errs: map[string][]string{}, skip: map[string]bool{}}}
}

func (r *recorder) Helper()             {}
func (r *recorder) Logf(string, ...any) {}
func (r *recorder) add(msg string) {
	r.root.mu.Lock()
	r.root.errs[r.name] = append(r.root.errs[r.name], msg)
	r.root.mu.Unlock()
}
func (r *recorder) Errorf(format string, args ...any) { r.add(fmt.Sprintf(format, args...)) }
func (r *recorder) Fatalf(format string, args ...any) {
	r.add(fmt.Sprintf(format, args...))
	runtime.Goexit()
}
func (r *recorder) Skipf(format string, args ...any) {
	r.root.mu.Lock()
	r.root.skip[r.name] = true
	r.root.mu.Unlock()
	runtime.Goexit()
}
func (r *recorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }

func (r *recorder) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

func (r *recorder) Run(name string, fn func(t T)) bool {
	child := &recorder{name: name, root: r.root}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer child.runCleanups()
		fn(child)
	}()
	<-done
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	return len(r.root.errs[name]) == 0
}

// failed — алдаа бүртгэгдсэн subtest-үүд (root-ийн өөрийн алдаа "" түлхүүртэй).
func (r *recorder) failed() map[string][]string {
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	out := map[string][]string{}
	for k, v := range r.root.errs {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

func (r *recorder) summary() string {
	var b strings.Builder
	for k, v := range r.failed() {
		fmt.Fprintf(&b, "[%s]\n  %s\n", k, strings.Join(v, "\n  "))
	}
	return b.String()
}
