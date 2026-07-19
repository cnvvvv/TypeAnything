// Package translate ports the C++ translation pipeline (typeanything_processor.cc
// DispatchTranslate) into Go. The pipeline runs ASYNCHRONOUSLY off the IME
// hot path: the dispatcher calls Dispatch() on a separate goroutine, the
// Chinese stays visible in the app until the LLM returns, then we replace
// it in place via clipboard + SendInput.
//
// A request-id version counter drops stale results if the user types
// again before the LLM responds (same pattern as the C++ request_id_
// std::atomic<uint64_t>).
package translate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"typeanything/internal/config"
	"typeanything/internal/logging"
)

// Pipeline is the long-lived translation orchestrator. One instance
// serves all sessions; per-call state lives in the goroutine that
// Dispatch spawns.
type Pipeline struct {
	cfg     *config.Store
	logger  *logging.TranslateLogger
	client  *LLMClient

	// requestID is bumped on every Dispatch call. A worker compares
	// its snapshot of requestID against the current value before
	// applying its result; mismatch means a newer translation is in
	// flight and this one is stale.
	requestID uint64

	// OnMissingAPIKey / OnAuthFailed are called when the daemon should
	// surface the model-config panel (the C++ LaunchTaSettingsModelPage).
	// They run synchronously inside the worker goroutine; wire them to
	// cheap operations (a channel send to the main goroutine).
	OnMissingAPIKey func()
	OnAuthFailed    func()
}

// New constructs a Pipeline. cfg must outlive the Pipeline. logger may
// be nil (logging is silently skipped).
func New(cfg *config.Store, logger *logging.TranslateLogger) *Pipeline {
	return &Pipeline{
		cfg:    cfg,
		logger: logger,
		client: NewLLMClient(),
	}
}

// Dispatch starts an asynchronous translation of chinese. It returns
// immediately (the caller — the IPC dispatcher — never blocks on the
// LLM). The chinese text stays visible in the host app until the worker
// either replaces it or gives up (in which case it's left in place).
//
// The ctx is for the worker goroutine; cancel it to abort an in-flight
// call (used by the daemon's shutdown path).
func (p *Pipeline) Dispatch(ctx context.Context, chinese string) {
	thisID := atomic.AddUint64(&p.requestID, 1)

	// Resolve target + category. Done synchronously so a "off" mode
	// check can short-circuit without ever talking to the network.
	rawLang := p.cfg.ResolveTargetLang()
	if IsNoTranslate(rawLang) {
		// Pure mode — user explicitly disabled translation. No-op.
		return
	}

	category, lang := ParseCategory(rawLang)
	if category != 0 {
		// Category-specific templates come from prompts.txt. Read fresh
		// on every call so prompt iteration doesn't need a daemon restart.
		// (File-read cost is microseconds; we are not on the keystroke
		// hot path here, we're on the Enter-key path.)
		tmpl := p.cfg.LoadPromptSection(string(category))
		go p.run(ctx, thisID, chinese, lang, category, tmpl)
		return
	}
	// Legacy / freeform — no category template; fallback prompt.
	go p.run(ctx, thisID, chinese, lang, 0, "")
}

// run is the worker goroutine. Never panics out — all errors are logged
// and the worker exits quietly so the IME keeps running.
func (p *Pipeline) run(ctx context.Context, id uint64, chinese, lang string, category byte, tmpl string) {
	// Pre-build the log record; we mutate it as we go and log on every
	// exit path.
	prov := p.cfg.Provider()
	rec := logging.TranslateRecord{
		TargetLang: lang,
		Category:   category,
		InputText:  chinese,
		Host:       prov.Host,
		Path:       prov.Path,
		Model:      prov.Model,
	}
	defer func() {
		if p.logger != nil {
			p.logger.Log(rec)
		}
	}()

	// No API key → surface the model-config panel and bail. The user's
	// Chinese is left in place (we never delete what we can't replace).
	if prov.APIKey == "" {
		if p.OnMissingAPIKey != nil {
			p.OnMissingAPIKey()
		}
		rec.Result = "api_key_empty"
		return
	}

	sys := BuildSystemPrompt(category, lang, tmpl)
	req := ChatRequest{
		Model:       prov.Model,
		Temperature: prov.Temperature,
		Messages: []ChatMessage{
			{Role: "system", Content: sys},
			{Role: "user", Content: chinese},
		},
	}

	// Translate config.Provider → translate.Provider. They have the same
	// shape by design; we copy field-by-field to keep the two types
	// decoupled (config doesn't import translate; translate doesn't
	// import config's full surface, only the Store).
	tp := Provider{
		APIKey:      prov.APIKey,
		Model:       prov.Model,
		Host:        prov.Host,
		Path:        prov.Path,
		Temperature: prov.Temperature,
	}
	result := p.client.Call(ctx, tp, req)
	rec.HTTPStatus = result.HTTPStatus
	rec.Response = result.Body
	if result.Err != nil {
		// Transport failure — leave Chinese in place; user can retry.
		log.Printf("translate: llm call failed: %v", result.Err)
		rec.Result = "network_error"
		return
	}

	// Stale check: if another Dispatch came in while we were waiting,
	// drop this result so we don't paste a translation over a buffer
	// the user has already moved past.
	if id != atomic.LoadUint64(&p.requestID) {
		rec.Result = "superseded"
		return
	}

	// 401 / 403 — auth failure. Open the model config panel so the user
	// can fix the key; do NOT paste anything (the model is wrong too).
	if result.HTTPStatus == 401 || result.HTTPStatus == 403 {
		if p.OnAuthFailed != nil {
			p.OnAuthFailed()
		}
		rec.Result = "auth_failed"
		return
	}

	english := strings.TrimSpace(result.Content)
	rec.Output = english
	if english == "" {
		// Empty body / unparseable — leave the Chinese in place so the
		// user knows nothing happened.
		log.Printf("translate: empty content for input %q (status=%d, body bytes=%d)",
			chinese, result.HTTPStatus, len(result.Body))
		rec.Result = "empty-response"
		return
	}

	// Replace: clipboard + backspaces + paste. SendInput goes to the
	// foreground window; if the user alt-tabbed since pressing Enter,
	// we'll paste into the wrong app. The C++ has the same risk and
	// doesn't mitigate; we accept it for parity.
	ok := ReplaceText(chinese, english)
	rec.SendInputOK = ok
	if ok {
		rec.Result = "ok"
	} else {
		rec.Result = "clipboard-fail"
	}
}

// Stats returns counters for diagnostics (tests, future /debug endpoint).
// Not used on the hot path.
type Stats struct {
	mu                sync.Mutex
	Dispatched        uint64
	Completed         uint64
	Failed            uint64
	Superseded        uint64
}

func (s *Stats) IncDispatch() { s.mu.Lock(); s.Dispatched++; s.mu.Unlock() }

func fmtStats(s *Stats) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("dispatched=%d completed=%d failed=%d superseded=%d",
		s.Dispatched, s.Completed, s.Failed, s.Superseded)
}

var _ = fmtStats // kept for future /debug; not currently wired
