# TypeAnything 重构计划：Go sidecar + 极小 TSF shim DLL

## 背景与事实校正（先对齐认知）

1. **五笔输入已经在 commit `b04a420` 完整实现并接进翻译链路**。方案 `wubi86.schema.yaml` + `wubi86.dict.yaml`（136,981 行）已就绪，安装器已接好。所以"增加五笔"这部分无需重做——重构后只需把这套五笔引擎用 Go 重新实现即可（因为我们要彻底去 librime）。
2. **代码库里没有 Rust**（`git ls-files '*.rs'` 全空）。"避免 Rust"约束天然满足。真正的痛点是 MSVC + Boost + librime。
3. **Weasel 自己已经是"薄 TSF shim + sidecar 进程"架构**：`WeaselTSF.dll`（in-process COM）通过 `\\.\pipe\<user>\WeaselNamedPipe` 跟 `WeaselServer.exe`（librime）通信。我们要做的是**换 sidecar 的语言**，不是发明新架构。
4. **所有 Go 依赖都是纯 Go、无 CGO**：`microsoft/go-winio`（命名管道）、`jchv/go-webview2`（设置面板）、`mozillazg/go-pinyin`。`go build` 产出单文件 exe，**用户机器零 gcc/MSVC**。
5. **唯一必须保留 MSVC 的部分**：~300 行 C++ 的 TSF shim DLL（导出 `DllGetClassObject` / `DllRegisterServer`，实现 `ITfTextInputProcessorEx`）。Windows 系统级硬约束，无法消除。处理方式见下文"MSVC 处理策略"。

## 目标架构

```
┌─────────────────────────────────────────────────────────────┐
│ WeaselTSFShim.dll   ← 唯一的 C++（~300 行，预编译签入仓库）  │
│  - 实现 ITfTextInputProcessorEx + ITfKeyEventSink           │
│  - 捕获按键 → 命名管道 → Go daemon                           │
│  - 把 Go 返回的候选/提交 通过 TSF COM 写进应用               │
│  - 翻译后替换：daemon 调 SendInput(BackSpace)+Ctrl+V         │
│  (从 Weasel 现有 PipeChannel.cpp 移植 retry 循环 + IL 处理) │
└──────────────┬──────────────────────────────────────────────┘
               │ \\.\pipe\typeanything-ime  (JSON-RPC, go-winio)
┌──────────────▼──────────────────────────────────────────────┐
│ ta-daemon.exe   (Go，业务逻辑全部在这里，CGO_ENABLED=0)      │
│  - wubi86 引擎：解析 wubi86.dict.yaml → trie → 候选生成      │
│  - 拼音引擎：mozillazg/go-pinyin + 自建频度表（可选）        │
│  - 候选排序、userdb 频次累计                                 │
│  - 翻译链路（移植 typeanything_processor.cc 全部逻辑）       │
│      * ResolveTargetLang / LoadPromptSection / 4 类 A/B/C/D  │
│      * WinHTTP → net/http                                   │
│      * ExtractContent → encoding/json                       │
│      * 剪贴板 + SendInput 替换（用 golang.org/x/sys/windows）│
│  - 配置：lang.txt / prompts.txt / schema.yaml / keyring.json │
│  - 日志：typeanything_translate.log（1MB 轮转）              │
│  - HTTP server：localhost 端口给 ta-settings UI 调用         │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ ta-settings.exe (Go + jchv/go-webview2，替换 C++ 宿主)       │
│  - 托盘菜单 + 切换语言 + 模型配置面板                        │
│  - 复用现有 HTML/CSS/JS（tools/ta-settings/ui/*）            │
│  - 通过 localhost HTTP 与 daemon 通信                        │
└─────────────────────────────────────────────────────────────┘
```

## 目录结构（新增 Go 项目，不破坏现有 C++ 代码）

```
D:\UGit\TypeAnything\
├── go/                          ★ 新增：所有 Go 代码
│   ├── go.mod                   (module typeanything, go 1.22+)
│   ├── cmd/
│   │   ├── ta-daemon/main.go    (守护进程入口)
│   │   └── ta-settings/main.go  (设置面板入口)
│   ├── internal/
│   │   ├── ipc/                 (命名管道 JSON-RPC server/client)
│   │   ├── engine/
│   │   │   ├── wubi.go          (五笔引擎 + dict.yaml 解析)
│   │   │   ├── pinyin.go        (拼音引擎包装)
│   │   │   └── types.go         (Candidate / Session 抽象)
│   │   ├── translate/           (移植 processor.cc 翻译逻辑)
│   │   │   ├── pipeline.go      (主流程)
│   │   │   ├── prompts.go       (LoadPromptSection + 4 类路由)
│   │   │   ├── llm.go           (net/http 调 OpenAI/Anthropic)
│   │   │   ├── classify.go      (移植 ===CLASSIFY=== 逻辑)
│   │   │   └── replace.go       (剪贴板 + SendInput 替换)
│   │   ├── config/              (lang.txt / schema.yaml / keyring)
│   │   ├── logging/             (1MB 轮转日志)
│   │   └── settingsapi/         (HTTP API 给 ta-settings 调)
│   └── resources/
│       ├── wubi86.dict.yaml     (从 third_party/.../schema/ 复制)
│       ├── embed_prompts.txt    (从 tools/ta-installer/ 复制)
│       └── ui/                  (复用 tools/ta-settings/ui/*)
├── shim/                        ★ 新增：~300 行 C++ TSF shim
│   ├── TsfShim.cpp              (ITfTextInputProcessorEx)
│   ├── KeyEventSink.cpp         (按键捕获)
│   ├── PipeClient.cpp           (命名管道客户端, 移植自 Weasel)
│   ├── dllmain.cpp              (DllGetClassObject 等)
│   ├── TsfShim.def              (导出表)
│   ├── xmake.lua                (构建配置)
│   └── README.md                (如何重编: 只在改 shim 时需要 MSVC)
├── shim-prebuilt/               ★ 预编译产物签入 (x64 release)
│   └── WeaselTSFShim.dll        (release 构建二进制)
└── (现有 third_party/weasel/ 等保持不动)
```

## 实施步骤（10 步，每步可独立验证）

### Phase A：Go daemon 骨架（无需 MSVC）
1. **建 Go 项目 + IPC 框架**
   - `go.mod`、`cmd/ta-daemon/main.go`、`internal/ipc/`
   - 用 `microsoft/go-winio` 起 `\\.\pipe\typeanything-ime` server
   - 定义 JSON-RPC 协议（`KeyEvent` / `Candidates` / `Commit` / `TranslateRequest`）
   - SDDL 配置允许 MEDIUM IL 客户端连接（移植 Weasel 的 IL 处理）
   - *验证*：用 Go 写测试客户端连上去能 echo。

2. **五笔引擎（核心需求 1）**
   - `internal/engine/wubi.go`：启动时解析 `wubi86.dict.yaml`（136k 行），构建 `map[code][]Candidate` + 前缀索引
   - 实现 `Lookup(code string) []Candidate`：支持简码补全、4 码顶屏、词组
   - userdb 频次累计（替代 librime `enable_user_dict`），存 `%APPDATA%\Rime\ta-userdb.json`
   - *验证*：单元测试覆盖 `aaaa→工`、`gtht→五笔字型`、简码、词组。

3. **翻译链路移植（核心需求 1 的英文输出）**
   - `internal/translate/pipeline.go`：完整移植 `typeanything_processor.cc` 的 `DispatchTranslate` 流程
   - `prompts.go`：`LoadPromptSection` 解析 `%APPDATA%\Rime\typeanything_prompts.txt`
   - `llm.go`：`net/http` POST + `encoding/json` 解析（取代 WinHTTP + 手写 ExtractContent）
   - `classify.go`：移植 `===CLASSIFY===` 逻辑，包括 commit `13f30a7` 的 `<think>`/代码块剥离
   - `replace.go`：剪贴板 + SendInput（用 `golang.org/x/sys/windows` 调 `user32.SendInput`）
   - 版本计数器 `request_id_` 防陈旧结果
   - *验证*：跑现有 `tools/eval/run_eval.py`（40 chips × 4 句 × 4 类），结果对标当前 C++ 版。

### Phase B：TSF shim DLL（需要 MSVC，但仅此一次）
4. **写 ~300 行 C++ shim**
   - 以 `nathancorvussolis/tsf-sample-ime` 为模板
   - 实现 `ITfTextInputProcessorEx::ActivateEx` / `ITfKeyEventSink`
   - 每个按键：通过命名管道发给 daemon，阻塞读响应（带 50ms 超时）
   - 收到 `Commit` 指令：调用 TSF `ITfInsertAtSelection::InsertTextAtSelection` 把字写到应用
   - 收到 `ShowCandidates`：调用现有候选窗代码（可继续用 Weasel 的 CandidateList.cpp，或最简化）
   - 候选窗选词、翻页、F4 切方案 → 走同样的 IPC
   - *验证*：编译通过、注册进 TSF、按键事件能打到 daemon 日志。

5. **预编译 + 签入二进制**
   - `xmake f -p windows -a x64 -m release && xmake build` 产出 `WeaselTSFShim.dll`
   - 拷到 `shim-prebuilt/WeaselTSFShim.dll` 并 commit
   - 写 `shim/README.md`：明确"普通开发不需要重编此 DLL；仅当修改 shim/ 下 C++ 时需要 VS BuildTools"

### Phase C：设置面板 + 集成
6. **ta-settings Go 重写**
   - `cmd/ta-settings/main.go`：`jchv/go-webview2` 加载 `resources/ui/index.html`
   - 移植 `tools/ta-settings/main.cpp` 的托盘菜单 + WebView2 宿主逻辑
   - 通过 `localhost:17890` HTTP API 与 daemon 交互（替换语言、改模型、看日志）
   - 复用现有 `tools/ta-settings/ui/{index.html,app.js,style.css}` —— HTML/JS 一行不改
   - *验证*：托盘菜单能切语言、daemon 端 lang.txt 同步更新。

7. **配置加载整合**
   - daemon 启动时读 `%APPDATA%\Rime\typeanything.schema.yaml` 的 `typeanything:` 块（用 `gopkg.in/yaml.v3`）
   - 兼容现有 keyring.json 格式
   - *验证*：配置缺失/错误时不崩，fallback 到 deepseek 默认值。

### Phase D：构建系统 + 分发
8. **Go 构建脚本**
   - `go/build.ps1`：`$env:CGO_ENABLED=0; go build -ldflags="-H windowsgui -s -w" ./cmd/ta-daemon; go build ... ./cmd/ta-settings`
   - 产出 `go/bin/ta-daemon.exe` + `ta-settings.exe`（单文件，无运行时依赖）
   - *验证*：在干净 Win10/11（无 Go 之外的工具链）上 `build.ps1` 能跑通。

9. **重写 ta-installer（C++ 不变，但 embed 换成 Go 产物）**
   - `tools/ta-installer/_stage_embed.ps1` 改为 stage：`WeaselTSFShim.dll`（prebuilt）+ `ta-daemon.exe` + `ta-settings.exe` + `wubi86.dict.yaml` + `embed_prompts.txt`
   - `main.cpp` 的安装步骤调整：注册 shim DLL 到 TSF、launch daemon、写 `Run` 注册表项
   - 不再 embed `WeaselServer.exe` / `WeaselDeployer.exe` / `rime.dll` / Boost 相关
   - *验证*：装到干净 VM，五笔能输入、Enter 能触发翻译。

10. **文档 + AGENTS.md 更新**
    - 更新 `AGENTS.md`：新的构建命令、shim 重编条件、Go 依赖列表
    - 写 `docs/architecture-v2.md`：新架构图、IPC 协议、迁移指南
    - 更新 `README.md` file-structure 表

## MSVC 处理策略（回应你的核心顾虑）

| 场景 | 是否需要 MSVC |
|---|---|
| 日常 Go 开发（五笔引擎、翻译、UI、配置） | ❌ 不需要 |
| `go build` 产出 daemon / settings exe | ❌ 不需要 |
| 用户安装使用 | ❌ 不需要 |
| 改 300 行 C++ shim DLL（极少发生） | ✅ 需要（VS BuildTools） |
| CI 自动重编 shim DLL | ✅ 需要（在 GitHub Actions windows-runner 上） |

**你本机的日常开发流程是 100% 无 MSVC 的。** shim DLL 作为预编译二进制签入仓库，等同于"第三方依赖"——你只在改它时才需要装 MSVC，而那块代码（TSF COM 接口）极度稳定。

## 关键风险与对策

1. **MEDIUM IL 管道 ACL**：Weasel 当前用 `CreateProcessWithTokenW`+explorer token 解决。Go daemon 复制同样启动方式（用 `golang.org/x/sys/windows` 调同等 API），或用显式 SDDL `D:P(A;;GA;;;WD)(A;;GA;;;AC)` 开放给 medium-IL。预算半天调试。
2. **按键延迟**：每次按键跨进程。Weasel 已证实在 `\\.\pipe\WeaselNamedPipe` 上延迟可接受（<5ms）。Go daemon 用 `MessageMode=true` 帧边界、连接池常驻、shim 端缓存上一次候选窗隐藏延迟。
3. **五笔引擎的"整句/词组"**：librime 的 `enable_sentence` / `encode_commit_history` 要在 Go 里重新实现。第一版可以只做单字 + 4 码词组（覆盖 95% 五笔用户），整句作为 v2。
4. **OpenCC 繁简转换**：当前 schema 用 `simplifier@simplification_filter`。Go 版用 `golang.org/x/text/unicode/norm` + OpenCC 字典（复用现有 `t2s.json` / `TSPhrases.ocd2` 数据）。
5. **翻译等价性**：以 `tools/eval/run_eval.py` 作为回归门，Go 版输出必须与 C++ 版在 40×4×4 用例上语义等价（允许空格/标点差异）。

## 时间预估

- Phase A（Go daemon 骨架 + 五笔 + 翻译）：~5 天
- Phase B（TSF shim DLL）：~3 天
- Phase C（设置面板 + 集成）：~2 天
- Phase D（构建 + 安装器 + 文档）：~2 天
- **总计 ~12 天**（1-2 周内，符合你"直接全量重构"的预期）

## 不在本次范围

- 拼音引擎的整句候选排序（仅做基础版，等同句留 v2）
- 五笔 98/06 变体（当前仅 86）
- macOS/Linux（本就是 Windows-only 项目）
- 后端 typeanything-backend（独立私有仓库，不受影响）

---

**确认方向后我开始执行 Phase A 第 1 步（Go 项目骨架 + IPC 框架）。**