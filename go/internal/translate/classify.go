package translate

import (
	"strings"
)

// ParseCategory splits a "X:name" target-language string (as written by
// ta-settings when the user picks a chip) into its category letter and
// the language name proper. Returns category=0 (and lang=raw unchanged)
// when the prefix is absent or malformed, which signals "use the generic
// fallback prompt" to the pipeline — same behaviour as the C++ version.
//
// Examples:
//
//	"A:English"  -> category='A', lang="English"
//	"B:知乎大佬腔" -> category='B', lang="知乎大佬腔"
//	"English"    -> category=0,   lang="English"   (legacy lang.txt)
//	"A:"         -> category=0,   lang="A:"        (malformed → fallback)
//	"X:English"  -> category=0,   lang="X:English" (X not A/B/C/D → fallback)
func ParseCategory(rawLang string) (category byte, lang string) {
	// Match the C++ check exactly: at least 2 chars, [1]==':', [0] in ABCD.
	if len(rawLang) >= 2 && rawLang[1] == ':' {
		c := rawLang[0]
		if c == 'A' || c == 'B' || c == 'C' || c == 'D' {
			return c, rawLang[2:]
		}
	}
	return 0, rawLang
}

// IsNoTranslate reports whether the resolved language string means "user
// toggled pure mode, don't call the LLM". Mirrors the C++ equality chain.
// These literal strings are written by ta-settings' tray menu when the
// user picks "纯净模式".
func IsNoTranslate(lang string) bool {
	switch lang {
	case "off", "Off", "none", "None", "no-translate":
		return true
	}
	return false
}

// StripReasoningBlocks removes `<think>...</think>`, `<thinking>...</thinking>`,
// and triple-backtick code fences from content. This is the classify-step
// post-processing added in commit 13f30a7 to stop the back-scan from
// matching a random capital inside a DeepSeek-R1 / GLM-Think reasoning
// trace.
//
// Edge cases matching the C++ strip_block lambda exactly:
//   - if the open tag has no matching close, the rest of the string is
//     dropped (treating it as "reasoning never closed" — safer than
//     leaving it in).
//   - blocks are removed iteratively from the front so nested or
//     repeated tags all get stripped.
func StripReasoningBlocks(content string) string {
	content = stripBlock(content, "<think>", "</think>")
	content = stripBlock(content, "<thinking>", "</thinking>")
	content = stripBlock(content, "```", "```")
	return content
}

// stripBlock removes every open...close span from s. If an open has no
// close, the remainder of the string from that open is dropped. This
// mirrors the C++ lambda:
//
//	size_t p = 0;
//	while ((p = s.find(open, p)) != npos) {
//	  size_t e = s.find(close, p + open.size());
//	  if (e == npos) { s.erase(p); break; }
//	  s.erase(p, e + close.size() - p);
//	}
func stripBlock(s, open, close string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if j := strings.Index(s[i:], open); j < 0 {
			b.WriteString(s[i:])
			break
		} else {
			// Write up to the open tag.
			b.WriteString(s[i : i+j])
			start := i + j + len(open)
			if k := strings.Index(s[start:], close); k < 0 {
				// No closing tag — drop the rest (C++ erase-to-end behaviour).
				return b.String()
			} else {
				// Skip past the close tag and continue scanning.
				i = start + k + len(close)
			}
		}
	}
	return b.String()
}

// ScanCategoryBackward finds the LAST occurrence of any of A/B/C/D in
// content, scanning from the end. Returns 0 if none found. The C++ uses
// a reverse iterator for the same effect.
//
// Why back-scan: LLMs frequently emit "Category: A" or "The answer is C"
// before the final letter, and earlier forward-scans mis-fired on
// word-leading capitals ("Answer", "Type"). Back-scan picks the final
// letter, which is almost always the actual decision.
func ScanCategoryBackward(content string) byte {
	for i := len(content) - 1; i >= 0; i-- {
		switch content[i] {
		case 'A', 'B', 'C', 'D':
			return content[i]
		}
	}
	return 0
}

// ClassifyContent runs the full post-processing pipeline on a raw
// classify-LLM response body (after JSON content extraction): strip
// reasoning blocks, then back-scan for A/B/C/D. Returns 0 on failure.
func ClassifyContent(content string) byte {
	return ScanCategoryBackward(StripReasoningBlocks(content))
}
