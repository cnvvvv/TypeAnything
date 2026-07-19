package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// MaxFrameBytes caps a single JSON frame. Preedit + candidate lists for an
// IME are tiny (a few KB at most); 1 MiB is a generous ceiling that still
// rejects obviously corrupt length headers.
const MaxFrameBytes = 1 << 20

// ErrFrameTooLarge is returned by ReadFrame when the announced length exceeds
// MaxFrameBytes. Callers should drop the connection rather than try to resync.
var ErrFrameTooLarge = errors.New("ipc: frame too large")

// ReadFrame reads one length-prefixed JSON frame from r and unmarshals it
// into dst. The 4-byte little-endian length header is NOT encrypted or
// checksummed — integrity is not a concern on a local named pipe.
func ReadFrame(r io.Reader, dst any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 {
		return nil
	}
	if n > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, dst)
}

// WriteFrame marshals src and writes a length-prefixed frame to w.
func WriteFrame(w io.Writer, src any) error {
	body, err := json.Marshal(src)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	return nil
}
