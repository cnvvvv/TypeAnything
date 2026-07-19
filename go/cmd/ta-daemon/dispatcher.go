package main

import (
	"context"
	"log"
	"sync"
	"time"

	"typeanything/internal/config"
	"typeanything/internal/engine"
	"typeanything/internal/ipc"
	"typeanything/internal/translate"
)

// session is the per-app state. A new one is allocated on OpActivate
// and torn down on OpDeactivate. Each session holds its own engine
// instance so two simultaneously-focused apps don't share a composition.
type session struct {
	id       uint32
	engine   engine.Engine
	lastUsed time.Time

	// translationBuf accumulates committed Chinese (from candidate
	// selections or full-code auto-tops) that will be translated on
	// Enter. Guarded by bufMu.
	bufMu         sync.Mutex
	translationBuf string
}

// dispatcher is the per-daemon state that wires IPC requests to the
// engines + translate pipeline. It owns:
//   - one wubi engine per session (each app gets its own composition
//     state — typing in notepad must not interfere with typing in chrome)
//   - a shared user-db (one per schema; typing history is global)
//   - the translate pipeline (shared, async)
//
// The hot-path method is handleKey; everything else is lifecycle glue.
type dispatcher struct {
	cfg      *config.Store
	pipeline *translate.Pipeline
	userdb   *engine.UserDB

	mu       sync.Mutex
	sessions map[uint32]*session
}

// newDispatcher wires up the engines + translate pipeline and prepares
// for per-session bookkeeping. The wubi dict is shared (read-only after
// load); the user-db is shared and mutable (frequency bumps).
func newDispatcher(cfg *config.Store, dict *engine.Dict, rimeDir string, pipeline *translate.Pipeline) (*dispatcher, error) {
	userdb := engine.NewUserDB(rimeDir, "wubi86")
	if err := userdb.Load(); err != nil {
		log.Printf("ta-daemon: userdb load failed (starting empty): %v", err)
	}
	return &dispatcher{
		cfg:      cfg,
		pipeline: pipeline,
		userdb:   userdb,
		sessions: make(map[uint32]*session),
	}, nil
}

// handle is the IPC entry point — one call per request. Called from
// the IPC server's serveConn goroutine; safe to block briefly but
// never for >100ms (we'd stall the shim's key loop).
func (d *dispatcher) handle(ctx context.Context, req ipc.Request) (ipc.Response, error) {
	switch req.Op {
	case ipc.OpPing:
		return ipc.Response{Accepted: true}, nil
	case ipc.OpActivate:
		return d.activate(req)
	case ipc.OpDeactivate:
		return d.deactivate(req)
	case ipc.OpKey:
		return d.handleKey(req)
	case ipc.OpSelectCandidate:
		return d.selectCandidate(req)
	case ipc.OpSwitchSchema:
		return d.switchSchema(req)
	case ipc.OpShutdown:
		return ipc.Response{Accepted: true}, nil
	default:
		return ipc.Response{Error: "unknown op " + req.Op}, nil
	}
}

// activate creates a per-session engine. Defaults to wubi86; the schema
// switch will replace the engine on demand.
func (d *dispatcher) activate(req ipc.Request) (ipc.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions[req.Session] = &session{
		id:       req.Session,
		engine:   d.newEngine("wubi86"),
		lastUsed: time.Now(),
	}
	log.Printf("dispatcher: activate session=%d (total=%d)",
		req.Session, len(d.sessions))
	return ipc.Response{Accepted: true, SchemaID: "wubi86"}, nil
}

// newEngine constructs a fresh engine for the given schema. Currently
// only wubi86 is implemented; pinyin will land in Phase C as part of
// the full settings integration.
func (d *dispatcher) newEngine(schemaID string) engine.Engine {
	switch schemaID {
	case "wubi86", "":
		dict := d.dictFor(schemaID)
		return engine.NewWubi(dict, d.userdb)
	default:
		// Unknown schema → fall back to wubi (better than crashing).
		dict := d.dictFor("wubi86")
		return engine.NewWubi(dict, d.userdb)
	}
}

// dictFor is a placeholder for per-schema dict lookup. Today we only
// load the wubi86 dict (passed into newDispatcher indirectly via the
// engine constructor); future schemas will need their own dicts.
// The method is here so the schema-switch path doesn't need to be
// rewritten when pinyin lands.
func (d *dispatcher) dictFor(_ string) *engine.Dict {
	// The dict is currently stashed on the dispatcher's first session
	// engine; we re-fetch it via the wubi engine. Cleaner plumbing (a
	// dedicated dict registry) is a Phase C refactor — for now we
	// expose the dict through a package-level shim set in main.
	return globalWubiDict
}

// globalWubiDict is set once in main() before the dispatcher starts.
// This is intentionally package-level rather than per-dispatcher
// because the dict is read-only after load and shared by every
// session — no need to copy 3MB around.
var globalWubiDict *engine.Dict

// setGlobalDict is called from main after loading the dict.
func setGlobalDict(d *engine.Dict) { globalWubiDict = d }

// deactivate tears down the session. The user-db is NOT saved here —
// that happens on the periodic flush loop in main, to avoid disk I/O
// on the deactivate hot path.
func (d *dispatcher) deactivate(req ipc.Request) (ipc.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.sessions[req.Session]; ok {
		delete(d.sessions, req.Session)
		log.Printf("dispatcher: deactivate session=%d (total=%d)",
			req.Session, len(d.sessions))
	}
	return ipc.Response{Accepted: true}, nil
}

// handleKey is the hot path. Translates a TSF key event into:
//   - feed the engine (if it's a printable wubi key)
//   - recompute preedit + candidates
//   - if Enter was pressed with accumulated Chinese in the buffer (or
//     a full wubi code was just typed), trigger the translate pipeline
//
// The dispatch logic mirrors the C++ TypeAnythingProcessor::ProcessKeyEvent:
//   - BackSpace outside composition pops the accumulated translation buffer
//   - Escape clears the buffer
//   - Enter triggers DispatchTranslate on the accumulated Chinese
//   - Other keys feed the engine normally
//
// One key difference from the C++: the C++ "accumulated" buffer holds
// COMMITTED text (already inserted by librime into the app); we hold
// the same logical buffer but it lives in the daemon because the Go
// engine is what produced the commits.
func (d *dispatcher) handleKey(req ipc.Request) (ipc.Response, error) {
	// Always ACK key-up so the shim's blocking read completes.
	if req.Release {
		return ipc.Response{Accepted: false}, nil
	}

	sess := d.session(req.Session)
	if sess == nil {
		// No session yet — let the host app handle the key normally.
		return ipc.Response{Accepted: false}, nil
	}

	// The C++ has special-case handling for VK_BACK / VK_ESCAPE /
	// VK_RETURN outside composition. We replicate it here for the
	// translate-buffer; the engine's own composition is handled below.
	switch req.KeyCode {
	case vkReturn:
		return d.handleEnter(sess, req)
	case vkBack:
		// Engine handles its own composition backspace. If the engine
		// is idle, fall through to "not accepted" so the host app
		// deletes the last committed char normally.
		if sess.engine.Input() != "" {
			sess.engine.FeedBackspace()
			return d.buildResponse(sess), nil
		}
		return ipc.Response{Accepted: false}, nil
	case vkEscape:
		if sess.engine.Input() != "" {
			sess.engine.Reset()
			return ipc.Response{Accepted: true}, nil // hide preedit+candidates
		}
		return ipc.Response{Accepted: false}, nil
	}

	// Printable key: feed the engine. If rejected (not a valid wubi
	// key, or composition already at max length), let the host app
	// see the key.
	if req.Char != "" {
		ch := req.Char[0]
		if sess.engine.Feed(ch) {
			resp := d.buildResponse(sess)
			// 86-style auto-top: if the user typed a full 4-letter
			// code and the engine says so, commit the top candidate
			// and clear the composition immediately.
			if w, ok := sess.engine.(*engine.Wubi); ok && w.IsFullCodeInput() {
				_, cands := sess.engine.Build(engine.MaxPageSize)
				if len(cands) > 0 {
					text := sess.engine.Select(0)
					// Push into the translate accumulator; if Enter
					// comes next, this Chinese will be translated.
					d.pushTranslation(sess, text)
					return ipc.Response{Accepted: true, Commit: text}, nil
				}
			}
			return resp, nil
		}
	}
	// Otherwise: not a key the engine handles. Let the host app see it.
	return ipc.Response{Accepted: false}, nil
}

// buildResponse constructs the IPC response from the engine's current
// state: preedit string + candidate list. Called after every Feed.
func (d *dispatcher) buildResponse(sess *session) ipc.Response {
	input, cands := sess.engine.Build(engine.MaxPageSize)
	if input == "" && len(cands) == 0 {
		return ipc.Response{Accepted: true} // hide UI
	}
	resp := ipc.Response{
		Accepted:    true,
		Preedit:     input,
		Candidates:  make([]string, 0, len(cands)),
		CandPageSize: engine.MaxPageSize,
	}
	for _, c := range cands {
		resp.Candidates = append(resp.Candidates, c.Text)
	}
	return resp
}

// selectCandidate commits the user-picked candidate. The shim sends
// this when the user clicks/types a candidate index (1-9, 0, or click).
func (d *dispatcher) selectCandidate(req ipc.Request) (ipc.Response, error) {
	sess := d.session(req.Session)
	if sess == nil {
		return ipc.Response{Error: "no session"}, nil
	}
	text := sess.engine.Select(int(req.Index))
	if text == "" {
		return ipc.Response{Error: "index out of range"}, nil
	}
	// Add to the translate accumulator.
	d.pushTranslation(sess, text)
	// Clear the engine composition; Commit carries the text.
	sess.engine.Reset()
	return ipc.Response{Accepted: true, Commit: text}, nil
}

// switchSchema replaces the session's engine with one for the new schema.
// Any in-progress composition is discarded.
func (d *dispatcher) switchSchema(req ipc.Request) (ipc.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sess, ok := d.sessions[req.Session]
	if !ok {
		return ipc.Response{Error: "no session"}, nil
	}
	sess.engine = d.newEngine(req.SchemaID)
	log.Printf("dispatcher: session=%d switched to %s", req.Session, req.SchemaID)
	return ipc.Response{Accepted: true, SchemaID: req.SchemaID}, nil
}

// handleEnter triggers translation of the accumulated Chinese in the
// translation buffer. Mirrors the C++ ProcessKeyEvent Enter branch:
// if no composition is active and the accumulator is non-empty,
// DispatchTranslate the buffer; otherwise the host app sees Enter.
//
// The accumulator is held in the session via translationBuf (added below)
// rather than as a daemon-wide field — each app's typing is independent.
func (d *dispatcher) handleEnter(sess *session, req ipc.Request) (ipc.Response, error) {
	buf := sess.takeTranslationBuf()
	if buf == "" {
		// No buffered Chinese — let Enter reach the app normally.
		return ipc.Response{Accepted: false}, nil
	}
	// Trigger async translation; the daemon returns immediately so the
	// host app's key loop isn't stalled. The user's Chinese stays
	// visible until the LLM result arrives and is pasted in its place.
	d.pipeline.Dispatch(context.Background(), buf)
	// Consume the Enter so the host app doesn't also receive a newline
	// next to the just-typed Chinese (matches C++ kAccepted).
	return ipc.Response{Accepted: true}, nil
}

// pushTranslation appends committed text to the session's translation
// accumulator. The accumulator holds Chinese the user has typed since
// the last Enter; on Enter it gets sent to the LLM as one batch.
func (d *dispatcher) pushTranslation(sess *session, text string) {
	sess.pushTranslationBuf(text)
}

// flushUserDB saves the user db if dirty. Called periodically and on
// shutdown. Cheap when nothing changed.
func (d *dispatcher) flushUserDB() {
	if err := d.userdb.Save(); err != nil {
		log.Printf("dispatcher: userdb save failed: %v", err)
	}
}

// session returns the session for id, or nil if none (the caller must
// handle nil — typically by returning Accepted=false so the host app
// sees its own keypress).
func (d *dispatcher) session(id uint32) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[id]
	if !ok {
		return nil
	}
	s.lastUsed = time.Now()
	return s
}

// Virtual-key constants used by handleKey. We only need these three;
// the rest of the VK table lives in the C++ shim.
const (
	vkBack   = 0x08
	vkReturn = 0x0D
	vkEscape = 0x1B
)
