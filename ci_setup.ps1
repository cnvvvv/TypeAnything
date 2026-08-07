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
if (-not (Test-Path (Join-Path $boostDir "boost"))) {
    Write-Host "[2/4] Setting up Boost 1.84.0..."

    # Method A: Try prebuilt from SourceForge (direct mirror)
    $prebuiltOk = $false
    $urls = @(
        "https://sourceforge.net/projects/boost/files/boost-binaries/1.84.0/boost_${boostVer}-msvc-14.3-64.exe/download",
        "https://downloads.sourceforge.net/project/boost/boost-binaries/1.84.0/boost_${boostVer}-msvc-14.3-64.exe"
    )
    foreach ($url in $urls) {
        try {
            Write-Host "  Trying: $url"
            $exePath = Join-Path $env:TEMP "boost_${boostVer}-msvc-14.3-64.exe"
            # Use .NET WebClient for better redirect handling
            $wc = New-Object System.Net.WebClient
            $wc.Headers.Add("User-Agent", "TypeAnything-CI")
            $wc.DownloadFile($url, $exePath)
            $fileInfo = Get-Item $exePath
            Write-Host "  Downloaded: $($fileInfo.Length) bytes"
            if ($fileInfo.Length -gt 1000000) {
                # Extract (it's a self-extracting 7z)
                New-Item -ItemType Directory -Force -Path (Split-Path $boostDir) | Out-Null
                & 7z x $exePath -o"$boostDir" -y | Out-Null
                if (Test-Path (Join-Path $boostDir "boost")) {
                    $prebuiltOk = $true
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

        # Build with b2
        Push-Location $boostDir
        # Detect VS path
        $vswherePath = "C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe"
        $vsPath = & $vswherePath -latest -property installationPath
        $vcvars = Join-Path $vsPath "VC\Auxiliary\Build\vcvarsall.bat"
        Write-Host "  Using vcvars: $vcvars"
        # Activate VS env via batch file
        $batContent = "@echo off`r`ncall `"$vcvars`" x64`r`ncd /d `"$boostDir`"`r`nif not exist b2.exe call bootstrap.bat`r`nb2 -j%NUMBER_OF_PROCESSORS% --with-filesystem --with-json --with-locale --with-regex --with-serialization --with-system --with-thread toolset=msvc-14.3 link=static runtime-link=static address-model=64 stage`r`n"
        $batFile = Join-Path $env:TEMP "build_boost.bat"
        [System.IO.File]::WriteAllText($batFile, $batContent, [System.Text.Encoding]::Default)
        & $batFile 2>&1 | ForEach-Object { Write-Host $_ }
        if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Boost build failed" }
        Pop-Location
        Write-Host "  Boost built from source"
    }
} else {
    Write-Host "[2/4] Boost already present"
}
$env:BOOST_ROOT = $boostDir
$env:BOOST_LIBRARYDIR = Join-Path $boostDir "lib64-msvc-14.3"
if ($env:GITHUB_ENV) {
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_ROOT=$boostDir"
    Add-Content -Path $env:GITHUB_ENV -Value "BOOST_LIBRARYDIR=$($env:BOOST_LIBRARYDIR)"
}
Write-Host "  BOOST_ROOT=$env:BOOST_ROOT"

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
    Push-Location $weasel
    $env:rime_dir = $dataDir
    $env:plum_dir = "plum"
    & bash plum/rime-install preset
    if ($LASTEXITCODE -ne 0) { Pop-Location; throw "plum/rime-install failed" }
    Pop-Location
    Write-Host "  Rime data fetched"
} else {
    Write-Host "[4/4] Rime data already present"
}
Write-Host ""
Write-Host "=== ci_setup done ==="
