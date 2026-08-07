# Stage all artifacts for installer.rc to embed.
# Run before xmake build.

$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
$repoRoot = Split-Path -Parent (Split-Path -Parent $here)
$embed = Join-Path $here "embed"

if (Test-Path $embed) { Remove-Item -Recurse -Force $embed }
New-Item -ItemType Directory -Force -Path $embed,"$embed\target-ui","$embed\install-ui" | Out-Null

# 1. Weasel binaries (check both xmake build\windows\x64\release and msbuild output\)
$buildBin = "$repoRoot\third_party\weasel\build\windows\x64\release"
$outputDir = "$repoRoot\third_party\weasel\output"
Copy-Item "$repoRoot\third_party\weasel\librime\dist\lib\rime.dll" "$embed\rime.dll"
function CopyWeaselBinary($subdir, $filename, $dstName) {
    $xmakePath = Join-Path $buildBin "$subdir\$filename"
    $msbuildPath = Join-Path $outputDir $filename
    $dst = Join-Path $embed $dstName
    if (Test-Path $xmakePath) {
        Copy-Item $xmakePath $dst
    } elseif (Test-Path $msbuildPath) {
        Copy-Item $msbuildPath $dst
    } else {
        throw "Weasel binary not found: $filename (checked: $xmakePath, $msbuildPath)"
    }
}
CopyWeaselBinary "WeaselTSF" "weaselx64.dll" "weaselx64.dll"
CopyWeaselBinary "WeaselServer" "WeaselServer.exe" "WeaselServer.exe"
CopyWeaselBinary "WeaselDeployer" "WeaselDeployer.exe" "WeaselDeployer.exe"

# 2. ta-settings.exe + loader
$taBin = "$repoRoot\tools\ta-settings\build\windows\x64\release"
Copy-Item "$taBin\ta-settings.exe"     "$embed\ta-settings.exe"
Copy-Item "$taBin\WebView2Loader.dll"  "$embed\WebView2Loader.dll"

# 3. target ui (for ta-settings to load post-install)
foreach ($f in "index.html","style.css","app.js","fish.png") {
    Copy-Item "$taBin\ui\$f" "$embed\target-ui\$f"
}

# 4. installer ui (this exe's own webview pages)
foreach ($f in "index.html","style.css","app.js","fish.png") {
    Copy-Item "$here\ui\$f" "$embed\install-ui\$f"
}

# 5. schema yaml template + supplement dict (modern AI/IT/slang terms)
Copy-Item "$repoRoot\third_party\weasel\librime\plugins\typeanything\schema\typeanything.schema.yaml" `
          "$embed\typeanything.schema.yaml"
Copy-Item "$repoRoot\third_party\weasel\librime\plugins\typeanything\schema\typeanything.dict.yaml" `
          "$embed\typeanything.dict.yaml"

# 5b. wubi86 schema + dict (rime/rime-wubi). Shares translation pipeline with pinyin.
Copy-Item "$repoRoot\third_party\weasel\librime\plugins\typeanything\schema\wubi86.schema.yaml" `
          "$embed\wubi86.schema.yaml"
Copy-Item "$repoRoot\third_party\weasel\librime\plugins\typeanything\schema\wubi86.dict.yaml" `
          "$embed\wubi86.dict.yaml"

# 6. Rime base data - required on cold machines (no prior Weasel install).
#    Search order: installed Weasel > weasel build output (CI) > librime/data/minimal
$pfData = "C:\Program Files\Rime\weasel-0.17.4\data"
$weaselData = "$repoRoot\third_party\weasel\output\data"
$minData = "$repoRoot\third_party\weasel\librime\data\minimal"
function StageDataFile($name) {
    $dst = Join-Path $embed $name
    foreach ($src in @(
        (Join-Path $pfData $name),
        (Join-Path $weaselData $name),
        (Join-Path $minData $name)
    )) {
        if (Test-Path $src) { Copy-Item $src $dst; return }
    }
    throw "data file not found: $name (checked: $pfData, $weaselData, $minData)"
}
StageDataFile "default.yaml"
StageDataFile "luna_pinyin.dict.yaml"
StageDataFile "luna_pinyin.schema.yaml"
StageDataFile "essay.txt"
StageDataFile "symbols.yaml"
StageDataFile "punctuation.yaml"
StageDataFile "key_bindings.yaml"
# weasel.yaml - base panel/style template. Without this on a cold machine,
# WeaselDeployer cannot produce build\weasel.yaml and the candidate window
# renders at ~0px on high-DPI screens (issue #14).
StageDataFile "weasel.yaml"

# 6b. OpenCC data - required by the schema's simplifier@simplification_filter
#     (opencc_config: t2s.json). Without these the simplifier fails to load,
#     traditional luna_pinyin output passes through unconverted AND the
#     candidate generation chain errors out -> empty candidate window.
#     Only the t2s (trad->simp) chain is needed: t2s.json + TSPhrases + TSCharacters.
#     Search order: installed Weasel opencc > weasel build output opencc > librime share opencc
$openccDst = Join-Path $embed "opencc"
New-Item -ItemType Directory -Force -Path $openccDst | Out-Null
$openccSources = @(
    (Join-Path $pfData "opencc"),
    (Join-Path $weaselData "opencc"),
    "$repoRoot\third_party\weasel\librime\share\opencc"
)
foreach ($f in "t2s.json","TSPhrases.ocd2","TSCharacters.ocd2") {
    $found = $false
    foreach ($srcDir in $openccSources) {
        $srcF = Join-Path $srcDir $f
        if (Test-Path $srcF) {
            Copy-Item $srcF (Join-Path $openccDst $f)
            $found = $true
            break
        }
    }
    if (-not $found) { throw "OpenCC data missing: $f (checked: $($openccSources -join ', '))" }
}

# 7. Translate / classify prompts (UTF-8). Source of truth = embed_prompts.txt
#    (generated from tools/eval/run_eval.py validated prompts).
Copy-Item "$here\embed_prompts.txt" "$embed\typeanything_prompts.txt"

# Compile installer.rc -> .res -> .obj (real COFF) so xmake link picks it up
# as a normal object file. xmake's built-in RC handling outputs RES format
# but under a .obj name, which link.exe silently drops.
$rcExe = Get-ChildItem "C:\Program Files (x86)\Windows Kits\10\bin" -Directory -ErrorAction SilentlyContinue |
         Sort-Object Name -Descending | ForEach-Object {
             $candidate = Join-Path $_.FullName "x64\rc.exe"
             if (Test-Path $candidate) { return $candidate }
         } | Select-Object -First 1
$vsDirs = @(
    "C:\Program Files\Microsoft Visual Studio\2022\Enterprise",
    "C:\Program Files\Microsoft Visual Studio\2022\Community",
    "C:\Program Files\Microsoft Visual Studio\2022\Professional",
    "C:\Program Files\Microsoft Visual Studio\2022\BuildTools",
    "C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools",
    "C:\Program Files (x86)\Microsoft Visual Studio\2022\Enterprise"
)
$cvtres = $null
foreach ($vsDir in $vsDirs) {
    $msvcDir = Join-Path $vsDir "VC\Tools\MSVC"
    if (Test-Path $msvcDir) {
        $cvtres = Get-ChildItem $msvcDir -Directory -ErrorAction SilentlyContinue |
                  Sort-Object Name -Descending | ForEach-Object {
                      $candidate = Join-Path $_.FullName "bin\HostX64\x64\cvtres.exe"
                      if (Test-Path $candidate) { return $candidate }
                  } | Select-Object -First 1
        if ($cvtres) { break }
    }
}

if (-not $rcExe)   { throw "rc.exe not found (need Windows SDK)" }
if (-not $cvtres)  { throw "cvtres.exe not found (need VS2022 BuildTools)" }

Write-Host "==> rc.exe : $rcExe"
Write-Host "==> cvtres : $cvtres"

$rc  = Join-Path $here "installer.rc"
$res = Join-Path $here "embed\installer.res"
$obj = Join-Path $here "embed\installer.res.obj"

& $rcExe -nologo -fo $res $rc
if ($LASTEXITCODE -ne 0) { throw "rc.exe failed: $LASTEXITCODE" }
& $cvtres /MACHINE:X64 /NOLOGO /OUT:$obj $res
if ($LASTEXITCODE -ne 0) { throw "cvtres failed: $LASTEXITCODE" }

Write-Host "==> embed staged:"
Get-ChildItem -Recurse $embed | ForEach-Object { Write-Host ("  {0,10}B  {1}" -f $_.Length, $_.FullName.Substring($embed.Length + 1)) }