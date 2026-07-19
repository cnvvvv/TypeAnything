// Package logging provides a rotating log writer compatible with the
// C++ TypeAnything's typeanything_translate.log format (one record per
// translation, 1 MiB rotate to .1). The daemon also writes a general
// stderr log for non-translate events.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RotatingWriter is a best-effort appending writer that renames its
// output file to <path>.1 once it exceeds MaxBytes. Concurrent writes
// are serialized by mu. Errors are swallowed (the IME hot path MUST
// never trip on a disk problem) but surfaced via the LastErr method
// for diagnostics.
type RotatingWriter struct {
	path     string
	MaxBytes int64

	mu      sync.Mutex
	current *os.File
	size    int64
	lastErr error
}

// NewRotatingWriter opens (appending) path, creating parent dirs as
// needed. If the file already exists, size is initialized to its current
// length so rotation kicks in at the right point.
func NewRotatingWriter(path string, maxBytes int64) *RotatingWriter {
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MiB, matching the C++ translate log
	}
	w := &RotatingWriter{path: path, MaxBytes: maxBytes}
	_ = w.openLocked() // best-effort; writes will retry on first Write
	return w
}

// openLocked must be called with mu held.
func (w *RotatingWriter) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		w.lastErr = err
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		w.lastErr = err
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		w.lastErr = err
		return err
	}
	w.current = f
	w.size = info.Size()
	return nil
}

// Write implements io.Writer. Rotates if the write would push size past
// MaxBytes. Errors are stored but not returned — callers in the hot path
// don't act on them and we don't want a logging failure to abort a
// translation. Use LastErr() to inspect.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.current == nil {
		if err := w.openLocked(); err != nil {
			return 0, nil // swallow; logged via LastErr
		}
	}
	if w.size+int64(len(p)) > w.MaxBytes {
		// Rotate: close current, rename to .1 (overwriting any prior .1),
		// reopen fresh.
		_ = w.current.Close()
		_ = os.Rename(w.path, w.path+".1")
		w.current = nil
		w.size = 0
		if err := w.openLocked(); err != nil {
			return 0, nil
		}
	}
	n, err := w.current.Write(p)
	w.size += int64(n)
	if err != nil {
		w.lastErr = err
		return 0, nil
	}
	return n, nil
}

// LastErr returns the most recent error encountered (or nil). Mostly
// useful in tests / diagnostics; not on the hot path.
func (w *RotatingWriter) LastErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

// Close flushes and closes the underlying file. Safe to call multiple times.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		err := w.current.Close()
		w.current = nil
		return err
	}
	return nil
}

// TranslateLogger writes translation records in the same multi-line
// format as the C++ typeanything_processor.cc TranslateLog(): a
// timestamped TRANSLATE block with target/host/model/category/input/
// http_status/response/parsed/send_input/result lines.
type TranslateLogger struct {
	w io.Writer
}

// NewTranslateLogger wraps a writer (usually a *RotatingWriter).
func NewTranslateLogger(w io.Writer) *TranslateLogger {
	return &TranslateLogger{w: w}
}

// TranslateRecord holds the fields logged per translation. Mirrors the
// C++ TranslateLogRec struct exactly so log analysis tooling keeps working.
type TranslateRecord struct {
	TargetLang  string // resolved language / freeform description
	Category    byte   // 'A'/'B'/'C'/'D' or 0 for legacy/fallback
	InputText   string // raw input (truncated to 50 chars in output)
	Host        string
	Path        string
	Model       string
	HTTPStatus  int
	Response    string // raw body (truncated to 500 chars in output)
	Output      string // extracted translation
	Result      string // "ok" / error message
	SendInputOK bool
}

// Log writes one record. Truncation rules match the C++ version:
//   - target:        50 chars, single-line
//   - input[0..50]:  50 chars, single-line
//   - response[0..500]: 500 chars, single-line
// Newlines within fields are replaced with spaces so each record stays
// parseable with simple line-based tooling.
func (l *TranslateLogger) Log(rec TranslateRecord) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	cat := "-"
	if rec.Category != 0 {
		cat = string(rec.Category)
	}
	fmt.Fprintf(l.w,
		"[%s] TRANSLATE target=\"%s\"\n"+
			"  host=%s path=%s model=%s\n"+
			"  category=%s input[0..50]=\"%s\"\n"+
			"  http_status=%d\n"+
			"  response[0..500]=%s\n"+
			"  parsed=%s\n"+
			"  send_input=%s\n"+
			"  result=%s\n"+
			"---\n",
		ts,
		truncate(oneLine(rec.TargetLang), 50, "..."),
		rec.Host, rec.Path, rec.Model,
		cat,
		truncate(oneLine(rec.InputText), 50, "..."),
		rec.HTTPStatus,
		truncate(oneLine(rec.Response), 500, "...(truncated)"),
		oneLine(rec.Output),
		boolStr(rec.SendInputOK, "ok", "fail"),
		oneLine(rec.Result),
	)
}

func oneLine(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\r' {
			out = append(out, ' ')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}

// truncate cuts s at limit bytes, walking back over any partial UTF-8
// continuation byte so we never emit a broken rune. Appends suffix if cut.
func truncate(s string, limit int, suffix string) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + suffix
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
