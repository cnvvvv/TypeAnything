// TypeAnything TSF shim — minimal, no ATL/WTL/Boost.
// Compiled with mingw-w64 (x86_64-w64-mingw32-g++).
//
// Architecture: forward keystrokes to the Go daemon via a named pipe,
// then insert the daemon's response text (committed characters) into
// the application. No composition/preedit/candidate window in the shim
// — the daemon handles all IME logic and sends "commit" results back.
//
// This keeps the shim to <200 lines of raw Win32 COM.

#include <windows.h>
#include <ole2.h>
#include <msctf.h>
#include <string>
#include <cstdio>

// ─── CLSID ───────────────────────────────────────────────────
static const CLSID CLSID_TypeAnything = {
    0xe5b3c8a1, 0x2d4f, 0x4a6b, {0x9c, 0x3d, 0x7e, 0x8f, 0x1a, 0x2b, 0x4c, 0x6d}};
static const wchar_t* REG_KEY = L"CLSID\\{E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}";

// ─── Class factory ───────────────────────────────────────────
class Factory : public IClassFactory {
    LONG ref_ = 1;
public:
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv) {
        if (riid == IID_IUnknown || riid == IID_IClassFactory)
            { *ppv = static_cast<IClassFactory*>(this); AddRef(); return S_OK; }
        *ppv = nullptr; return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&ref_); }
    STDMETHODIMP_(ULONG) Release() {
        LONG r = InterlockedDecrement(&ref_);
        if (r == 0) { delete this; return 0; }
        return r;
    }
    STDMETHODIMP CreateInstance(IUnknown*, REFIID riid, void** ppv) override;
    STDMETHODIMP LockServer(BOOL) override { return S_OK; }
};

// ─── Pipe client ─────────────────────────────────────────────
class PipeClient {
    HANDLE h_ = INVALID_HANDLE_VALUE;
public:
    PipeClient() = default;
    ~PipeClient() { if (h_ != INVALID_HANDLE_VALUE) CloseHandle(h_); }
    bool Connect() {
        if (h_ != INVALID_HANDLE_VALUE) return true;
        for (int i = 0; i < 8; i++) {
            h_ = CreateFileA("\\\\.\\pipe\\typeanything-ime",
                GENERIC_READ | GENERIC_WRITE, 0, nullptr, OPEN_EXISTING, 0, nullptr);
            if (h_ != INVALID_HANDLE_VALUE) { DWORD m = PIPE_READMODE_MESSAGE; SetNamedPipeHandleState(h_, &m, nullptr, nullptr); return true; }
            Sleep(250 * (i + 1));
        }
        return false;
    }
    bool IsConnected() const { return h_ != INVALID_HANDLE_VALUE; }
    void Close() { if (h_ != INVALID_HANDLE_VALUE) { CloseHandle(h_); h_ = INVALID_HANDLE_VALUE; } }
    bool SendRecv(const std::string& req, std::string& resp);
};

bool PipeClient::SendRecv(const std::string& req, std::string& resp) {
    if (h_ == INVALID_HANDLE_VALUE) return false;
    DWORD len = (DWORD)req.size();
    char hdr[4] = {(char)len, (char)(len>>8), (char)(len>>16), (char)(len>>24)};
    DWORD n = 0;
    if (!WriteFile(h_, hdr, 4, &n, nullptr) || n != 4) return false;
    if (!WriteFile(h_, req.data(), len, &n, nullptr) || n != len) return false;
    FlushFileBuffers(h_);
    char rhdr[4];
    if (!ReadFile(h_, rhdr, 4, &n, nullptr) || n != 4) return false;
    DWORD rlen = (unsigned char)rhdr[0]|((unsigned char)rhdr[1]<<8)|((unsigned char)rhdr[2]<<16)|((unsigned char)rhdr[3]<<24);
    if (rlen == 0 || rlen > 65536) { resp.clear(); return true; }
    resp.resize(rlen);
    DWORD total = 0;
    while (total < rlen) {
        if (!ReadFile(h_, &resp[total], rlen - total, &n, nullptr)) return false;
        total += n;
    }
    return true;
}

// ─── Text Service ────────────────────────────────────────────
class Service : public ITfTextInputProcessor, public ITfKeyEventSink {
    LONG ref_ = 1;
    ITfThreadMgr* tm_ = nullptr;
    TfClientId cid_ = 0;
    PipeClient pc_;
    int session_ = 0;
public:
    // IUnknown
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv) {
        *ppv = nullptr;
        if (riid == IID_IUnknown || riid == IID_ITfTextInputProcessor) *ppv = (ITfTextInputProcessor*)this;
        else if (riid == IID_ITfKeyEventSink) *ppv = (ITfKeyEventSink*)this;
        else return E_NOINTERFACE;
        AddRef(); return S_OK;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&ref_); }
    STDMETHODIMP_(ULONG) Release() { LONG r = InterlockedDecrement(&ref_); if (r==0) delete this; return r; }

    // ITfTextInputProcessor
    STDMETHODIMP Activate(ITfThreadMgr* ptim, TfClientId tid) override {
        tm_ = ptim; tm_->AddRef(); cid_ = tid; session_ = (int)GetCurrentProcessId();
        ITfKeystrokeMgr* k = nullptr;
        if (SUCCEEDED(tm_->QueryInterface(IID_ITfKeystrokeMgr, (void**)&k))) {
            k->AdviseKeyEventSink(cid_, this, TRUE); k->Release();
        }
        if (pc_.Connect()) {
            std::string r = "{\"op\":\"activate\",\"session\":" + std::to_string(session_) + ",\"lang_id\":\"zh-CN\"}";
            std::string _; pc_.SendRecv(r, _);
        }
        return S_OK;
    }
    STDMETHODIMP Deactivate() override {
        if (pc_.IsConnected()) { std::string r = "{\"op\":\"deactivate\",\"session\":" + std::to_string(session_) + "}"; std::string _; pc_.SendRecv(r, _); }
        pc_.Close();
        if (tm_) { ITfKeystrokeMgr* k=nullptr; if (SUCCEEDED(tm_->QueryInterface(IID_ITfKeystrokeMgr,(void**)&k))) { k->UnadviseKeyEventSink(cid_); k->Release(); } }
        if (tm_) { tm_->Release(); tm_=nullptr; }
        return S_OK;
    }

    // ITfKeyEventSink
    STDMETHODIMP OnSetFocus(BOOL) override { return S_OK; }
    STDMETHODIMP OnTestKeyDown(ITfContext*, WPARAM, LPARAM, BOOL* e) override { *e=FALSE; return S_OK; }
    STDMETHODIMP OnKeyDown(ITfContext*, WPARAM w, LPARAM, BOOL* e) override;
    STDMETHODIMP OnTestKeyUp(ITfContext*, WPARAM, LPARAM, BOOL*) override { return S_OK; }
    STDMETHODIMP OnKeyUp(ITfContext*, WPARAM, LPARAM, BOOL*) override { return S_OK; }
    STDMETHODIMP OnPreservedKey(ITfContext*, REFGUID, BOOL*) override { return S_OK; }
};

STDMETHODIMP Service::OnKeyDown(ITfContext*, WPARAM w, LPARAM, BOOL* e) {
    if (!pc_.IsConnected() && !pc_.Connect()) { *e=FALSE; return S_OK; }
    UINT vk = (UINT)w;
    char ch = 0;
    if (vk>='0'&&vk<='9') ch=(char)vk;
    else if (vk>='A'&&vk<='Z') ch=(char)(vk+0x20);
    char json[512];
    sprintf_s(json, sizeof(json), "{\"op\":\"key\",\"session\":%d,\"keycode\":%u,\"char\":\"%c\"}", session_, vk, ch?:' ');
    std::string resp;
    if (!pc_.SendRecv(std::string(json), resp)) { *e=FALSE; return S_OK; }

    // Parse response — minimal JSON scanning
    bool accepted = false;
    std::string commit;
    auto find_val = [&](const std::string& key) -> std::string {
        auto p = resp.find("\""+key+"\"");
        if (p==std::string::npos) return "";
        p=resp.find(':',p+key.size()+2); if(p==std::string::npos) return "";
        p=resp.find('"',p); if(p==std::string::npos) return "";
        auto e2=resp.find('"',p+1); if(e2==std::string::npos) return "";
        return resp.substr(p+1,e2-p-1);
    };
    auto find_bool = [&](const std::string& key) -> bool {
        auto p = resp.find("\""+key+"\"");
        if(p==std::string::npos) return false;
        p=resp.find(':',p+key.size()+2); if(p==std::string::npos) return false;
        p=resp.find_first_of("tf",p+1); if(p==std::string::npos) return false;
        return resp[p]=='t';
    };
    accepted = find_bool("accepted");
    commit = find_val("commit");

    // Insert committed text into the application via TSF
    if (!commit.empty()) {
        ITfDocumentMgr* dm = nullptr;
        ITfContext* ctx = nullptr;
        if (tm_ && SUCCEEDED(tm_->GetFocus(&dm)) && dm &&
            SUCCEEDED(dm->GetTop(&ctx)) && ctx) {
            // Convert UTF-8 commit to UTF-16 for TSF
            std::wstring w;
            int n = MultiByteToWideChar(CP_UTF8,0,commit.data(),(int)commit.size(),nullptr,0);
            if (n>0) { w.resize(n); MultiByteToWideChar(CP_UTF8,0,commit.data(),(int)commit.size(),&w[0],n); }
            // Insert via edit session to get a TfEditCookie
            struct InsertSession : ITfEditSession {
                std::wstring text;
                LONG ref = 1;
                InsertSession(std::wstring&& t) : text(std::move(t)) {}
                STDMETHODIMP QueryInterface(REFIID riid, void** ppv) override {
                    if (riid == IID_IUnknown || riid == IID_ITfEditSession) { *ppv = this; AddRef(); return S_OK; }
                    *ppv = nullptr; return E_NOINTERFACE;
                }
                STDMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&ref); }
                STDMETHODIMP_(ULONG) Release() override { LONG r = InterlockedDecrement(&ref); if (r==0) { delete this; return 0; } return r; }
                STDMETHODIMP DoEditSession(TfEditCookie ec) override {
                    ITfInsertAtSelection* ins = nullptr;
                    if (SUCCEEDED(ec_ctx->QueryInterface(IID_ITfInsertAtSelection, (void**)&ins))) {
                        ITfRange* r = nullptr;
                        ins->InsertTextAtSelection(ec, TF_IAS_NOQUERY, text.data(), (LONG)text.size(), &r);
                        if (r) r->Release();
                        ins->Release();
                    }
                    return S_OK;
                }
                ITfContext* ec_ctx = nullptr;
            } *sess = new InsertSession(std::move(w));
            sess->ec_ctx = ctx; sess->ec_ctx->AddRef();
            HRESULT hr = 0;
            ctx->RequestEditSession(cid_, sess, TF_ES_SYNC | TF_ES_READWRITE, &hr);
            sess->ec_ctx->Release();
            sess->Release();
            ctx->Release();
        }
        if (dm) dm->Release();
        *e = TRUE;
        return S_OK;
    }

    *e = accepted ? TRUE : FALSE;
    return S_OK;
}

// ─── Factory implementation ──────────────────────────────────
STDMETHODIMP Factory::CreateInstance(IUnknown* outer, REFIID riid, void** ppv) {
    if (outer) return CLASS_E_NOAGGREGATION;
    auto* svc = new Service();
    if (!svc) return E_OUTOFMEMORY;
    HRESULT hr = svc->QueryInterface(riid, ppv);
    svc->Release();
    return hr;
}

// ─── DllMain ─────────────────────────────────────────────────
BOOL WINAPI DllMain(HINSTANCE h, DWORD r, LPVOID) {
    if (r == DLL_PROCESS_ATTACH) DisableThreadLibraryCalls(h);
    return TRUE;
}

// ─── COM exports ─────────────────────────────────────────────
extern "C" __declspec(dllexport)
HRESULT WINAPI DllGetClassObject(REFCLSID rclsid, REFIID riid, void** ppv) {
    if (rclsid != CLSID_TypeAnything) return CLASS_E_CLASSNOTAVAILABLE;
    *ppv = nullptr;
    auto* f = new Factory();
    HRESULT hr = f->QueryInterface(riid, ppv);
    f->Release();
    return hr;
}

extern "C" __declspec(dllexport)
HRESULT WINAPI DllCanUnloadNow() { return S_OK; }  // never locked

extern "C" __declspec(dllexport)
HRESULT WINAPI DllRegisterServer() {
    HKEY k = nullptr;
    if (RegCreateKeyEx(HKEY_CLASSES_ROOT, REG_KEY, 0, nullptr, REG_OPTION_NON_VOLATILE, KEY_WRITE, nullptr, &k, nullptr) == ERROR_SUCCESS) {
        RegSetValueExW(k, nullptr, 0, REG_SZ, (const BYTE*)L"TypeAnything", 26);
        RegCloseKey(k);
    }
    // Register as TSF keyboard input processor
    ITfCategoryMgr* m = nullptr;
    if (SUCCEEDED(CoCreateInstance(CLSID_TF_CategoryMgr, nullptr, CLSCTX_INPROC_SERVER, IID_ITfCategoryMgr, (void**)&m))) {
        m->RegisterCategory(CLSID_TypeAnything, GUID_TFCAT_TIP_KEYBOARD, CLSID_TypeAnything);
        m->Release();
    }
    return S_OK;
}

extern "C" __declspec(dllexport)
HRESULT WINAPI DllUnregisterServer() {
    ITfCategoryMgr* m = nullptr;
    if (SUCCEEDED(CoCreateInstance(CLSID_TF_CategoryMgr, nullptr, CLSCTX_INPROC_SERVER, IID_ITfCategoryMgr, (void**)&m))) {
        m->UnregisterCategory(CLSID_TypeAnything, GUID_TFCAT_TIP_KEYBOARD, CLSID_TypeAnything);
        m->Release();
    }
    RegDeleteTreeW(HKEY_CLASSES_ROOT, REG_KEY);
    return S_OK;
}
