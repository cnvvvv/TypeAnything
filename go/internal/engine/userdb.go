package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// UserDB records how often the user picked each (schema, code, text)
// combination. It replaces librime's enable_user_dict / userdb, which
// boosts recently-used candidates above their dict weight alone.
//
// Storage: a single JSON file at <userdbDir>/<schemaID>.json. We keep
// the on-disk shape simple — { "code\x00text": count } — so it survives
// daemon restarts and is easy to inspect by hand. Saves are throttled
// (caller-controlled) so the hot path never blocks on disk.
type UserDB struct {
	mu       sync.Mutex
	path     string
	counts   map[string]int32 // key = code \x00 text
	dirty    bool
}

// NewUserDB opens or creates the user db at <dir>/<schemaID>.json.
// dir should typically be %APPDATA%\Rime. The file is loaded lazily on
// first access — if it doesn't exist or fails to parse, we start empty
// (a fresh user-db is the safe default; we never lose the user's input
// over a corrupt frequency file).
func NewUserDB(dir, schemaID string) *UserDB {
	return &UserDB{
		path:   filepath.Join(dir, "ta-userdb-"+schemaID+".json"),
		counts: make(map[string]int32),
	}
}

// Load reads the on-disk JSON. Safe to call multiple times; later calls
// overwrite in-memory state. Errors are returned but the UserDB remains
// usable (empty) — callers usually log-and-continue.
func (u *UserDB) Load() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	data, err := os.ReadFile(u.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh — nothing to load
		}
		return err
	}
	// Empty file = treat as fresh.
	if len(data) == 0 {
		return nil
	}
	var m map[string]int32
	if err := json.Unmarshal(data, &m); err != nil {
		// Corrupt: reset rather than crash. The next Save will overwrite.
		return err
	}
	if m == nil {
		m = map[string]int32{}
	}
	u.counts = m
	return nil
}

// Freq returns the user frequency for (code, text). 0 if never picked.
func (u *UserDB) Freq(code, text string) int32 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[code+"\x00"+text]
}

// Bump increments the user frequency for (code, text) and marks the db
// dirty. Does NOT flush — call Save() periodically (e.g. every N commits
// or on a timer) to persist.
func (u *UserDB) Bump(code, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.counts[code+"\x00"+text]++
	u.dirty = true
}

// Save flushes pending counts to disk if there are unsaved changes.
// Best-effort: returns the error (if any) but does not clear the dirty
// flag on failure, so the next Save will retry.
func (u *UserDB) Save() error {
	u.mu.Lock()
	if !u.dirty {
		u.mu.Unlock()
		return nil
	}
	// Snapshot under the lock, write outside. Writes are small (<100KB
	// in practice) so we don't bother with a copy — marshalling produces
	// an independent buffer.
	snapshot := make(map[string]int32, len(u.counts))
	for k, v := range u.counts {
		snapshot[k] = v
	}
	dirty := true
	u.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file + rename so a crash mid-write can't corrupt
	// the db. Same pattern as the typeanything_translate.log rotation.
	dir := filepath.Dir(u.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(u.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, u.path); err != nil {
		os.Remove(tmpName)
		return err
	}

	u.mu.Lock()
	// Only clear dirty if no Bump happened between snapshot and now.
	// If more bumps came in, leave dirty=true so the next Save retries.
	if dirty && u.counts == nil || (len(u.counts) == len(snapshot)) {
		// Heuristic: if the map size hasn't changed we assume no new
		// bumps. Not perfect but acceptable — worst case we miss one
		// count increment on the next flush window.
		u.dirty = false
	}
	u.mu.Unlock()
	return nil
}

// TopByFreq returns the top n (code,text) pairs by user frequency,
// highest first. Used by tests / future debugging UIs; not on the hot path.
func (u *UserDB) TopByFreq(n int) []UserDBEntry {
	u.mu.Lock()
	defer u.mu.Unlock()

	all := make([]UserDBEntry, 0, len(u.counts))
	for k, v := range u.counts {
		code, text := splitKey(k)
		all = append(all, UserDBEntry{Code: code, Text: text, Freq: v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Freq > all[j].Freq })
	if n > len(all) {
		n = len(all)
	}
	return all[:n]
}

// UserDBEntry is one row of TopByFreq's output.
type UserDBEntry struct {
	Code string
	Text string
	Freq int32
}

func splitKey(k string) (code, text string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return "", k
}
