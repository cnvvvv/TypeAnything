# AGENTS.md — TypeAnything

Workspace guide for ZCode agents. Read this before editing this repo.

## What this repo is

**TypeAnything** is a Windows OS-level TSF input method (IME). User types pinyin → presses Enter → an LLM rewrites the committed Chinese into any language/style/voice → the translation replaces the Chinese in-place in any Windows app. It is a **fork of [rime/weasel](https://github.com/rime/weasel)** with a custom librime plugin.

- **Platform:** Windows 10/11 only (TSF). macOS/Linux are explicitly out of scope (would need squirrel / fcitx-rime).
- **Stack:** C++ (MSVC + librime + Boost + WinHTTP + WebView2), PowerShell build scripts, Python eval harness, HTML/CSS/JS for WebView2 UI.
- **Default LLM provider:** DeepSeek, but any OpenAI Chat Completions–compatible endpoint works.

## Major directories

| Path | What |
|---|---|
| `third_party/weasel/` | Forked Weasel TSF framework. **Upstream code retains its own license (GPL v3 / BSD-3-Clause).** Do not relicense. |
| `third_party/weasel/librime/plugins/typeanything/` | **★ The core TypeAnything plugin — the ONLY part of librime tracked in this repo.** Everything else under `librime/` is gitignored. |
| `tools/ta-installer/` | Single-exe installer (UAC, embeds all binaries + schema + UI via `installer.rc`). |
| `tools/ta-settings/` | Standalone WebView2 app for the tray menu's "Switch Language" + "Model Config" panels. |
| `tools/eval/run_eval.py` | Python harness that runs 40 chips × 4 sentences through the 4 category prompts. |
| `docs/PRODUCT.md` | Product positioning + commercial model. **Read before changing monetization/hosting logic.** |
| `docs/v0.9.0-managed-mode.md` | Managed/hosted-mode sprint plan (free trial → subscription). Read before touching API-key/auth flow. |

Files marked ★ in `README.md` "File structure" are TypeAnything's own changes/additions to upstream Weasel.

## Build commands (and a critical gotcha)

**⚠ GOTCHA — hardcoded paths:** The build scripts (`tools/ta-installer/_stage_embed.ps1`, `_build.ps1`, `tools/ta-settings/_build.ps1`) contain hardcoded absolute paths `D:\hrdai\products\TypeAnything\...`. The repo may actually be checked out elsewhere (this workspace is `D:\UGit\TypeAnything`). **Before running any build, update those `$here`/`Set-Location` paths to your real checkout root, or the scripts will fail.**

Build toolchain: VS 2022 BuildTools + Boost 1.84 + xmake + Windows SDK. Toolchain must be MSVC (`cl.exe`) — scripts actively strip MSYS/mingw from PATH.

Build **order** (each step depends on the previous):

```powershell
# 1. librime (with typeanything plugin) → dist\lib\rime.dll
cd third_party\weasel\librime
git submodule update --init --recursive   # one-time
.\build.bat librime

# 2. Weasel binaries (WeaselTSF.dll, WeaselServer.exe, WeaselDeployer.exe)
cd ..\
.\_build_weasel_xmake.ps1    # referenced in README; check third_party/weasel/ for actual script name

# 3. ta-settings.exe (WebView2 settings panel)
cd ..\..\tools\ta-settings
.\_build.ps1                 # → build\windows\x64\release\ta-settings.exe

# 4. ta-installer.exe (stages all embeds, then builds single exe)
cd ..\ta-installer
.\_stage_embed.ps1           # copies all artifacts into embed\
.\_build.ps1                 # → build\windows\x64\release\ta-installer.exe
```

**Eval** (needs a gitignored `config.json` at repo root with `api_key` / `endpoint` / `model` / `temperature`):

```bash
python tools/eval/run_eval.py    # → tools/eval/results.jsonl (gitignored)
```

There is no test suite; `run_eval.py` is the only quality gate for translation prompt changes.

## Architecture boundaries & rules

1. **Only `plugins/typeanything/` is ours under librime.** The `.gitignore` excludes all of `librime/*` except `plugins/typeanything/**`. Do not commit other librime changes here.
2. **Never edit upstream Weasel files without intent** — upstream retains its license. Our changes are the ★-marked files in README's file-structure section.
3. **Backend is a separate private repo** (`typeanything-backend`, Cloudflare Worker). This repo is client-only and MIT-licensed.
4. **Plugin runtime config lives in `%APPDATA%\Rime\`** (user machine), never in-repo: `typeanything_lang.txt` (active target), `typeanything_prompts.txt` (4 category prompts + classify prompt), `typeanything.schema.yaml` (provider config), `typeanything_translate.log`, `typeanything.keyring.json`. These are all gitignored.

## Coding conventions specific to this repo

- **No Chinese string literals in C++ `.cc`/`.cpp` source.** MSVC compiles source as cp936 (GBK) on Chinese Windows, which corrupts embedded UTF-8 Chinese. All Chinese prompt text lives in the runtime `typeanything_prompts.txt` (mirrored in `tools/ta-installer/embed_prompts.txt`) and is loaded at runtime via `LoadPromptSection()`. If you must embed a Chinese literal, use `\xXX` byte escapes (see the fallback translator prompt in `typeanything_processor.cc`).
- **Prompt changes must be mirrored in two places:** `tools/eval/run_eval.py` (`PROMPT_A/B/C/D` constants — the validated source of truth) and `tools/ta-installer/embed_prompts.txt` (what ships to users). The processor reads from the runtime file at `%APPDATA%\Rime\typeanything_prompts.txt`, which is written by the installer from `embed_prompts.txt`. Keep all three consistent.
- **4-category prompt routing (A/B/C/D):** A = natural language translate, B = Chinese sociolect/style rewrite, C = cross-language style (e.g. academic English), D = cipher/encoding. The classify prompt assigns the category at chip-save time; the `X:name` prefix is written to `lang.txt`. See `embed_prompts.txt` `===CLASSIFY===` section for disambiguation rules.
- **Async translation only.** The processor runs on the IME hot path — never block typing. LLM calls go to a detached `std::thread`; clipboard/disk logging is also detached. A `request_id_` version counter drops stale results if the user types again.
- **Never leave the user with lost input.** If no API key / auth fails / empty response → leave original Chinese in place and log (`typeanything_translate.log`), or launch the model-config panel. The processor's `LaunchTaSettingsModelPage()` handles this.

## Windows / IME-specific gotchas

- **`weasel.dll` in `system32` is locked by every text-input process.** Installer uses `MoveFileEx(..., MOVEFILE_DELAY_UNTIL_REBOOT)` as a fallback; new apps pick up the new DLL immediately, already-open apps need restart. A full reboot is not required.
- **WeaselServer runs at MEDIUM integrity level** (`CreateProcessWithTokenW` + explorer token) so the IME works in normal apps — pipe ACLs reject IL mismatches.
- **OpenCC data (`t2s.json` + `TSPhrases.ocd2` + `TSCharacters.ocd2`) must be bundled** — the schema's `simplifier@simplification_filter` depends on it; missing it breaks the entire candidate chain and yields an empty candidate window (issue #1).
- **`weasel.yaml` must be bundled for cold installs** — without it `WeaselDeployer` can't generate `build\weasel.yaml` and the candidate window renders at ~0px on high-DPI (issue #14).
- **WebView2 user-data folder** must be under `%LOCALAPPDATA%\TypeAnything\WebView2`, not Program Files (read-only).
- **JSON parsing in the processor is hand-rolled** (`ExtractContent`) and must handle both OpenAI (`"content":"..."`) and Anthropic (`"content":[{"type":"text","text":"..."}]`) shapes. The classify step strips `<think>`/`<thinking>`/code-fence blocks before scanning the category letter (most recent fix, commit 13f30a7).

## Read before changing sensitive areas

- **Translation/replace chain:** `third_party/weasel/librime/plugins/typeanything/src/typeanything_processor.cc` — README explicitly says read this before sending a PR.
- **Prompts:** `tools/eval/run_eval.py` (source of truth) + `tools/ta-installer/embed_prompts.txt` (shipped).
- **Installer behavior (registry, TSF registration, file replacement):** `tools/ta-installer/main.cpp`.
- **Commercial/hosting decisions:** `docs/PRODUCT.md` §5 (business model), §9 (open/closed split).
