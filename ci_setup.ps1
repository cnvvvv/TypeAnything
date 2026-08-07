# ci_setup.ps1 — CI environment setup for TypeAnything
# Runs on GitHub Actions windows-2022 runner.
# Steps:
#   1. Clone librime (preserving tracked plugin files)
#   2. Download/build Boost 1.84.0 (MSVC 14.3 x64)
#   3. Create missing data files (typeanything.dict.yaml, weasel.yaml)
#   4. Fetch Rime base data via plum/rime-install

$ErrorActionPreference = "Stop"
$repoRoot = $env:GITHUB_WORKSPACE
if (-not $repoRoot) { $repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot) }
$weasel = Join-Path $repoRoot "third_party\weasel"

Write-Host "=== ci_setup: repoRoot=$repoRoot ==="

# ── 1. Clone librime (only the plugin is tracked; source must be fetched) ──
$librimeDir = Join-Path $weasel "librime"
$tempDir = Join-Path $weasel "librime_src"
if (-not (Test-Path (Join-Path $librimeDir "build.bat"))) {
    Write-Host "[1/4] Cloning librime..."
    git clone --depth 1 https://github.com/rime/librime.git $tempDir
    # Copy librime source into target dir, preserving our tracked plugins/ dir
    robocopy $tempDir $librimeDir /E /XD .git plugins /XF .gitignore /NFL /NDL /NJH /NJS /NC /NS /NP
    if ($LASTEXITCODE -ge 8) { throw "robocopy failed" }
    Remove-Item -Recurse -Force $tempDir
    Write-Host "  librime cloned to $librimeDir"
} else {
    Write-Host "[1/4] librime already present"
}

# ── 2. Download/build Boost 1.84.0 ──
$boostVer = "1_84_0"
$boostDir = Join-Path $weasel "deps\boost_$boostVer"
$boostRoot = $boostDir
if (-not (Test-Path (Join-Path $boostDir "boost"))) {
    Write-Host "[2/4] Setting up Boost 1.84.0..."

    # Method A: Try prebuilt binaries from SourceForge
    $prebuiltOk = $false
    try {
        Write-Host "  Trying prebuilt from SourceForge..."
        New-Item -ItemType Directory -Force -Path (Split-Path $boostDir) | Out-Null
        $url = "https://sourceforge.net/projects/boost/files/boost-binaries/1.84.0/boost_${boostVer}-msvc-14.3-64.exe/download"
        $exePath = Join-Path $env:TEMP "boost_${boostVer}-msvc-14.3-64.exe"
        Invoke-WebRequest $url -OutFile $exePath -UseBasicParsing -MaximumRedirection 10
        & 7z x $exePath -o"$boostDir" -y | Out-Null
        Remove-Item $exePath -ErrorAction SilentlyContinue
        if (Test-Path (Join-Path $boostDir "boost")) {
            $prebuiltOk = $true
            Write-Host "  Boost prebuilt extracted to $boostDir"
        }
    } catch {
        Write-Host "  Prebuilt download failed: $($_.Exception.Message)"
    }

    # Method B: Build from source (fallback — slow but reliable)
    if (-not $prebuiltOk) {
        Write-Host "  Building Boost from source..."
        Push-Location $weasel
        $env:BOOST_ROOT = $boostRoot
        # Download source
        $srcUrl = "https://archives.boost.io/release/1.84.0/source/boost_${boostVer}.7z"
        $srcArchive = Join-Path $env:TEMP "boost_${boostVer}.7z"
        Invoke-WebRequest $srcUrl -OutFile $srcArchive -UseBasicParsing
        New-Item -ItemType Directory -Force -Path (Split-Path $boostDir) | Out-Null
        & 7z x $srcArchive -o"$boostDir\.." -y | Out-Null
        Remove-Item $srcArchive -ErrorAction SilentlyContinue

        # Build with b2
        Push-Location $boostDir
        # Activate VS env
        $vswhere = Join-Path $env:ProgramFilesx86 "Microsoft Visual Studio\Installer\vswhere.exe"
        if (-not (Test-Path $vswhere)) { $vswhere = "C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe" }
        $vsPath = & $vswhere -latest -property installationPath
        $vcvars = Join-Path $vsPath "VC\Auxiliary\Build\vcvarsall.bat"
        $tmpEnv = Join-Path $env:TEMP "boost_vs_env.txt"
        & cmd /c "`"$vcvars`" x64 >nul 2>nul && set > `"$tmpEnv`""
        foreach ($line in (Get-Content $tmpEnv -Encoding Default)) {
            if ($line -match '^([^=]+)=(.*)$') { Set-Item -Path "env:$($Matches[1])" -Value $Matches[2] }
        }
        & .\bootstrap.bat
        & .\b2 -j$env:NUMBER_OF_PROCESSORS --with-filesystem --with-json --with-locale --with-regex --with-serialization --with-system --with-thread toolset=msvc-14.3 link=static runtime-link=static address-model=64 stage
        Pop-Location
        Pop-Location
        Write-Host "  Boost built from source at $boostDir"
    }
} else {
    Write-Host "[2/4] Boost already present at $boostDir"
}
$env:BOOST_ROOT = $boostRoot
$env:BOOST_LIBRARYDIR = Join-Path $boostRoot "lib64-msvc-14.3"
# Export to GitHub Actions env for subsequent steps
if ($env:GITHUB_ENV) {
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_ROOT=$boostRoot"
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_LIBRARYDIR=$($env:BOOST_LIBRARYDIR)"
}
Write-Host "  BOOST_ROOT=$env:BOOST_ROOT"
Write-Host "  BOOST_LIBRARYDIR=$env:BOOST_LIBRARYDIR"

# ── 3. Create missing data files ──
Write-Host "[3/4] Creating missing data files..."

# 3a. typeanything.dict.yaml (imports luna_pinyin + modern terms)
$dictPath = Join-Path $librimeDir "plugins\typeanything\schema\typeanything.dict.yaml"
if (-not (Test-Path $dictPath)) {
    $dictContent = @"
---
name: typeanything
version: "0.3"
sort: by_weight
use_preset_vocabulary: true
import_tables:
  - luna_pinyin
...
"@
    [System.IO.File]::WriteAllText($dictPath, $dictContent, (New-Object System.Text.UTF8Encoding $true))
    Write-Host "  Created typeanything.dict.yaml"
}

# 3b. weasel.yaml (base panel/style template — needed by WeaselDeployer)
$weaselYamlPath = Join-Path $weasel "output\data\weasel.yaml"
if (-not (Test-Path $weaselYamlPath)) {
    New-Item -ItemType Directory -Force -Path (Split-Path $weaselYamlPath) | Out-Null
    $weaselYamlContent = @"
# weasel.yaml — base UI config for TypeAnything (Weasel 0.17.4)
config_version: "0.17.4.0"
style:
  color_scheme: native
  horizontal: true
  inline_preedit: true
  font_face: "Microsoft YaHei"
  font_point: 14
  label_font_face: "Microsoft YaHei"
  label_font_point: 10
  comment_font_face: "Microsoft YaHei"
  comment_font_point: 10
  layout:
    min_width: 160
    min_height: 0
    border_width: 3
    margin_x: 12
    margin_y: 12
    spacing: 10
    candidate_spacing: 5
    hilite_spacing: 4
    hilite_padding: 2
    round_corner: 4
    corner_radius: 4
    shadow_radius: 0
"@
    [System.IO.File]::WriteAllText($weaselYamlPath, $weaselYamlContent, (New-Object System.Text.UTF8Encoding $true))
    Write-Host "  Created weasel.yaml"
}

# ── 4. Fetch Rime base data via plum ──
$dataDir = Join-Path $weasel "output\data"
if (-not (Test-Path (Join-Path $dataDir "luna_pinyin.dict.yaml"))) {
    Write-Host "[4/4] Fetching Rime base data (preset packages)..."
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    Push-Location $weasel
    $env:rime_dir = $dataDir
    $env:plum_dir = "plum"
    # Use Git Bash to run plum installer
    & bash plum/rime-install preset
    if ($LASTEXITCODE -ne 0) { throw "plum/rime-install failed" }
    Pop-Location
    Write-Host "  Rime data fetched to $dataDir"
} else {
    Write-Host "[4/4] Rime data already present"
}

Write-Host ""
Write-Host "=== ci_setup done ==="
Write-Host "BOOST_ROOT=$env:BOOST_ROOT"
