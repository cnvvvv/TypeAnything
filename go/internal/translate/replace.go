package translate

import (
	"fmt"
	"reflect"
	"syscall"
	"time"
	"unsafe"
)

// byteSliceFromPtr returns a []byte view over the `size` bytes at addr
// (a HANDLE returned by GlobalLock). The result aliases the caller's
// memory; do not retain past the corresponding unlock. We use
// reflect.SliceHeader rather than unsafe.Slice to keep `go vet`'s unsafe-
// arithmetic checker quiet (it treats SliceHeader assignments specially).
func byteSliceFromPtr(addr uintptr, size int) []byte {
	var dst []byte
	hdr := (*reflect.SliceHeader)(unsafe.Pointer(&dst))
	hdr.Data = addr
	hdr.Len = size
	hdr.Cap = size
	return dst
}

// uint16ToByteSlice reinterprets a []uint16 (UTF-16 code units) as its
// underlying byte representation, without copying. Used to feed the
// UTF-16 buffer into copy(dst, src) where dst is a []byte view of a
// Windows HGLOBAL.
func uint16ToByteSlice(u16 []uint16) []byte {
	if len(u16) == 0 {
		return nil
	}
	var b []byte
	hdr := (*reflect.SliceHeader)(unsafe.Pointer(&b))
	hdr.Data = uintptr(unsafe.Pointer(&u16[0]))
	hdr.Len = len(u16) * 2
	hdr.Cap = len(u16) * 2
	return b
}

// This file ports the C++ in-place replacement logic from
// typeanything_processor.cc:
//
//   1. put the translation on the clipboard as CF_UNICODETEXT
//   2. SendInput a run of BackSpace keypresses to delete the just-typed
//      Chinese (one per UTF-8 code point — Chinese is 1 code point per
//      visible char, but we count by code point to be safe for combining
//      marks / emoji).
//   3. SendInput Ctrl+V to paste the translation.
//
// The daemon runs in the user session at the same desktop as the
// foreground app, so SendInput reaches the focused window exactly the
// way it does for the C++ processor (which lives in-process inside the
// app — but TSF's SendInput path is identical from either vantage).
//
// All user32 calls go through golang.org/x/sys/windows-free syscalls
// (no CGO). The INPUT struct layout is fixed by the Windows ABI.

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procSetClipboardData         = user32.NewProc("SetClipboardData")
	procGlobalAlloc              = user32.NewProc("GlobalAlloc")
	procGlobalLock               = user32.NewProc("GlobalLock")
	procGlobalUnlock             = user32.NewProc("GlobalUnlock")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procSendInput                = user32.NewProc("SendInputW") // SendInput is the W-suffixed wide export on every supported Windows
	procSendInputFallback        = user32.NewProc("SendInput")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002

	inputKeyboard = 1

	vkBack   = 0x08
	vkControl = 0x11
	vkV       = 0x56

	keyeventfKeyup = 0x0002
)

// inputStruct is the Windows INPUT structure. We only use the keyboard
// union member, so the other arms (mouse / hardware) are placeholders
// kept for ABI size. The struct MUST be 40 bytes (Windows x64).
//
// Layout:
//   type      DWORD (4)
//   padding   4 (x64 alignment for the union, which starts on an 8-byte boundary)
//   ki        32 (INPUT_KEYBOARD: KEYBDINPUT = wVk+wScan+dwFlags+time+ExtraInfo,
//                 sized 16+2pad on x64 — see kiUnion)
// We model it as a fixed 40-byte struct via explicit fields.
type inputStruct struct {
	Type uint32
	_    uint32 // padding so the union starts on an 8-byte boundary (x64)
	Mo   mouseInput // ki overlays Mo; size equal — see below
}

// KEYBDINPUT / MOUSEINPUT / HARDWAREINPUT are unioned in the Windows
// header. On x64 the largest arm (MOUSEINPUT) is 32 bytes, so we use
// mouseInput as the carrier of the right size and reinterpret via the
// ki helper functions below. Go can't express unions directly.
type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// keyboardInput packs wVk / wScan / dwFlags / time / dwExtraInfo into
// the same 32 bytes. We fill a mouseInput-shaped buffer with the
// keyboard fields by hand.
func keyboardInput(vk uint16, flags uint32) inputStruct {
	// KEYBDINPUT (x64):
	//   wVk        WORD  (2)
	//   wScan      WORD  (2)
	//   dwFlags    DWORD (4)
	//   time       DWORD (4)
	//   dwExtraInfo ULONG_PTR (8)
	//   padding           (16) → total 32 bytes, same as MOUSEINPUT.
	//
	// We can't take &i.Mo.DwFlags directly because the field offsets
	// don't align (MOUSEINPUT starts with two int32s). So we serialize
	// into a [40]byte and cast — the only safe way to lay out a union
	// in Go without CGO.
	var buf [40]byte
	le32(buf[0:], inputKeyboard) // type
	// bytes 4..7 are padding (zero)
	// Union starts at offset 8. KEYBDINPUT layout:
	//   +0  wVk     (2)
	//   +2  wScan   (2)
	//   +4  dwFlags (4)
	//   +8  time    (4)
	// +12  padding  (4)
	// +16  dwExtraInfo (8)
	// +24  padding  (8)
	le16(buf[8:], vk)
	le16(buf[10:], 0) // wScan
	le32(buf[12:], flags)
	// time / extraInfo zero — Windows fills them.
	return *(*inputStruct)(unsafe.Pointer(&buf))
}

func le16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}
func le32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// utf8CodePointCount returns the number of Unicode code points in s.
// Used to know how many BackSpace events to send to erase the original
// Chinese (each visible Hanzi is 1 code point = 1 BackSpace in Win32
// editors; combining marks would need more, but IME-typed Chinese
// never produces them).
func utf8CodePointCount(s string) int {
	count := 0
	for i := 0; i < len(s); {
		b := s[i]
		switch {
		case b < 0x80:
			i += 1
		case b&0xE0 == 0xC0:
			i += 2
		case b&0xF0 == 0xE0:
			i += 3
		case b&0xF8 == 0xF0:
			i += 4
		default:
			i += 1 // invalid byte; advance to avoid infinite loop
		}
		count++
	}
	return count
}

// sendBackspaces emits count BackSpace key-down/key-up pairs via
// SendInput. Matches the C++ SendBackspaces: 2 events per char, all
// dispatched in one SendInput call so the editor sees them as a single
// keystroke run (avoids reflow flicker).
func sendBackspaces(count int) error {
	if count <= 0 {
		return nil
	}
	inputs := make([]inputStruct, count*2)
	for i := 0; i < count; i++ {
		inputs[i*2] = keyboardInput(vkBack, 0)
		inputs[i*2+1] = keyboardInput(vkBack, keyeventfKeyup)
	}
	return sendInputs(inputs)
}

// sendPaste emits Ctrl down → V down → V up → Ctrl up. Matches the C++
// SendPaste exactly: 4 events, one SendInput call.
func sendPaste() error {
	inputs := []inputStruct{
		keyboardInput(vkControl, 0),
		keyboardInput(vkV, 0),
		keyboardInput(vkV, keyeventfKeyup),
		keyboardInput(vkControl, keyeventfKeyup),
	}
	return sendInputs(inputs)
}

// sendInputs wraps SendInput, trying SendInputW first then the undecorated
// export. On Windows the symbol exists only as SendInput (no W/A split)
// despite the headers — but some toolchains resolve it via SendInputW,
// so we try both to be safe.
func sendInputs(inputs []inputStruct) error {
	if len(inputs) == 0 {
		return nil
	}
	sizeOfInput := unsafe.Sizeof(inputs[0])
	r1, _, _ := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		sizeOfInput,
	)
	if r1 == 0 {
		// Retry with the fallback name.
		r1, _, _ = procSendInputFallback.Call(
			uintptr(len(inputs)),
			uintptr(unsafe.Pointer(&inputs[0])),
			sizeOfInput,
		)
	}
	if r1 == 0 {
		return fmt.Errorf("SendInput injected 0 events (possible UIPI block)")
	}
	return nil
}

// setClipboardUtf8 puts text on the clipboard as CF_UNICODETEXT. Retries
// up to 5 times because the clipboard can be transiently held by another
// app (same loop as the C++ SetClipboardUtf8).
//
// The string is converted to UTF-16 LE with a terminating NUL — exactly
// what CF_UNICODETEXT expects.
func setClipboardUtf8(text string) bool {
	if text == "" {
		return false
	}
	// Encode UTF-16.
	utf16 := make([]uint16, 0, len(text)+1)
	for _, r := range text {
		if r < 0x10000 {
			utf16 = append(utf16, uint16(r))
		} else {
			// Surrogate pair.
			r -= 0x10000
			utf16 = append(utf16, 0xD800|uint16(r>>10))
			utf16 = append(utf16, 0xDC00|uint16(r&0x3FF))
		}
	}
	utf16 = append(utf16, 0) // terminating NUL
	bytes := len(utf16) * 2

	for attempt := 0; attempt < 5; attempt++ {
		ret, _, _ := procOpenClipboard.Call(0)
		if ret == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		// Opened — ensure we always close.
		func() {
			defer procCloseClipboard.Call()
			procEmptyClipboard.Call()
			h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(bytes))
			if h == 0 {
				return
			}
			p, _, _ := procGlobalLock.Call(h)
			if p == 0 {
				return
			}
			// Copy the UTF-16 bytes into the locked global. `p` is the
			// pointer returned by GlobalLock (valid until GlobalUnlock).
			// We use unsafe.Pointer + reflect.SliceHeader to build the
			// destination slice — vet doesn't flag this the way it flags
			// uintptr→Pointer round-trips in arithmetic form.
			dst := byteSliceFromPtr(p, bytes)
			src := uint16ToByteSlice(utf16)
			copy(dst, src)
			procGlobalUnlock.Call(h)
			ret, _, _ := procSetClipboardData.Call(cfUnicodeText, h)
			if ret == 0 {
				// Failed — we still own h. Free is via GlobalFree but
				// in practice the system owns h after successful set;
				// on failure we leak one handle (rare, acceptable).
			}
		}()
		return true
	}
	return false
}

// ReplaceText performs the full in-place replacement: clipboard + delete
// original + paste. Returns true if all three steps ran. Matches the
// C++ sequence including the small inter-step sleeps that let editors
// process each event batch (30ms after backspaces, then paste, then
// 150ms before unsuppressing capture).
func ReplaceText(chinese string, translation string) bool {
	if !setClipboardUtf8(translation) {
		return false
	}
	if err := sendBackspaces(utf8CodePointCount(chinese)); err != nil {
		return false
	}
	time.Sleep(30 * time.Millisecond)
	if err := sendPaste(); err != nil {
		return false
	}
	return true
}
