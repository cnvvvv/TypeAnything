package engine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DictEntry is one parsed row of a rime dict .yaml's TSV body.
type DictEntry struct {
	Text   string
	Code   string
	Weight int64
	Stem   string // optional; empty when not present
}

// Dict is a parsed rime dictionary: the YAML frontmatter is dropped, the
// TSV body is decoded into entries. We don't keep the encoder rules —
// the wubi86.dict.yaml encoder rules ("AaAbBaBb" etc.) are for
// auto-encoding new phrases the user types, which is a v2 feature; the
// shipped 137k-line dict already contains every common phrase's code.
type Dict struct {
	Entries []DictEntry

	// ByCode maps an exact code ("aaaa", "ggtt", "g") to the entries
	// that produce it. Lookup walks this map for the exact code, then
	// falls back to code-prefix completions via Prefix.
	ByCode map[string][]DictEntry

	// Prefix is a trie-ish index: for each input prefix, every code
	// that starts with it. Implemented as a simple map because the
	// alphabet is tiny (a-y + z) and total prefixes are bounded.
	// Used to support short-code completion (打不足全码出候选).
	Prefix map[string]map[string]struct{}
}

// LoadDictFile reads a rime dict .yaml from path and returns a parsed Dict.
// The YAML frontmatter is detected by the "---" / "..." markers; everything
// after the closing "..." is treated as TSV (tab-separated, 3 or 4 columns).
// Comment lines starting with '#' (after the frontmatter) are skipped.
func LoadDictFile(path string) (*Dict, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return LoadDict(f)
}

// LoadDict parses a rime dict from r. See LoadDictFile.
func LoadDict(r io.Reader) (*Dict, error) {
	d := &Dict{
		ByCode: make(map[string][]DictEntry),
		Prefix: make(map[string]map[string]struct{}),
	}

	// Phase 1: skip YAML frontmatter delimited by "---" ... "...".
	// Lines before "---" are file comments; the frontmatter between
	// "---" and "..." is the dict header (name/version/sort/columns/
	// encoder). The TSV body starts right after "...".
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024) // long lines possible for phrases

	inFrontmatter := false
	bodyStarted := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimRight(line, "\r")

		if !bodyStarted {
			// Still consuming header.
			if strings.TrimSpace(trimmed) == "---" {
				inFrontmatter = true
				continue
			}
			if inFrontmatter && strings.TrimSpace(trimmed) == "..." {
				bodyStarted = true
				continue
			}
			if !inFrontmatter {
				// pre-frontmatter comments / blank lines
				continue
			}
			// inside frontmatter — skip
			continue
		}

		// Body: skip blank lines and full-line comments.
		if trimmed == "" || strings.HasPrefix(strings.TrimSpace(trimmed), "#") {
			continue
		}

		entry, ok := parseDictLine(trimmed)
		if !ok {
			continue
		}
		d.Entries = append(d.Entries, entry)
		d.ByCode[entry.Code] = append(d.ByCode[entry.Code], entry)
		// Add the full code and each prefix up to len-1 to the prefix
		// index. For code "ggtt" we add "g", "gg", "ggt", "ggtt".
		for i := 1; i <= len(entry.Code); i++ {
			pref := entry.Code[:i]
			set, exists := d.Prefix[pref]
			if !exists {
				set = make(map[string]struct{})
				d.Prefix[pref] = set
			}
			set[entry.Code] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !bodyStarted {
		return nil, errors.New("dict: YAML frontmatter terminator '...' not found")
	}
	if len(d.Entries) == 0 {
		return nil, errors.New("dict: no entries parsed")
	}
	return d, nil
}

// parseDictLine decodes one TSV body line into a DictEntry.
// Expected shapes:
//   text\tcode\tweight        (3 cols)
//   text\tcode\tweight\tstem  (4 cols)
// Lines whose text starts with "#" are alternative codes for the same
// char (e.g. "#子" appears because 子 has codes b AND nb); we strip the
// leading "#" and treat the row as a normal entry.
// Returns ok=false for malformed rows (wrong column count, empty text/code).
func parseDictLine(line string) (DictEntry, bool) {
	var e DictEntry
	// Split on tabs but preserve content exactly. We use SplitN to
	// keep "stem" (the 4th col) free to contain anything; in practice
	// stem is just the root code, no tabs.
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) < 3 {
		return e, false
	}
	text := parts[0]
	if strings.HasPrefix(text, "#") {
		text = strings.TrimPrefix(text, "#")
	}
	code := parts[1]
	if text == "" || code == "" {
		return e, false
	}
	var weight int64
	for _, ch := range parts[2] {
		if ch < '0' || ch > '9' {
			// Non-numeric weight — skip silently. rime dicts always
			// use integer weights, so this guards against corruption.
			weight = 0
			break
		}
		weight = weight*10 + (int64(ch) - '0')
		// Cap to avoid overflow on pathological input. Real weights
		// are < 2.5e9 (fits int32 but we keep int64 headroom).
		if weight > 1<<62 {
			weight = 1 << 62
			break
		}
	}
	e.Text = text
	e.Code = code
	e.Weight = weight
	if len(parts) >= 4 {
		e.Stem = parts[3]
	}
	return e, true
}

// String returns a debug summary. Used by tests; not performance-sensitive.
func (d *Dict) String() string {
	return fmt.Sprintf("Dict{entries=%d codes=%d prefixes=%d}",
		len(d.Entries), len(d.ByCode), len(d.Prefix))
}
