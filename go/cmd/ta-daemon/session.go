package main

import "sync"

// session buffer helpers. The session struct (defined in dispatcher.go)
// has a translationBuf string + bufMu guarding it. These methods are the
// only accessors.
//
// The buffer is separate from the engine's composition state:
//   composition   = the in-progress wubi/pinyin code being typed
//   translationBuf = already-committed Chinese waiting for Enter to
//                    trigger translation
// Mirrors the C++ accumulated_ std::string in typeanything_processor.cc.

// pushTranslationBuf appends text. Called from the IPC goroutine after a
// candidate is selected or auto-committed.
func (s *session) pushTranslationBuf(text string) {
	if text == "" {
		return
	}
	s.bufMu.Lock()
	s.translationBuf += text
	s.bufMu.Unlock()
}

// takeTranslationBuf returns the accumulated text and resets the buffer.
// Called on Enter.
func (s *session) takeTranslationBuf() string {
	s.bufMu.Lock()
	out := s.translationBuf
	s.translationBuf = ""
	s.bufMu.Unlock()
	return out
}

// Compile-time assertion that sync is used (so the import isn't dropped
// if we ever inline these back into dispatcher.go).
var _ = sync.Mutex{}
