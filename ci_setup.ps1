# ci_setup.ps1 — CI environment setup for TypeAnything
# Runs on GitHub Actions windows-2022 runner.

$ErrorActionPreference = "Stop"
$repoRoot = $env:GITHUB_WORKSPACE
if (-not $repoRoot) { $repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot) }
$weasel = Join-Path $repoRoot "third_party\weasel"

Write-Host "=== ci_setup: repoRoot=$repoRoot ==="

# ── 1. Clone librime ──
$librimeDir = Join-Path $weasel "librime"
$tempDir = Join-Path $weasel "librime_src"
if (-not (Test-Path (Join-Path $librimeDir "build.bat"))) {
    Write-Host "[1/4] Cloning librime..."
    git clone --depth 1 https://github.com/rime/librime.git $tempDir
    robocopy $tempDir $librimeDir /E /XD .git plugins /XF .gitignore /NFL /NDL /NJH /NJS /NC /NS /NP
    if ($LASTEXITCODE -ge 8) { throw "robocopy failed" }
    Remove-Item -Recurse -Force $tempDir
    Write-Host "  librime cloned"
} else {
    Write-Host "[1/4] librime already present"
}

# ── 2. Setup Boost 1.84.0 ──
$boostVer = "1_84_0"
$boostDir = Join-Path $weasel "deps\boost_$boostVer"
$boostLibDir = ""
if (-not (Test-Path (Join-Path $boostDir "boost"))) {
    Write-Host "[2/4] Setting up Boost 1.84.0..."

    # Method A: Try prebuilt from SourceForge
    $prebuiltOk = $false
    $urls = @(
        "https://sourceforge.net/projects/boost/files/boost-binaries/1.84.0/boost_${boostVer}-msvc-14.3-64.exe/download",
        "https://downloads.sourceforge.net/project/boost/boost-binaries/1.84.0/boost_${boostVer}-msvc-14.3-64.exe"
    )
    foreach ($url in $urls) {
        try {
            Write-Host "  Trying: $url"
            $exePath = Join-Path $env:TEMP "boost_${boostVer}-msvc-14.3-64.exe"
            $wc = New-Object System.Net.WebClient
            $wc.Headers.Add("User-Agent", "TypeAnything-CI")
            $wc.DownloadFile($url, $exePath)
            $fileInfo = Get-Item $exePath
            Write-Host "  Downloaded: $($fileInfo.Length) bytes"
            if ($fileInfo.Length -gt 1000000) {
                New-Item -ItemType Directory -Force -Path (Split-Path $boostDir) | Out-Null
                & 7z x $exePath -o"$boostDir" -y | Out-Null
                if (Test-Path (Join-Path $boostDir "boost")) {
                    $prebuiltOk = $true
                    $boostLibDir = Join-Path $boostDir "lib64-msvc-14.3"
                    Write-Host "  Boost prebuilt extracted"
                    break
                }
            } else {
                Write-Host "  File too small, likely HTML page"
            }
        } catch {
            Write-Host "  Failed: $($_.Exception.Message)"
        }
    }

    # Method B: Build from source (reliable fallback)
    if (-not $prebuiltOk) {
        Write-Host "  Building Boost from source..."
        $srcUrl = "https://archives.boost.io/release/1.84.0/source/boost_${boostVer}.7z"
        $srcArchive = Join-Path $env:TEMP "boost_${boostVer}.7z"
        Invoke-WebRequest $srcUrl -OutFile $srcArchive -UseBasicParsing
        New-Item -ItemType Directory -Force -Path (Split-Path $boostDir) | Out-Null
        & 7z x $srcArchive -o"$boostDir\.." -y | Out-Null
        Remove-Item $srcArchive -ErrorAction SilentlyContinue

        Push-Location $boostDir
        $vswherePath = "C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe"
        $vsPath = & $vswherePath -latest -property installationPath
        $vcvars = Join-Path $vsPath "VC\Auxiliary\Build\vcvarsall.bat"
        Write-Host "  Using vcvars: $vcvars"
        $batContent = "@echo off`r`ncall `"$vcvars`" x64`r`ncd /d `"$boostDir`"`r`nif not exist b2.exe call bootstrap.bat`r`nb2 -j%NUMBER_OF_PROCESSORS% --with-filesystem --with-json --with-locale --with-regex --with-serialization --with-system --with-thread toolset=msvc-14.3 link=static runtime-link=static address-model=64 stage`r`n"
        $batFile = Join-Path $env:TEMP "build_boost.bat"
        [System.IO.File]::WriteAllText($batFile, $batContent, [System.Text.Encoding]::Default)
        & $batFile 2>&1 | ForEach-Object { Write-Host $_ }
        if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Boost build failed" }
        Pop-Location
        $boostLibDir = Join-Path $boostDir "stage\lib"
        Write-Host "  Boost built from source"
    }
} else {
    Write-Host "[2/4] Boost already present"
    $prebuiltLib = Join-Path $boostDir "lib64-msvc-14.3"
    $sourceLib = Join-Path $boostDir "stage\lib"
    if (Test-Path $prebuiltLib) {
        $boostLibDir = $prebuiltLib
    } elseif (Test-Path $sourceLib) {
        $boostLibDir = $sourceLib
    } else {
        $boostLibDir = $prebuiltLib
    }
}
$env:BOOST_ROOT = $boostDir
$env:BOOST_LIBRARYDIR = $boostLibDir
if ($env:GITHUB_ENV) {
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_ROOT=$boostDir"
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_LIBRARYDIR=$boostLibDir"
}
Write-Host "  BOOST_ROOT=$env:BOOST_ROOT"
Write-Host "  BOOST_LIBRARYDIR=$env:BOOST_LIBRARYDIR"

# ── 3. Create missing data files ──
Write-Host "[3/4] Creating missing data files..."
$dictPath = Join-Path $librimeDir "plugins\typeanything\schema\typeanything.dict.yaml"
if (-not (Test-Path $dictPath)) {
    $dictContent = "---`nname: typeanything`nversion: `"0.3`"`nsort: by_weight`nuse_preset_vocabulary: true`nimport_tables:`n  - luna_pinyin`n...`n"
    [System.IO.File]::WriteAllText($dictPath, $dictContent, (New-Object System.Text.UTF8Encoding $true))
    Write-Host "  Created typeanything.dict.yaml"
}
$weaselYamlPath = Join-Path $weasel "output\data\weasel.yaml"
if (-not (Test-Path $weaselYamlPath)) {
    New-Item -ItemType Directory -Force -Path (Split-Path $weaselYamlPath) | Out-Null
    $yaml = "config_version: `"0.17.4.0`"`nstyle:`n  color_scheme: native`n  horizontal: true`n  inline_preedit: true`n  font_face: `"Microsoft YaHei`"`n  font_point: 14`n  label_font_face: `"Microsoft YaHei`"`n  label_font_point: 10`n  comment_font_face: `"Microsoft YaHei`"`n  comment_font_point: 10`n  layout:`n    min_width: 160`n    min_height: 0`n    border_width: 3`n    margin_x: 12`n    margin_y: 12`n    spacing: 10`n    candidate_spacing: 5`n    hilite_spacing: 4`n    hilite_padding: 2`n    round_corner: 4`n    corner_radius: 4`n    shadow_radius: 0`n"
    [System.IO.File]::WriteAllText($weaselYamlPath, $yaml, (New-Object System.Text.UTF8Encoding $true))
    Write-Host "  Created weasel.yaml"
}

# ── 4. Fetch Rime base data ──
$dataDir = Join-Path $weasel "output\data"
if (-not (Test-Path (Join-Path $dataDir "luna_pinyin.dict.yaml"))) {
    Write-Host "[4/4] Fetching Rime base data..."
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

    # Method A: Use plum with correct :preset syntax
    $plumOk = $false
    try {
        Push-Location $weasel
        $env:rime_dir = $dataDir
        $env:plum_dir = "plum"
        $env:GIT_TERMINAL_PROMPT = "0"
        $env:no_update = "1"
        & bash plum/rime-install :preset 2>&1 | ForEach-Object { Write-Host $_ }
        if ($LASTEXITCODE -eq 0) {
            $plumOk = $true
            Write-Host "  Rime data fetched via plum"
        } else {
            Write-Host "  plum exited with code $LASTEXITCODE, trying fallback..."
        }
        Pop-Location
    } catch {
        Write-Host "  plum failed: $($_.Exception.Message)"
        Pop-Location -ErrorAction SilentlyContinue
    }

    # Method B: Direct clone fallback
    if (-not $plumOk) {
        Write-Host "  Falling back to direct package cloning..."
        $packages = @(
            @{ repo = "rime/rime-prelude";      name = "prelude" },
            @{ repo = "rime/rime-essay";        name = "essay" },
            @{ repo = "rime/rime-luna-pinyin";  name = "luna-pinyin" },
            @{ repo = "rime/rime-bopomofo";     name = "bopomofo" },
            @{ repo = "rime/rime-cangjie";      name = "cangjie" },
            @{ repo = "rime/rime-stroke";       name = "stroke" },
            @{ repo = "rime/rime-terra-pinyin"; name = "terra-pinyin" }
        )
        $pkgTemp = Join-Path $env:TEMP "rime_packages"
        if (Test-Path $pkgTemp) { Remove-Item -Recurse -Force $pkgTemp }
        New-Item -ItemType Directory -Force -Path $pkgTemp | Out-Null

        $env:GIT_TERMINAL_PROMPT = "0"
        foreach ($pkg in $packages) {
            $cloneDir = Join-Path $pkgTemp $pkg.name
            Write-Host "    Cloning $($pkg.repo)..."
            & git clone --depth 1 "https://github.com/$($pkg.repo).git" $cloneDir 2>&1 | ForEach-Object { Write-Host "    $_" }
            if ($LASTEXITCODE -eq 0) {
                Get-ChildItem -Path $cloneDir -Filter "*.yaml" | Where-Object { $_.Name -notmatch '\.custom\.yaml$|\.recipe\.yaml$' } | ForEach-Object {
                    $dest = Join-Path $dataDir $_.Name
                    if (-not (Test-Path $dest)) { Copy-Item $_.FullName $dest }
                }
                Get-ChildItem -Path $cloneDir -Filter "*.txt" | ForEach-Object {
                    $dest = Join-Path $dataDir $_.Name
                    if (-not (Test-Path $dest)) { Copy-Item $_.FullName $dest }
                }
                Write-Host "    $($pkg.name) installed"
            } else {
                Write-Host "    WARNING: Failed to clone $($pkg.repo)"
            }
        }
        Remove-Item -Recurse -Force $pkgTemp -ErrorAction SilentlyContinue
        Write-Host "  Direct package cloning done"
    }
} else {
    Write-Host "[4/4] Rime data already present"
}
Write-Host ""
Write-Host "=== ci_setup done ==="
