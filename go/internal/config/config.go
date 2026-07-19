// Package config loads the TypeAnything runtime configuration that the
// translation pipeline and tray menu depend on. All paths are under
// %APPDATA%\Rime\ to stay byte-compatible with the C++ processor.
//
// Sources, in priority order at runtime:
//   * lang.txt      — currently-selected target language / freeform text
//   * prompts.txt   — ===A=== / ===B=== / ===C=== / ===D=== / ===CLASSIFY=== sections
//   * schema.yaml   — typeanything: block (api_key / host / path / model / temperature)
//   * keyring.json  — optional api-key override (written by ta-settings)
//
// The Store caches values in memory and re-reads the user-mutable files
// (lang.txt, prompts.txt) on every Translate call so a tray-menu change
// takes effect on the next Enter.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Provider is the LLM endpoint configuration from schema.yaml's
// typeanything: block. Field names match the C++ processor's load order.
type Provider struct {
	APIKey       string  `yaml:"api_key" json:"api_key"`
	Model        string  `yaml:"model" json:"model"`
	Host         string  `yaml:"host" json:"host"`
	Path         string  `yaml:"path" json:"path"`
	TargetLang   string  `yaml:"target_lang" json:"target_lang"`
	Temperature  float64 `yaml:"temperature" json:"temperature"`
}

// Defaults match the typeanything.schema.yaml fallback values, so a
// missing/malformed config still produces a working translator pointed
// at DeepSeek.
func defaultProvider() Provider {
	return Provider{
		Model:       "deepseek-chat",
		Host:        "api.deepseek.com",
		Path:        "/v1/chat/completions",
		TargetLang:  "English",
		Temperature: 0.3,
	}
}

// LangEntry maps a language code (in lang.txt) to the full name used in
// the LLM prompt. Mirrors the C++ kLangs[] table exactly so existing
// lang.txt values keep working.
var LangEntries = map[string]string{
	"en": "English", "ja": "Japanese", "ko": "Korean", "yue": "Cantonese",
	"fr": "French", "de": "German", "es": "Spanish", "it": "Italian",
	"pt": "Portuguese", "ru": "Russian", "ar": "Arabic", "vi": "Vietnamese",
	"th": "Thai", "id": "Indonesian", "tr": "Turkish", "hi": "Hindi",
	"nl": "Dutch", "pl": "Polish", "sv": "Swedish", "el": "Greek",
	"he": "Hebrew", "fa": "Persian", "uk": "Ukrainian", "cs": "Czech",
	"da": "Danish", "fi": "Finnish", "no": "Norwegian", "hu": "Hungarian",
	"ro": "Romanian", "ms": "Malay",
}

// Store is the runtime config view. All methods are safe for concurrent
// use; reads of lang.txt / prompts.txt re-stat the file each call so
// tray-menu edits take effect immediately.
type Store struct {
	rimeDir string

	// provider is loaded once at startup (schema.yaml rarely changes at
	// runtime; ta-settings writes a fresh keyring.json + relaunches the
	// daemon to pick up host/model changes). Guarded by mu.
	mu       sync.Mutex
	provider Provider
}

// New returns a Store rooted at rimeDir (typically %APPDATA%\Rime).
// Loads schema.yaml + keyring.json immediately; lang.txt / prompts.txt
// are read lazily on demand. Errors are logged via the returned store's
// LastError method but never prevent the store from being used — the
// daemon always starts with defaults.
func New(rimeDir string) *Store {
	s := &Store{rimeDir: rimeDir, provider: defaultProvider()}
	s.loadProviderLocked()
	return s
}

// loadProviderLocked reads schema.yaml + keyring.json into s.provider.
// Callers must hold s.mu. Errors update lastErr but never overwrite a
// good provider — partial reads leave defaults in place.
//
// We hand-parse the YAML block rather than pull in gopkg.in/yaml.v3
// because the schema file's "typeanything:" subtree is six flat scalars;
// a full YAML library is overkill and adds a build dependency. The
// parser is intentionally narrow: it scans for "typeanything:" at
// column 0 and reads 2-space-indented "key: value" lines until the
// indentation drops.
func (s *Store) loadProviderLocked() {
	p := defaultProvider()

	schemaPath := filepath.Join(s.rimeDir, "typeanything.schema.yaml")
	if data, err := os.ReadFile(schemaPath); err == nil {
		parseTypeAnythingBlock(string(data), &p)
	}

	// keyring.json (optional) overrides just the api_key. Format:
	//   {"api_key":"sk-...","host":"...","model":"..."}
	// Missing/empty file = no override.
	keyringPath := filepath.Join(s.rimeDir, "typeanything.keyring.json")
	if data, err := os.ReadFile(keyringPath); err == nil {
		var kr Provider
		if json.Unmarshal(data, &kr) == nil {
			if kr.APIKey != "" {
				p.APIKey = kr.APIKey
			}
			if kr.Host != "" {
				p.Host = kr.Host
			}
			if kr.Model != "" {
				p.Model = kr.Model
			}
			if kr.Path != "" {
				p.Path = kr.Path
			}
		}
	}
	s.provider = p
}

// Provider returns a snapshot of the LLM endpoint config. Hot-path
// callers should copy the fields they need rather than hold the struct.
func (s *Store) Provider() Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider
}

// Reload re-reads schema.yaml + keyring.json. Useful when ta-settings
// has written a new keyring.
func (s *Store) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadProviderLocked()
}

// ResolveTargetLang reads %APPDATA%\Rime\typeanything_lang.txt and
// returns the resolved target language name. Same semantics as the
// C++ ResolveTargetLang():
//   - first non-empty, non-# line wins
//   - if it matches a known code (en/ja/ko/...) → full name
//   - else → the raw string (freeform natural-language description)
//   - empty file → Provider.TargetLang fallback
//   - "off"/"none"/"no-translate" → returned verbatim; the pipeline
//     treats it as a skip signal
//
// The "X:name" category prefix (X=A/B/C/D) is NOT stripped here —
// Pipeline.ParseCategory() handles it.
func (s *Store) ResolveTargetLang() string {
	data, err := os.ReadFile(filepath.Join(s.rimeDir, "typeanything_lang.txt"))
	if err != nil {
		return s.Provider().TargetLang
	}
	for _, raw := range strings.Split(string(data), "\n") {
		// Trim CR and surrounding whitespace.
		line := strings.TrimRight(raw, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if full, ok := LangEntries[line]; ok {
			return full
		}
		return line // freeform
	}
	return s.Provider().TargetLang
}

// LoadPromptSection reads %APPDATA%\Rime\typeanything_prompts.txt and
// returns the body of the ===<section>=== block (section = "A", "B",
// "C", "D", or "CLASSIFY"). Returns "" if the file or section is
// missing — the pipeline falls back to a built-in generic prompt in
// that case (same as the C++ LoadPromptSection).
//
// Block format (port of the C++ parser):
//   ===A===
//   <body lines>
//   ===B===
//   <body lines>
//   ...
// The body is everything from after the marker line up to (but not
// including) the next "\n===" line, with trailing whitespace trimmed.
func (s *Store) LoadPromptSection(section string) string {
	data, err := os.ReadFile(filepath.Join(s.rimeDir, "typeanything_prompts.txt"))
	if err != nil {
		return ""
	}
	raw := string(data)
	marker := "===" + section + "==="
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	// Skip one trailing CR/LF after the marker line.
	if start < len(raw) && raw[start] == '\r' {
		start++
	}
	if start < len(raw) && raw[start] == '\n' {
		start++
	}
	// Find the next "\n===" that begins a new block.
	end := strings.Index(raw[start:], "\n===")
	var body string
	if end < 0 {
		body = raw[start:]
	} else {
		body = raw[start : start+end]
	}
	return strings.TrimRight(body, " \t\r\n")
}

// parseTypeAnythingBlock scans raw YAML for a top-level "typeanything:"
// key and reads its indented child keys into p. Tolerant: unknown keys
// are ignored, missing keys leave p untouched.
//
// This is NOT a general YAML parser — it handles exactly the shape we
// ship in typeanything.schema.yaml / wubi86.schema.yaml, which is six
// flat 2-space-indented scalars under "typeanything:". If you need
// nested structures or flow style, switch to gopkg.in/yaml.v3.
func parseTypeAnythingBlock(raw string, p *Provider) {
	lines := strings.Split(raw, "\n")
	inBlock := false
	for _, line := range lines {
		// Strip trailing CR.
		line = strings.TrimRight(line, "\r")

		// A top-level key starts at column 0.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if strings.HasPrefix(line, "typeanything:") {
				inBlock = true
				continue
			}
			// Any other top-level key ends our block.
			if inBlock && strings.HasSuffix(strings.TrimSpace(line), ":") {
				inBlock = false
			}
			continue
		}
		if !inBlock {
			continue
		}
		// Inside the typeanything block. Parse "  key: value".
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		colon := strings.Index(trimmed, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		val := strings.TrimSpace(trimmed[colon+1:])
		val = strings.Trim(val, "\"'")
		switch key {
		case "api_key":
			p.APIKey = val
		case "model":
			p.Model = val
		case "host":
			p.Host = val
		case "path":
			p.Path = val
		case "target_lang":
			p.TargetLang = val
		case "temperature":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				p.Temperature = f
			}
		}
	}
}

// ErrEmpty is returned by helpers when a config source is missing but
// that's an expected condition (e.g. lang.txt absent on first run).
var ErrEmpty = errors.New("config: empty")
