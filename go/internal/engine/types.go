// Package engine implements the input-method engines (wubi, pinyin) that
// the daemon uses to turn raw keypresses into candidates. It is the Go
// replacement for librime's table_translator / script_translator.
//
// Engine implementations live in wubi.go and pinyin.go; this file holds
// the shared types that the dispatcher and the IPC layer depend on.
package engine

// Candidate is one possible interpretation of the user's current input
// buffer. Text is the string to commit if the user picks this candidate
// (e.g. "工" for wubi code "a"). Weight drives ranking — higher first.
type Candidate struct {
	// Text is the hanzi/word to commit.
	Text string

	// Code is the wubi/pinyin code this candidate matches. Equal to the
	// user's input, or a prefix of a longer code (for completion).
	Code string

	// Weight is the original dict weight. The engine adjusts it with
	// user-db frequency before returning candidates.
	Weight int64

	// UserFreq is how often the user picked this candidate (from userdb).
	// 0 means "never picked". Boosts ranking above pure-dict order.
	UserFreq int32

	// IsFullCode is true when Code is exactly 4 chars (a complete wubi
	// 86 code). The engine uses this to surface full-code matches above
	// completions of shorter input.
	IsFullCode bool
}

// Engine turns a stream of input characters into preedit + candidate
// suggestions. Each per-session dispatcher holds one Engine instance.
//
// The interface is intentionally minimal: Feed advances internal state
// with one input character (or backspace via FeedBackspace), Reset
// wipes the composition, and Build returns the current preedit + ranked
// candidates. This keeps the IPC handler trivial and lets us swap engine
// implementations per schema (wubi86 vs pinyin vs future variants).
type Engine interface {
	// Feed appends one input character (a wubi key letter or pinyin
	// letter) to the composition. Returns true if the character was
	// accepted (was a valid input for this engine). The dispatcher
	// should reject the key (return Accepted=false so the host app
	// sees its own keypress) when this returns false.
	Feed(ch byte) bool

	// FeedBackspace removes the last input character. No-op if the
	// composition is empty. Returns true if the composition changed.
	FeedBackspace() bool

	// Reset clears the composition. Called on Escape, focus loss, etc.
	Reset()

	// Build returns the current composition state: the literal input
	// string (for preedit display) and the ranked candidate list.
	// pageSize controls how many candidates the caller wants per page;
	// Build may return more or fewer — paging is the dispatcher's job.
	Build(pageSize int) (input string, cands []Candidate)

	// Select commits the candidate at idx. Returns the text to commit
	// and updates the user-db frequency. Returns "" if idx is out of
	// range (the dispatcher treats that as a no-op).
	Select(idx int) string

	// Input returns the current raw input string without recomputing
	// candidates. Used by the dispatcher for preedit-only updates.
	Input() string

	// SchemaID returns the engine's schema identifier ("wubi86", etc.).
	SchemaID() string
}

// MaxPageSize caps candidate page size. Wubi/pinyin rarely show more
// than 10 per page (1-9 + 0); we allow up to 10 to match traditional
// IME conventions.
const MaxPageSize = 10
