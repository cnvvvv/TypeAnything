package translate

import "testing"

// TestParseCategory mirrors the exact rules of the C++ DispatchTranslate
// "X:name" prefix parser. Each case must match the C++ behaviour so
// lang.txt written by the existing ta-settings keeps working.
func TestParseCategory(t *testing.T) {
	cases := []struct {
		in       string
		wantCat  byte
		wantLang string
	}{
		{"A:English", 'A', "English"},
		{"B:知乎大佬腔", 'B', "知乎大佬腔"},
		{"C:鲁迅式英语", 'C', "鲁迅式英语"},
		{"D:火星文", 'D', "火星文"},
		// No prefix / legacy lang.txt.
		{"English", 0, "English"},
		// "A:" with empty lang: C++ accepts this (size>=2, [1]==':',
		// [0]=='A') and returns category='A', lang="". Edge case that
		// the production code path doesn't actually emit (ta-settings
		// always writes "A:<name>") but we preserve C++ parity.
		{"A:", 'A', ""},
		{"X:English", 0, "X:English"}, // X not in ABCD → fallback
		{"", 0, ""},
	}
	for _, c := range cases {
		gotCat, gotLang := ParseCategory(c.in)
		if gotCat != c.wantCat || gotLang != c.wantLang {
			t.Errorf("ParseCategory(%q) = (%q, %q); want (%q, %q)",
				c.in, string(gotCat), gotLang, string(c.wantCat), c.wantLang)
		}
	}
}

func TestIsNoTranslate(t *testing.T) {
	for _, s := range []string{"off", "Off", "none", "None", "no-translate"} {
		if !IsNoTranslate(s) {
			t.Errorf("IsNoTranslate(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"English", "A:English", "", "offx"} {
		if IsNoTranslate(s) {
			t.Errorf("IsNoTranslate(%q) = true, want false", s)
		}
	}
}

// TestStripReasoningBlocks covers the three block shapes removed before
// the category back-scan, matching commit 13f30a7's fix in the C++.
func TestStripReasoningBlocks(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "think block removed",
			in:   "let me think <think>I believe A is wrong</think> so the answer is B",
			want: "let me think  so the answer is B",
		},
		{
			name: "thinking tag variant",
			in:   "<thinking>reasoning</thinking>A",
			want: "A",
		},
		{
			name: "code fence removed",
			in:   "```json\n{\"cat\":\"A\"}\n```\nFinal: A",
			want: "\nFinal: A",
		},
		{
			name: "unclosed tag drops rest",
			in:   "answer is A<think>never closed",
			want: "answer is A",
		},
		{
			name: "no blocks untouched",
			in:   "Category: A",
			want: "Category: A",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripReasoningBlocks(c.in)
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// TestScanCategoryBackward verifies the back-scan picks the LAST A/B/C/D,
// not the first — the whole point of the 13f30a7 fix.
func TestScanCategoryBackward(t *testing.T) {
	cases := []struct {
		in   string
		want byte
	}{
		{"Category: A", 'A'},                       // back-scan finds the A in "A"
		{"The answer is B", 'B'},                   // back-scan finds B
		{"I considered A but picked C finally", 'C'}, // LAST letter wins
		{"Type: D", 'D'},
		{"no letter here", 0},
		{"", 0},
	}
	for _, c := range cases {
		got := ScanCategoryBackward(c.in)
		if got != c.want {
			t.Errorf("ScanCategoryBackward(%q) = %q, want %q", c.in, string(got), string(c.want))
		}
	}
}

// TestClassifyContent runs the full classify post-processing (strip + scan).
func TestClassifyContent(t *testing.T) {
	cases := []struct {
		in   string
		want byte
	}{
		{
			// DeepSeek-R1 style: reasoning block + final letter.
			in:   "<think>analyzing... the user said English so it's A</think>A",
			want: 'A',
		},
		{
			// Code-fenced JSON misdirection + final letter.
			in:   "```json\n{\"category\":\"B\"}\n```\nB",
			want: 'B',
		},
		{
			// Plain final letter.
			in:   "D",
			want: 'D',
		},
	}
	for _, c := range cases {
		got := ClassifyContent(c.in)
		if got != c.want {
			t.Errorf("ClassifyContent(%q) = %q, want %q", c.in, string(got), string(c.want))
		}
	}
}

// TestExtractContent verifies both the OpenAI and Anthropic shapes parse,
// matching the C++ ExtractContent which handles both.
func TestExtractContent(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "OpenAI shape",
			body: `{"choices":[{"message":{"content":"Hello, world!"}}]}`,
			want: "Hello, world!",
		},
		{
			name: "Anthropic shape",
			body: `{"content":[{"type":"text","text":"你好"}]}`,
			want: "你好",
		},
		{
			name: "Anthropic with non-text block first",
			body: `{"content":[{"type":"tool_use","text":"ignored"},{"type":"text","text":"real answer"}]}`,
			want: "real answer",
		},
		{
			name: "empty body",
			body: ``,
			want: "",
		},
		{
			name: "malformed json",
			body: `{not json`,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractContent(c.body)
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// TestBuildSystemPrompt verifies the {LANG} substitution and fallback.
func TestBuildSystemPrompt(t *testing.T) {
	// Category-specific template is used as-is with {LANG} substituted.
	got := BuildSystemPrompt('A', "English", "Translate into {LANG}.")
	wantSubstr := "Translate into English."
	if !contains(got, wantSubstr) {
		t.Errorf("category prompt: got %q, want substring %q", got, wantSubstr)
	}

	// Fallback when category == 0 (legacy lang.txt) or template empty.
	got = BuildSystemPrompt(0, "Japanese", "")
	if !contains(got, "idiomatic Japanese") {
		t.Errorf("fallback prompt missing 'idiomatic Japanese': %q", got)
	}
	// Fallback also when category set but template missing.
	got = BuildSystemPrompt('B', "French", "")
	if !contains(got, "idiomatic French") {
		t.Errorf("fallback for B w/o template: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUtf8CodePointCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"你好", 2},                 // 2 hanzi
		{"a你b", 3},                // mixed
		{"😀face", 5},              // emoji is 1 code point
	}
	for _, c := range cases {
		got := utf8CodePointCount(c.in)
		if got != c.want {
			t.Errorf("utf8CodePointCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
