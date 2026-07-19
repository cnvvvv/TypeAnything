// TypeAnything TSF Text Service — minimal COM implementation.
// Captures keystrokes, forwards them to the Go daemon over a named pipe,
// and displays preedit/candidates via TSF composition.
// No ATL, no WTL, no Boost — just raw Win32 COM + named pipes.

#pragma once
#include <windows.h>
#include <ole2.h>
#include <msctf.h>
#include <string>
#include <vector>
#include <deque>
#include <cstdio>

// Pipe buffer size (must match go/internal/ipc/conn.go MaxFrameBytes)
constexpr DWORD PIPE_BUF_SIZE = 64 * 1024;

// ─── PipeClient ─────────────────────────────────────────────
class PipeClient {
    HANDLE pipe_ = INVALID_HANDLE_VALUE;
    std::string pipe_name_;
public:
    PipeClient();
    ~PipeClient() { Disconnect(); }
    bool Connect();            // \\.\pipe\typeanything-ime
    void Disconnect();
    bool SendRequest(const char* json_request, int session_id);
    bool ReadResponse(std::vector<char>& out_buf);  // length-prefixed frame
    bool IsConnected() const { return pipe_ != INVALID_HANDLE_VALUE; }
};

// ─── TextService ────────────────────────────────────────────
class TypeAnythingTextService : public ITfTextInputProcessorEx,
                                public ITfKeyEventSink {
    LONG refcount_ = 1;
    HINSTANCE instance_ = nullptr;

    // TSF manager objects (set on ActivateEx, cleared on Deactivate)
    ITfThreadMgr* thread_mgr_ = nullptr;
    TfClientId client_id_ = 0;
    ITfDocumentMgr* document_mgr_ = nullptr;
    ITfContext* input_context_ = nullptr;
    ITfComposition* composition_ = nullptr;

    // Key event sink cookie (for UnadviseKeyEventSink)
    DWORD keyevent_cookie_ = 0;

    // Pipe + session
    PipeClient pipe_;
    int session_id_ = 0;

    // Pending response buffer (daemon's last reply kept for candidate selection)
    bool has_pending_ = false;
    std::string pending_preedit_;
    std::vector<std::string> pending_candidates_;
    std::string pending_commit_;

    // Composition state
    bool composing_ = false;
    std::string preedit_utf8_;   // current preedit string (UTF-8)

public:
    TypeAnythingTextService(HINSTANCE hinst) : instance_(hinst) {}

    // IUnknown
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv) override;
    STDMETHODIMP_(ULONG) AddRef() override;
    STDMETHODIMP_(ULONG) Release() override;

    // ITfTextInputProcessorEx
    STDMETHODIMP ActivateEx(ITfThreadMgr* ptim, TfClientId tid, DWORD flags) override;
    STDMETHODIMP Deactivate() override;
    // ITfTextInputProcessor (not used, ActivateEx takes precedence)
    STDMETHODIMP Activate(ITfThreadMgr*, TfClientId) override { return E_NOTIMPL; }

    // ITfKeyEventSink (processed in order)
    STDMETHODIMP OnSetFocus(BOOL) override { return S_OK; }
    STDMETHODIMP OnTestKeyDown(ITfContext*, WPARAM wParam, LPARAM lParam, BOOL* pfEaten) override;
    STDMETHODIMP OnKeyDown(ITfContext*, WPARAM wParam, LPARAM lParam, BOOL* pfEaten) override;
    STDMETHODIMP OnTestKeyUp(ITfContext*, WPARAM, LPARAM, BOOL*) override { return S_OK; }
    STDMETHODIMP OnKeyUp(ITfContext*, WPARAM, LPARAM, BOOL*) override { return S_OK; }
    STDMETHODIMP OnPreservedKey(ITfContext*, REFGUID, BOOL*) override { return S_OK; }

private:
    // Pipe helpers
    bool EnsurePipeConnected();
    void SendKeyEvent(WPARAM wParam, bool release, BOOL* pfEaten);
    void SendSelectCandidate(int index);
    void HandleDaemonResponse();

    // TSF composition helpers
    void StartComposition(const std::wstring& preedit);
    void UpdateComposition(const std::wstring& preedit);
    void EndComposition(const std::wstring* commit_text = nullptr);
    void CommitText(const std::wstring& text);
    void ClosePendingComposition();
    void ClearCompositionInternal();
    void EnsureInputContext();

    // UTF-8 ↔ UTF-16 conversions
    static std::wstring Utf8ToWide(const std::string& s);
    static std::string WideToUtf8(const wchar_t* s, int len);
    static std::string WideToUtf8(const std::wstring& s);

    // JSON parsing (minimal — enough to parse daemon responses)
    struct JsonValue {
        struct Member { std::string key; std::string val; };
        std::vector<Member> members;
        static JsonValue Parse(const std::string& json);
        std::string Get(const std::string& key) const;
        std::vector<std::string> GetArray(const std::string& key) const;
    };
};

// ─── Helper: UTF-8 <-> UTF-16 ───────────────────────────────
inline std::wstring Utf8ToWide(const std::string& s) {
    if (s.empty()) return L"";
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), nullptr, 0);
    std::wstring w(n, L'\0');
    MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), w.data(), n);
    return w;
}

inline std::string WideToUtf8(const wchar_t* s, int len) {
    if (len <= 0) return "";
    int n = WideCharToMultiByte(CP_UTF8, 0, s, len, nullptr, 0, nullptr, nullptr);
    std::string u(n, '\0');
    WideCharToMultiByte(CP_UTF8, 0, s, len, u.data(), n, nullptr, nullptr);
    return u;
}

inline std::string WideToUtf8(const std::wstring& s) {
    return WideToUtf8(s.data(), (int)s.size());
}

// ─── Helper for factory ─────────────────────────────────────
HRESULT CreateTextService(REFIID riid, void** ppv);
