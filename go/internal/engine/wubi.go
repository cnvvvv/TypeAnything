package engine

import (
	"sort"
	"strings"
)

// Wubi is the 86-code-table engine. It is a faithful (if simpler) port of
// librime's table_translator behaviour for the wubi86 schema:
//
//   - Input alphabet is a–y + z (z = 万能/学习键 in 86).
//   - Codes are 1–4 letters. A complete 4-letter code "tops" (auto-commits
//     the highest-ranked candidate) — but for the sidecar version we leave
//     the top-up policy to the dispatcher and just return candidates.
//   - Short input ("g", "gg", "ggt") returns completion candidates whose
//     codes start with the input, ranked by weight + user frequency.
//   - 4-letter input returns exact matches first, then no completions.
//
// Differences from librime (acceptable trade-offs, both noted in the plan):
//   - enable_sentence / 整句 is NOT implemented (v2); single-char + multi-
//     char-phrase candidates only, which covers ~95% of wubi typing.
//   - encode_commit_history (auto-encoding new phrases the user types) is
//     NOT implemented (v2); we only return phrases already in the dict.
type Wubi struct {
	dict   *Dict
	userdb *UserDB

	// input is the current composition (1–4 letters). Empty when idle.
	input []byte
}

// NewWubi builds a wubi engine over dict and userdb. userdb may be nil
// (then UserFreq is always 0 and ranking falls back to dict weight alone).
func NewWubi(dict *Dict, userdb *UserDB) *Wubi {
	return &Wubi{dict: dict, userdb: userdb}
}

// SchemaID implements Engine.
func (w *Wubi) SchemaID() string { return "wubi86" }

// validWubiKey reports whether ch is a wubi-86 input letter (a–z).
// In strict mode z is a wildcard and not a real input; we accept it
// anyway so the user can type z-prefixed sequences — completion will
// simply not match anything in the dict (correct behaviour for z=wildcard).
func validWubiKey(ch byte) bool {
	return ch >= 'a' && ch <= 'z'
}

// Feed implements Engine. Accepts one lowercase a–z letter. Rejects
// everything else (the dispatcher will let the host app handle it).
// Uppercase letters are rejected here; the dispatcher is responsible
// for ASCII-mode handling so the engine sees only lowercase input chars.
//
// The 86 code max length is 4; the 5th letter starts a new composition.
// librime's behaviour on overlong input is to keep buffering (so a fast
// typist can overshoot); we mirror that by capping at 4 — longer inputs
// get no candidates, which matches the "no completion past full code"
// rule below.
func (w *Wubi) Feed(ch byte) bool {
	if !validWubiKey(ch) {
		return false
	}
	if len(w.input) >= 4 {
		// 4 codes already buffered — extra input is a no-op. The
		// dispatcher should normally have auto-committed or cleared
		// by now; we just ignore.
		return false
	}
	w.input = append(w.input, ch)
	return true
}

// FeedBackspace implements Engine.
func (w *Wubi) FeedBackspace() bool {
	if len(w.input) == 0 {
		return false
	}
	w.input = w.input[:len(w.input)-1]
	return true
}

// Reset implements Engine.
func (w *Wubi) Reset() {
	w.input = w.input[:0]
}

// Input implements Engine.
func (w *Wubi) Input() string {
	return string(w.input)
}

// Build implements Engine. Ranking rules (highest first):
//  1. exact 4-code matches, then
//  2. shorter-code matches (when input length < 4): e.g. input "g" matches
//     codes "g" exactly AND codes "gg","gt","ggt","ggtt" by prefix.
//  3. within each tier: user-frequency desc, then dict weight desc, then
//     text asc (stable tiebreak).
//
// pageSize is a hint; we return at most pageSize*2 candidates so the
// dispatcher has enough to fill a second page without a second query,
// but never more.
func (w *Wubi) Build(pageSize int) (string, []Candidate) {
	if len(w.input) == 0 {
		return "", nil
	}
	if pageSize <= 0 {
		pageSize = 5
	}
	code := string(w.input)
	full := len(code) >= 4

	// Collect raw entries: exact-code first, then completions.
	var exact, completions []DictEntry
	if ents, ok := w.dict.ByCode[code]; ok {
		exact = ents
	}
	if !full {
		// Walk the prefix index for codes that start with `code`
		// but are not equal to it.
		if set, ok := w.dict.Prefix[code]; ok {
			for c := range set {
				if c == code {
					continue
				}
				completions = append(completions, w.dict.ByCode[c]...)
			}
		}
	}

	// Convert + rank within each tier.
	exactCands := w.rank(exact, true)
	compCands := w.rank(completions, false)

	// Merge: exact first (already sorted), then completions. Cap total.
	all := append(exactCands, compCands...)
	if len(all) > pageSize*2 {
		all = all[:pageSize*2]
	}
	return code, all
}

// rank sorts a slice of DictEntry into Candidates, applying the user-db
// frequency boost and the within-tier tiebreak. isFullCode marks whether
// these entries' codes are 4 letters (used for the IsFullCode flag and
// for stable display).
func (w *Wubi) rank(ents []DictEntry, isFullCode bool) []Candidate {
	cands := make([]Candidate, 0, len(ents))
	for _, e := range ents {
		c := Candidate{
			Text:       e.Text,
			Code:       e.Code,
			Weight:     e.Weight,
			IsFullCode: isFullCode && len(e.Code) >= 4,
		}
		if w.userdb != nil {
			c.UserFreq = w.userdb.Freq(e.Code, e.Text)
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		// User frequency dominates dict weight: a word the user picks
		// often should always rise to the top, even if its dict weight
		// is low. This is the single most important ranking signal.
		if cands[i].UserFreq != cands[j].UserFreq {
			return cands[i].UserFreq > cands[j].UserFreq
		}
		if cands[i].Weight != cands[j].Weight {
			return cands[i].Weight > cands[j].Weight
		}
		// Stable alphabetic tiebreak: shorter texts (single chars)
		// before longer phrases of equal weight, then by code point.
		if len(cands[i].Text) != len(cands[j].Text) {
			return len(cands[i].Text) < len(cands[j].Text)
		}
		return cands[i].Text < cands[j].Text
	})
	return cands
}

// Select implements Engine. Bumps the user-db frequency for the picked
// candidate and returns its text. Out-of-range index returns "".
func (w *Wubi) Select(idx int) string {
	_, cands := w.Build(MaxPageSize)
	if idx < 0 || idx >= len(cands) {
		return ""
	}
	c := cands[idx]
	if w.userdb != nil {
		w.userdb.Bump(c.Code, c.Text)
	}
	return c.Text
}

// HelpBuild exposed for tests: builds candidates for an arbitrary code
// without going through Feed. Lets us write table-driven tests against
// the dict directly.
func (w *Wubi) HelpBuild(code string) []Candidate {
	w.input = append(w.input[:0], code...)
	defer func() { w.input = w.input[:0] }()
	_, c := w.Build(MaxPageSize)
	return c
}

// IsFullCodeInput reports whether the current input is a complete 4-letter
// wubi code. The dispatcher uses this to auto-commit the top candidate on
// full-code input (the 86-style "顶屏" behaviour).
func (w *Wubi) IsFullCodeInput() bool {
	return len(w.input) == 4
}

// String is a debug helper.
func (w *Wubi) String() string {
	return "Wubi{input=" + strings.ReplaceAll(string(w.input), "\x00", "?") + "}"
}
