package cache

import (
	"testing"

	"github.com/khan-vision/khan/events"
)

func TestBumpOnEvents(t *testing.T) {
	c := newCache(t, Config{})
	h := BumpOnEvents(c)
	ver := func(org, res string) string {
		v, _ := c.Version(bg, org, res)
		return gensOf(v)
	}
	if err := h(bg, events.Delivered{OrgID: "o1", Topic: "finance.invoice.posted"}); err != nil {
		t.Fatal(err)
	}
	if ver("o1", "finance.invoice") != "g1" || ver("o2", "finance.invoice") != "g0" {
		t.Fatal("resource = topic - сүүлийн сегмент, зөвхөн тухайн org")
	}
	_ = h(bg, events.Delivered{OrgID: "o1", Topic: "x.created"})
	if ver("o1", "x") != "g1" {
		t.Fatal("2 сегмент")
	}
	logs := 0
	c2 := newCache(t, Config{Logf: func(string, ...any) { logs++ }})
	h2 := BumpOnEvents(c2)
	// цэггүй topic → алгасна (алдаа биш, retry-гүй)
	if err := h2(bg, events.Delivered{OrgID: "o", Topic: "ping"}); err != nil {
		t.Fatal(err)
	}
	if err := h2(bg, events.Delivered{OrgID: "o", Topic: ".x"}); err != nil {
		t.Fatal(err)
	}
	if logs != 2 || c2.Stats().Bumps != 0 {
		t.Fatalf("logs=%d bumps=%d", logs, c2.Stats().Bumps)
	}
	// org-гүй event → _system + _global namespace
	if err := h(bg, events.Delivered{OrgID: "", Topic: "a.b.c"}); err != nil {
		t.Fatal(err)
	}
	if ver("_system", "a.b") != "g1" || ver("_global", "a.b") != "g1" || ver("o1", "a.b") != "g0" {
		t.Fatal("org-гүй event _system/_global-г bump хийх ёстой")
	}
	// Bump алдвал handler алдаа буцаана (Dispatcher дахин оролдоно)
	fs := newFlaky()
	fs.failBump = true
	c3 := newCache(t, Config{L2: fs})
	if err := BumpOnEvents(c3)(bg, events.Delivered{OrgID: "o", Topic: "a.b.c"}); err == nil {
		t.Fatal("bump алдаа буцаах ёстой")
	}
}
