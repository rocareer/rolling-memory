# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>
#
# install.ps1 — One-line installer for Rolling-Memory on Windows (PowerShell).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File install.ps1 [--with-hooks] [--with-instructions]
#   irm https://github.com/rocareer/rolling-memory/raw/main/install.ps1 | iex
$ErrorActionPreference = "Stop"

$DefaultRepoUrl = "https://github.com/rocareer/rolling-memory.git"
$DefaultReleaseBase = "https://github.com/rocareer/rolling-memory/releases/latest/download"
# No default mirror: GitHub is the single public source for this project
# (see the project policy on external terms). Inject a mirror explicitly with
# ROLLING_MEMORY_RELEASE_MIRROR=<base>/releases/latest/download if you run one.
$DefaultReleaseMirror = ""
$RepoUrl = if ($env:ROLLING_MEMORY_REPO_URL) { $env:ROLLING_MEMORY_REPO_URL } else { $DefaultRepoUrl }
# Same rule for the source fallback: no third-party default mirror.
$RepoMirrorUrl = if ($env:ROLLING_MEMORY_REPO_MIRROR) { $env:ROLLING_MEMORY_REPO_MIRROR } else { "" }

$ReleaseBaseExplicit = $false
if (Test-Path Env:ROLLING_MEMORY_RELEASE_BASE) {
    $ReleaseBase = $env:ROLLING_MEMORY_RELEASE_BASE
    if ($ReleaseBase -ne "") { $ReleaseBaseExplicit = $true }
} else {
    $ReleaseBase = $DefaultReleaseBase
}
if ($ReleaseBase -in @("off", "none", "0")) {
    $ReleaseBase = ""
}
$ReleaseMirror = if (Test-Path Env:ROLLING_MEMORY_RELEASE_MIRROR) { $env:ROLLING_MEMORY_RELEASE_MIRROR } else { $DefaultReleaseMirror }
if ($ReleaseMirror -in @("off", "none", "0")) {
    $ReleaseMirror = ""
}

$InstallDir = if ($env:ROLLING_MEMORY_INSTALL_DIR) { $env:ROLLING_MEMORY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "rolling-memory\bin" }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$TargetBin = Join-Path $InstallDir "rolling-memory.exe"

# Map the host architecture to the release asset suffix.
$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "install.ps1: unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$Built = $false
$PrebuiltErr = ""
$EvalDataTarget = Join-Path (Split-Path -Parent $InstallDir) "evaldata"
if ((Test-Path "cmd\rolling-memory\main.go") -and (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[rolling-memory] Building from local checkout..."
    $env:CGO_ENABLED = "0"
    go build -trimpath -o $TargetBin .\cmd\rolling-memory
    if ((Test-Path "evaldata\MANIFEST.json") -and (-not (Test-Path (Join-Path $EvalDataTarget "MANIFEST.json")))) {
        Copy-Item -Recurse -Force "evaldata" $EvalDataTarget -ErrorAction SilentlyContinue
    }
    $Built = $true
}

function Try-DownloadPrebuilt([string]$BaseUrl) {
    $SumsUrl = "$($BaseUrl.TrimEnd('/'))/SHA256SUMS"
    $Sums = $null
    try {
        $Sums = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 20 -Uri $SumsUrl).Content
        if ($Sums -is [byte[]]) {
            $Sums = [System.Text.Encoding]::UTF8.GetString($Sums)
        }
    } catch {
        $script:PrebuiltErr = "无法连接 $SumsUrl"
        if ($ReleaseBaseExplicit) {
            throw "无法获取 $SumsUrl 的 SHA256SUMS，拒绝安装未校验的二进制。"
        }
        return $false
    }
    if (-not $Sums) { return $false }

    # Prefer native architecture asset; on ARM64 allow fallback to windows-amd64.exe
    # (Windows 11 ARM runs x64 binaries natively) if the mirror only carries amd64.
    $CandidateAssets = @("rolling-memory-windows-$Arch.exe")
    if ($Arch -eq "arm64" -and (-not $ReleaseBaseExplicit)) {
        $CandidateAssets += "rolling-memory-windows-amd64.exe"
    }

    $SelectedAsset = $null
    $Want = $null
    foreach ($Cand in $CandidateAssets) {
        foreach ($Line in ($Sums -split "`r?`n")) {
            $Fields = $Line.Trim() -split '\s+'
            if ($Fields.Count -ge 2 -and $Fields[1].TrimStart('*') -eq $Cand) {
                $SelectedAsset = $Cand
                $Want = $Fields[0].ToLower()
                break
            }
        }
        if ($Want) { break }
    }

    if (-not $Want) {
        $script:PrebuiltErr = "SHA256SUMS 中缺少 $($CandidateAssets[0]) 的校验和"
        if ($ReleaseBaseExplicit) {
            throw "$($script:PrebuiltErr)，拒绝安装未校验的二进制。"
        }
        return $false
    }

    $AssetUrl = "$($BaseUrl.TrimEnd('/'))/$SelectedAsset"
    Write-Host "[rolling-memory] Downloading prebuilt binary ($SelectedAsset)..."
    $TmpBin = "$TargetBin.tmp.$PID"
    try {
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $AssetUrl -OutFile $TmpBin
        $Got = (Get-FileHash -Algorithm SHA256 -Path $TmpBin).Hash.ToLower()
        if ($Got -ne $Want) {
            Remove-Item -Force -ErrorAction SilentlyContinue $TmpBin
            throw "SHA256 校验失败：期望 $Want，实得 $Got。"
        }
        Move-Item -Force -Path $TmpBin -Destination $TargetBin
        Write-Host "[rolling-memory] SHA256 verified ($Got)"
        return $true
    } catch {
        Remove-Item -Force -ErrorAction SilentlyContinue $TmpBin
        $script:PrebuiltErr = $_.Exception.Message
        if ($ReleaseBaseExplicit -or ($_.Exception.Message -match "SHA256")) {
            throw
        }
        return $false
    }
}

if ((-not $Built) -and ($ReleaseBase -ne "")) {
    if (Try-DownloadPrebuilt $ReleaseBase) {
        $Built = $true
    } elseif ((-not $ReleaseBaseExplicit) -and ($ReleaseMirror -ne "") -and ($ReleaseMirror -ne $ReleaseBase)) {
        Write-Host "[rolling-memory] Primary release channel unavailable; trying mirror ($ReleaseMirror)..."
        if (Try-DownloadPrebuilt $ReleaseMirror) {
            $Built = $true
        }
    }
    if (-not $Built) {
        Write-Host "[rolling-memory] Prebuilt channel unavailable ($PrebuiltErr)."
    }
}

if ((-not $Built) -and (Get-Command go -ErrorAction SilentlyContinue) -and (Get-Command git -ErrorAction SilentlyContinue)) {
    $TmpRepo = Join-Path ([System.IO.Path]::GetTempPath()) ("rm-src-" + [System.Guid]::NewGuid().ToString("N"))
    try {
        Write-Host "[rolling-memory] Cloning $RepoUrl and building..."
        git clone --depth 1 $RepoUrl $TmpRepo
        if ($LASTEXITCODE -ne 0 -and $RepoMirrorUrl -and ($RepoUrl -eq $DefaultRepoUrl)) {
            Write-Host "[rolling-memory] Primary source unavailable; retrying mirror $RepoMirrorUrl..."
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $TmpRepo
            git clone --depth 1 $RepoMirrorUrl $TmpRepo
        }
        if ($LASTEXITCODE -ne 0) {
            throw "install.ps1: failed to clone $RepoUrl"
        }
        Push-Location $TmpRepo
        try {
            $env:CGO_ENABLED = "0"
            go build -trimpath -o $TargetBin .\cmd\rolling-memory
            if ($LASTEXITCODE -ne 0) { throw "go build failed" }
            if ((Test-Path "evaldata\MANIFEST.json") -and (-not (Test-Path (Join-Path $EvalDataTarget "MANIFEST.json")))) {
                Copy-Item -Recurse -Force "evaldata" $EvalDataTarget -ErrorAction SilentlyContinue
            }
            $Built = $true
        } finally {
            Pop-Location
        }
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $TmpRepo
    }
}

if (-not $Built) {
    throw "无法获取预编译二进制（原因：$PrebuiltErr），且本机未安装 Git / Go 编译环境。普通用户请检查网络或设置 `$env:ROLLING_MEMORY_RELEASE_BASE='https://your-mirror/releases/latest/download' 后重试；开发者可安装 Go 1.26+ 与 Git 从源码构建。"
}

# Short alias `rmem` (equivalent to the POSIX symlink pair created by install.sh).
$RmemCmd = Join-Path $InstallDir "rmem.cmd"
Set-Content -Path $RmemCmd -Encoding ASCII -Value "@echo off`r`n`"%~dp0rolling-memory.exe`" %*"
Write-Host "[rolling-memory] Installed binary to $TargetBin (alias: $RmemCmd)"
if (($env:PATH -split ';') -notcontains $InstallDir) {
    $env:PATH = "$InstallDir;$env:PATH"
    if ($env:ROLLING_MEMORY_NO_MODIFY_PATH -ne "1") {
        try {
            $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
            $Parts = if ($UserPath) { $UserPath -split ';' } else { @() }
            if ($Parts -notcontains $InstallDir) {
                $NewUserPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
                [Environment]::SetEnvironmentVariable("Path", $NewUserPath, "User")
                Write-Host "[rolling-memory] Added $InstallDir to your Windows user PATH."
            }
        } catch {
            Write-Host "[rolling-memory] Add to PATH if needed: `$env:Path += `";$InstallDir`""
        }
    }
}

try { & $TargetBin init --write-config *> $null } catch {}

$InstallArgs = if ($args.Count -eq 0) { @("install", "--with-hooks") } else { @("install") + $args }
$PrevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$InstallOut = & $TargetBin @InstallArgs 2>&1
$InstallRc = $LASTEXITCODE
$ErrorActionPreference = $PrevEAP

if ($InstallOut) {
    $InstallOut | ForEach-Object { Write-Host $_ }
}
if ($InstallRc -eq 0) {
    & $TargetBin install --check
} elseif (($InstallOut -join "`n") -match "no harness detected") {
    Write-Host "[rolling-memory] CLI binary and local vault are installed and ready (no host harness detected yet)."
    Write-Host "[rolling-memory] Wire a harness anytime with: rolling-memory install --with-hooks [<harness-id>]"
} else {
    exit $InstallRc
}

Write-Host "[rolling-memory] Web console: run `rolling-memory panel` to start it (backend :9006 + web frontend :8008; add --desktop-shortcut to install for a desktop launcher)."
Write-Host "[rolling-memory] Self-update: run `rolling-memory update` (or `rolling-memory update --check`) anytime to update the CLI, console and host integrations."
