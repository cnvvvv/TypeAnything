// TypeAnything TSF shim — minimal, no ATL/WTL/Boost.
// Compiled with mingw-w64 (x86_64-w64-mingw32-g++).

#include <windows.h>
#include <ole2.h>
#include <msctf.h>
#include <string>
#include <vector>
#include <cstdio>

// ─── CLSID: {E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}
// Use a fixed GUID so TSF registration is deterministic.
static const CLSID CLSID_TypeAnythingShim = {
    0xe5b3c8a1, 0x2d4f, 0x4a6b, {0x9c, 0x3d, 0x7e, 0x8f, 0x1a, 0x2b, 0x4c, 0x6d}};

static const TCHAR* REG_KEY_ROOT = TEXT("CLSID\\{E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}");
static const TCHAR* REG_KEY_PROFILE = TEXT("Software\\Microsoft\\CTF\\TIP\\{E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}");

// ─── Class factory ─────────────────────────────────────────
class ClassFactory : public IClassFactory {
    LONG refcount_ = 1;
public:
    // IUnknown
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv) override {
        if (riid == IID_IUnknown || riid == IID_IClassFactory)
            { *ppv = static_cast<IClassFactory*>(this); AddRef(); return S_OK; }
        *ppv = nullptr; return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&refcount_); }
    STDMETHODIMP_(ULONG) Release() override {
        LONG r = InterlockedDecrement(&refcount_);
        if (r == 0) delete this;
        return r;
    }
    // IClassFactory
    STDMETHODIMP CreateInstance(IUnknown* outer, REFIID riid, void** ppv) override;
    STDMETHODIMP LockServer(BOOL) override { return S_OK; }
};

// Forward declaration — TextService defined in tsfshim.cpp
HRESULT CreateTextService(REFIID riid, void** ppv);

STDMETHODIMP ClassFactory::CreateInstance(IUnknown* outer, REFIID riid, void** ppv) {
    if (outer) return CLASS_E_NOAGGREGATION;
    return CreateTextService(riid, ppv);
}

// ─── Module-level state ─────────────────────────────────────
static LONG moduleRefCount = 0;

void ModuleAddRef() { InterlockedIncrement(&moduleRefCount); }
void ModuleRelease() { InterlockedDecrement(&moduleRefCount); }

// ─── DllMain ────────────────────────────────────────────────
BOOL WINAPI DllMain(HINSTANCE hinstDLL, DWORD reason, LPVOID) {
    if (reason == DLL_PROCESS_ATTACH) {
        DisableThreadLibraryCalls(hinstDLL);
    }
    return TRUE;
}

// ─── Standard COM exports ───────────────────────────────────
extern "C" __declspec(dllexport)
HRESULT WINAPI DllGetClassObject(REFCLSID rclsid, REFIID riid, void** ppv) {
    if (rclsid != CLSID_TypeAnythingShim)
        return CLASS_E_CLASSNOTAVAILABLE;
    *ppv = nullptr;
    auto cf = new ClassFactory();
    HRESULT hr = cf->QueryInterface(riid, ppv);
    cf->Release();
    return hr;
}

extern "C" __declspec(dllexport)
HRESULT WINAPI DllCanUnloadNow() {
    return moduleRefCount ? S_FALSE : S_OK;
}

extern "C" __declspec(dllexport)
HRESULT WINAPI DllRegisterServer() {
    HKEY hk;
    LONG r;

    // Register CLSID
    r = RegCreateKeyEx(HKEY_CLASSES_ROOT, REG_KEY_ROOT, 0, nullptr,
                       REG_OPTION_NON_VOLATILE, KEY_WRITE, nullptr, &hk, nullptr);
    if (r != ERROR_SUCCESS) return HRESULT_FROM_WIN32(r);
    RegSetValueEx(hk, nullptr, 0, REG_SZ,
                  (const BYTE*)TEXT("TypeAnything Input"), 18 * sizeof(TCHAR));
    RegCloseKey(hk);

    // Register TSF profile
    TCHAR subkey[512];
    _stprintf(subkey, TEXT("%s\\DisplayDescription"), REG_KEY_PROFILE);
    r = RegCreateKeyEx(HKEY_CURRENT_USER, subkey, 0, nullptr,
                       REG_OPTION_NON_VOLATILE, KEY_WRITE, nullptr, &hk, nullptr);
    if (r == ERROR_SUCCESS) {
        RegSetValueEx(hk, nullptr, 0, REG_SZ,
                      (const BYTE*)TEXT("TypeAnything - 五笔输入 + AI 翻译"), 54 * sizeof(TCHAR));
        RegCloseKey(hk);
    }

    // Notify TSF manager
    ITfInputProcessorProfileMgr* ppmgr = nullptr;
    if (SUCCEEDED(CoCreateInstance(CLSID_TF_InputProcessorProfiles, nullptr,
                                   CLSCTX_INPROC_SERVER, IID_ITfInputProcessorProfileMgr,
                                   (void**)&ppmgr))) {
        ppmgr->RegisterProfile(CLSID_TypeAnythingShim,
                              0x0804, // zh-CN
                              0, // profile GUID (default)
                              L"TypeAnything", (ULONG)wcslen(L"TypeAnything"),
                              L"五笔输入 → AI 翻译英文", (ULONG)wcslen(L"五笔输入 → AI 翻译英文"),
                              0, 0, 0);
        ppmgr->Release();
    }

    return S_OK;
}

extern "C" __declspec(dllexport)
HRESULT WINAPI DllUnregisterServer() {
    ITfInputProcessorProfileMgr* ppmgr = nullptr;
    if (SUCCEEDED(CoCreateInstance(CLSID_TF_InputProcessorProfiles, nullptr,
                                   CLSCTX_INPROC_SERVER, IID_ITfInputProcessorProfileMgr,
                                   (void**)&ppmgr))) {
        ppmgr->UnregisterProfile(CLSID_TypeAnythingShim, 0x0804, 0);
        ppmgr->Release();
    }
    RegDeleteTree(HKEY_CURRENT_USER, REG_KEY_PROFILE);
    RegDeleteTree(HKEY_CLASSES_ROOT, REG_KEY_ROOT);
    return S_OK;
}
