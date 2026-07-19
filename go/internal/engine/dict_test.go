package engine

import (
	"strings"
	"testing"
)

// miniDict is a handcrafted rime-dict fragment covering the cases the
// engine must handle:
//   - single-char full codes (aaaa→工)
//   - single-char short codes (g→一)
//   - multi-char phrases (ggtt→五笔)
//   - 3-column vs 4-column rows
//   - the "#text" duplicate marker
const miniDict = `# Rime dictionary: wubi-test
---
name: wubi-test
version: "0.1"
sort: by_weight
columns:
  - text
  - code
  - weight
  - stem
...
工	aaaa	99454797	aa
一	g	2015124793	gg
王	g	2015124792
五	gg	1000
五笔	ggtt	6090000
五笔字型	gtpg	776000
中华人民共和国	kwwl	5000000
`

func mustLoadMini(t *testing.T) *Dict {
	t.Helper()
	d, err := LoadDict(strings.NewReader(miniDict))
	if err != nil {
		t.Fatalf("LoadDict: %v", err)
	}
	return d
}

func TestLoadDict_ParsesAllShape(t *testing.T) {
	d := mustLoadMini(t)
	wantEntries := 7 // 7 data rows; "#王" line is the dup marker, becomes "王"
	if got := len(d.Entries); got != wantEntries {
		t.Fatalf("entries: got %d want %d (Entries=%v)", got, wantEntries, d.Entries)
	}
	// Spot-check the 4-column row kept its stem.
	var foundGong bool
	for _, e := range d.Entries {
		if e.Text == "工" && e.Code == "aaaa" && e.Stem == "aa" {
			foundGong = true
		}
	}
	if !foundGong {
		t.Errorf("工/aaaa/aa not found; entries=%+v", d.Entries)
	}
	// The "#王" line should have had its "#" stripped to text="王".
	var wangCount int
	for _, e := range d.Entries {
		if e.Text == "王" {
			wangCount++
		}
	}
	if wangCount != 1 {
		t.Errorf("王: found %d entries, want 1", wangCount)
	}
}

func TestLoadDict_PrefixIndex(t *testing.T) {
	d := mustLoadMini(t)
	// "g" prefix should cover: g, gg, ggtt, gtpg (all start with g).
	set, ok := d.Prefix["g"]
	if !ok {
		t.Fatal("no prefix index for 'g'")
	}
	for _, want := range []string{"g", "gg", "ggtt", "gtpg"} {
		if _, ok := set[want]; !ok {
			t.Errorf("prefix 'g' missing %q (set=%v)", want, set)
		}
	}
	// "gg" prefix should NOT include "gtpg" (starts "gt", not "gg").
	set2, _ := d.Prefix["gg"]
	if _, bad := set2["gtpg"]; bad {
		t.Errorf("prefix 'gg' incorrectly includes 'gtpg'")
	}
}

func TestWubi_ExactVsCompletion(t *testing.T) {
	d := mustLoadMini(t)
	w := NewWubi(d, nil)

	// Input "g" → exact match "一" (and 王), then completion "五" (gg),
	// "五笔" (ggtt), "五笔字型" (gtpg).
	cands := w.HelpBuild("g")
	if len(cands) == 0 {
		t.Fatal("no candidates for 'g'")
	}
	// The first candidate should be 一 or 王 (both have code "g", high weight).
	first := cands[0].Text
	if first != "一" && first != "王" {
		t.Errorf("first cand for 'g': got %q, want 一/王", first)
	}
	// Ensure completions appear after exacts.
	var sawWuBi bool
	for _, c := range cands {
		if c.Text == "五笔" {
			sawWuBi = true
		}
	}
	if !sawWuBi {
		t.Errorf("'五笔' (ggtt) should appear as a completion of 'g'; cands=%+v", cands)
	}
}

func TestWubi_FullCodeExact(t *testing.T) {
	d := mustLoadMini(t)
	w := NewWubi(d, nil)

	// "aaaa" is the exact full code for 工. Full-code input must return
	// 工 as the first (and only exact) candidate, no completions.
	cands := w.HelpBuild("aaaa")
	if len(cands) == 0 {
		t.Fatal("no candidates for 'aaaa'")
	}
	if cands[0].Text != "工" {
		t.Errorf("'aaaa': first cand = %q, want 工", cands[0].Text)
	}
	// No completion candidates should appear past full-code input.
	for _, c := range cands {
		if !c.IsFullCode {
			t.Errorf("'aaaa': got completion candidate %+v, want exact-only", c)
		}
	}
}

func TestWubi_FeedAndBackspace(t *testing.T) {
	d := mustLoadMini(t)
	w := NewWubi(d, nil)

	if w.Feed('g') != true {
		t.Fatal("Feed('g') rejected")
	}
	if w.Input() != "g" {
		t.Errorf("after Feed('g'), Input=%q want 'g'", w.Input())
	}
	if w.Feed('1') != false {
		t.Error("Feed('1') should be rejected (not a wubi key)")
	}
	if w.IsFullCodeInput() {
		t.Error("single-char input is not full code")
	}

	// Build to 4 chars, check IsFullCodeInput toggles.
	w.Feed('g')
	w.Feed('t')
	w.Feed('t')
	if !w.IsFullCodeInput() {
		t.Errorf("after 'ggtt', IsFullCodeInput=false, want true; input=%q", w.Input())
	}

	// 5th Feed is rejected (max 4).
	if w.Feed('x') {
		t.Error("Feed past 4 chars should be rejected")
	}

	// Backspace peels one char.
	if !w.FeedBackspace() {
		t.Error("FeedBackspace returned false on non-empty input")
	}
	if w.Input() != "ggt" {
		t.Errorf("after one Backspace, Input=%q want 'ggt'", w.Input())
	}

	// Reset clears.
	w.Reset()
	if w.Input() != "" {
		t.Errorf("after Reset, Input=%q want empty", w.Input())
	}

	// Backspace on empty is a no-op (returns false).
	if w.FeedBackspace() {
		t.Error("FeedBackspace on empty input returned true")
	}
}

func TestWubi_UserFreqBoostsRanking(t *testing.T) {
	d := mustLoadMini(t)
	// In-memory userdb; no disk path needed for this test.
	udb := NewUserDB(t.TempDir(), "test")
	w := NewWubi(d, udb)

	// "王" has the same code "g" as "一" but lower weight. Without user
	// frequency, "一" ranks first. Bump "王" a few times and it should
	// overtake.
	before := w.HelpBuild("g")
	if before[0].Text != "一" {
		t.Fatalf("baseline first cand = %q, want 一 (王 has lower weight)", before[0].Text)
	}

	// Boost 王 (code "g", text "王") 5 times.
	for i := 0; i < 5; i++ {
		udb.Bump("g", "王")
	}

	after := w.HelpBuild("g")
	if after[0].Text != "王" {
		t.Errorf("after bumping 王, first cand = %q, want 王 (UserFreq should dominate Weight)", after[0].Text)
	}
}

func TestWubi_SelectReturnsTextAndBumps(t *testing.T) {
	d := mustLoadMini(t)
	udb := NewUserDB(t.TempDir(), "test")
	w := NewWubi(d, udb)

	// Set up "g" input via Feed, then Select(0).
	w.Feed('g')
	got := w.Select(0)
	if got == "" {
		t.Fatal("Select(0) returned empty")
	}
	// Whatever was selected, its freq should now be 1.
	code := "g"
	if udb.Freq(code, got) != 1 {
		t.Errorf("after Select, Freq(g,%q)=%d want 1", got, udb.Freq(code, got))
	}
	// Out-of-range select returns "".
	if w.Select(999) != "" {
		t.Error("Select(999) should return empty")
	}
}
