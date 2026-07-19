package engine

import (
	"path/filepath"
	"testing"
)

// realDictPath is the shipped rime-wubi 86 dict in the repo. The test
// exercises the parser + engine end-to-end against 137k real entries,
// which is the data the daemon will load in production.
//
// Skipped automatically when the file isn't present (e.g. when the Go
// module is vendored standalone without the parent repo), so this test
// remains hermetic-friendly.
const realDictPath = `../../../third_party/weasel/librime/plugins/typeanything/schema/wubi86.dict.yaml`

func TestRealWubiDict_LoadsAndKnownCodesResolve(t *testing.T) {
	abs, err := filepath.Abs(realDictPath)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	d, err := LoadDictFile(abs)
	if err != nil {
		t.Skipf("real dict not available (%v) — skipping integration test", err)
	}

	// Sanity: the shipped dict has ~129k entries (per the column-count
	// awk in dev). We allow a wide range because the dict is updated
	// upstream; the floor of 100k catches "parser only read 1 page".
	if got := len(d.Entries); got < 100_000 {
		t.Errorf("real dict parsed only %d entries, want >= 100000", got)
	}
	t.Logf("real dict: %s (%d entries, %d codes, %d prefixes)",
		abs, len(d.Entries), len(d.ByCode), len(d.Prefix))

	// Spot-check well-known 86 codes.
	cases := []struct {
		code string
		text string // expected to be one of the candidates
	}{
		{"aaaa", "工"},
		{"g", "一"},   // single-letter short code
		{"ggtt", "五笔"},
		{"gtpg", "五笔字型"},
		{"kwwl", "中华人民共和国"}, // 4-char phrase
	}
	w := NewWubi(d, nil)
	for _, tc := range cases {
		cands := w.HelpBuild(tc.code)
		if len(cands) == 0 {
			t.Errorf("code %q: no candidates", tc.code)
			continue
		}
		// The expected text must be among the top few candidates.
		// We don't require it to be rank #0 — that depends on the dict's
		// weight field, which we don't control — only that it appears
		// at all (proves the parser found the entry).
		top := cands
		if len(top) > 8 {
			top = top[:8]
		}
		found := false
		for _, c := range top {
			if c.Text == tc.text {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("code %q: %q not in top candidates %v", tc.code, tc.text, top)
		} else {
			t.Logf("  %-6s → found %q (rank candidates: %v)", tc.code, tc.text, top[:min(3, len(top))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
