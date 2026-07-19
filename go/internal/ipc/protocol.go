// Package ipc defines the JSON-RPC contract between the C++ TSF shim
// (WeaselTSFShim.dll, loaded in-process by every text-input app) and the
// Go daemon (ta-daemon.exe), which owns the input engines and translation
// pipeline.
//
// Transport: Windows named pipe at \\.\pipe\typeanything-ime
// Framing:   length-prefixed (4-byte little-endian length header, then JSON body)
// Direction: one request → one response per IPC round-trip. The shim is always
//            the client; the daemon is always the server.
//
// Why length-prefixed instead of pipe message mode: byte-mode pipes are
// simpler to handle from the C++ side with a single ReadFile loop and avoid
// the message-mode quirks across Windows versions. go-winio supports both;
// we pick the one that's hardest to get wrong on the C++ end.
package ipc

// Request is sent by the TSF shim on every key event / lifecycle event.
// Exactly one field is set per request — discriminated by the handler in
// the daemon. Keeping it a flat struct (not an interface) makes the C++
// JSON encoder trivial.
type Request struct {
	// Session identifies the TSF client (each app process has its own
	// shim instance). The daemon keeps per-session engine state
	// (composition buffer, current schema, user dict). 0 = unassigned.
	Session uint32 `json:"session,omitempty"`

	// Op selects the operation. Must be one of the Op* constants below.
	Op string `json:"op"`

	// --- Op = "key" ----------------------------------------------------
	// Keycode is a Windows virtual-key code (VK_*) as passed by TSF's
	// ITfKeyEventSink::OnTestKeyDown / OnKeyDown. Printable ASCII letters
	// arrive as their uppercase ASCII value ('A' = 0x41).
	KeyCode uint32 `json:"keycode,omitempty"`

	// Char is the UTF-8 character produced by the key, when the key is a
	// printable one and the layout has resolved it. Empty for non-printable
	// keys (Enter, Backspace, arrows, etc.). The daemon uses this to feed
	// the wubi/pinyin engine.
	Char string `json:"char,omitempty"`

	// Modifiers is a bitmask: 1=Shift, 2=Ctrl, 4=Alt, 8=Win.
	Modifiers uint32 `json:"mods,omitempty"`

	// Release is true for key-up events. The daemon usually no-ops on
	// release but still ACKs so the shim's blocking read completes.
	Release bool `json:"release,omitempty"`

	// --- Op = "activate" / "deactivate" --------------------------------
	// LangID is the TSF language profile id (TfInputProcessorProfiles),
	// passed on activate so the daemon knows which schema to start with.
	LangID string `json:"lang_id,omitempty"`

	// --- Op = "select_candidate" ---------------------------------------
	// Index is the 0-based candidate index the user picked.
	Index uint32 `json:"index,omitempty"`

	// --- Op = "switch_schema" ------------------------------------------
	// SchemaID selects the input schema: "wubi86" or "pinyin".
	SchemaID string `json:"schema_id,omitempty"`

	// --- Op = "ping" ---------------------------------------------------
	// (no payload)
}

// Response is the daemon's reply. Exactly one action group is populated
// per response; the shim applies them in field order: Preedit → Candidates →
// Commit → ThenRelease.
type Response struct {
	// Accepted = true means the daemon consumed the key (shim should NOT
	// forward it to the app). false means "not mine, let the app handle
	// it normally" (e.g. when in ASCII mode or no composition is active).
	Accepted bool `json:"accepted"`

	// Preedit is the in-place composition string to display in the app's
	// inline preedit area (dashed underline). Empty means "clear preedit".
	Preedit string `json:"preedit,omitempty"`

	// Caret is the byte offset within Preedit where the caret should sit.
	Caret uint32 `json:"caret,omitempty"`

	// Candidates is the list shown in the candidate window. Empty slice
	// means "hide the window".
	Candidates []string `json:"candidates,omitempty"`

	// CandPageInfo, when Candidates is non-empty, reports paging state so
	// the shim can render prev/next indicators. Omitted on hide.
	CandPageSize    uint32 `json:"cand_page_size,omitempty"`
	CandPageNumber  uint32 `json:"cand_page_number,omitempty"`
	CandPageCount   uint32 `json:"cand_page_count,omitempty"`

	// Commit is text to insert into the app immediately via
	// ITfInsertAtSelection::InsertTextAtSelection, then clear preedit.
	// Used when a key directly produces output (full-code wubi auto-top,
	// punctuation in ASCII mode, etc.) or when a candidate is selected.
	Commit string `json:"commit,omitempty"`

	// SchemaID echoes back the active schema after a switch_schema op,
	// or reports the current schema on activate/ping.
	SchemaID string `json:"schema_id,omitempty"`

	// Error, when non-empty, signals a daemon-side error. The shim logs
	// it and treats the key as not-accepted so the user isn't stuck.
	Error string `json:"error,omitempty"`
}

// Operation constants. Strings (not iota ints) so the C++ side doesn't
// need to stay in numeric sync with this file — a typo surfaces immediately
// as "unknown op" rather than silent misbehavior.
const (
	OpPing            = "ping"             // health check; daemon returns Accepted=true
	OpActivate        = "activate"         // TSF activated the IME; start a session
	OpDeactivate      = "deactivate"       // TSF deactivated; release session state
	OpKey             = "key"              // a key event (the hot path)
	OpSelectCandidate = "select_candidate" // user picked candidate #Index
	OpSwitchSchema    = "switch_schema"    // F4 / Ctrl+` schema switch
	OpShutdown        = "shutdown"         // daemon is going down (shim initiated)
)

// PipeName is the well-known path the shim connects to.
// Per-user (no username in the path) because the daemon runs in the user
// session; a single user gets a single pipe. Multi-user-RDP is supported
// because each session has its own namespace for \\.\pipe\<simple-name>.
//
// Note: Weasel uses \\.\pipe\<username>\WeaselNamedPipe to avoid clashes
// across user profiles on the same machine. We keep it simple here since
// the daemon is per-user; revisit if RDP multi-session becomes a target.
const PipeName = `\\.\pipe\typeanything-ime`

// ReadFrame reads a length-prefixed JSON frame from r into dst.
// Exported so tests and a future Go client can share the codec.
//
// We hand-roll the framing instead of pulling jsonrpc2 because the C++
// side needs a trivially simple wire format.
//
// func ReadFrame / WriteFrame live in conn.go to avoid importing io here.
