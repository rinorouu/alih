$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Install-Alih {
    $repositoryUrl = "https://github.com/rinorouu/alih"
    $releaseApiUrl = "https://api.github.com/repos/rinorouu/alih/releases/latest"
    $temporaryDirectory = $null
    $temporaryInstallPath = $null

    try {
        if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
            throw "unsupported operating system. This installer supports Windows amd64 only."
        }

        $architecture = if ($env:PROCESSOR_ARCHITEW6432) {
            $env:PROCESSOR_ARCHITEW6432
        } else {
            $env:PROCESSOR_ARCHITECTURE
        }
        if ($architecture -ne "AMD64") {
            throw "unsupported architecture '$architecture'. This installer supports Windows amd64 only."
        }

        [Net.ServicePointManager]::SecurityProtocol =
            [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        $headers = @{
            Accept = "application/vnd.github+json"
            "User-Agent" = "Alih-Installer"
        }
        $release = Invoke-RestMethod -UseBasicParsing -Uri $releaseApiUrl -Headers $headers
        if ($release.draft -or $release.prerelease) {
            throw "GitHub returned a draft or prerelease instead of the latest stable release."
        }
        $tag = [string]$release.tag_name
        if ($tag -notmatch '^v[0-9][0-9A-Za-z._-]*$') {
            throw "GitHub returned an invalid release tag."
        }

        $artifact = "alih-windows-amd64.exe"
        $downloadBase = "$repositoryUrl/releases/download/$tag"
        $temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ("alih-install-" + [Guid]::NewGuid().ToString("N"))
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        $binaryPath = Join-Path $temporaryDirectory $artifact
        $checksumsPath = Join-Path $temporaryDirectory "SHA256SUMS"

        Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$artifact" -OutFile $binaryPath
        Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/SHA256SUMS" -OutFile $checksumsPath

        $matches = @(
            Get-Content -LiteralPath $checksumsPath | ForEach-Object {
                if ($_ -match '^([0-9A-Fa-f]{64})\s+\*?alih-windows-amd64\.exe$') {
                    $Matches[1]
                }
            }
        )
        if ($matches.Count -ne 1) {
            throw "SHA256SUMS does not contain exactly one valid entry for $artifact."
        }
        $expectedHash = $matches[0]
        $actualHash = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash
        if (-not $actualHash.Equals($expectedHash, [StringComparison]::OrdinalIgnoreCase)) {
            throw "SHA-256 checksum verification failed for $artifact; the existing Alih installation was not changed."
        }

        $expectedVersion = "alih " + $tag.Substring(1)
        $reportedVersion = (& $binaryPath --version | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw "the verified binary could not be executed."
        }
        if ($reportedVersion -ne $expectedVersion) {
            throw "the verified binary reported '$reportedVersion', expected '$expectedVersion'."
        }

        if ($env:ALIH_INSTALL_DIR) {
            $installDirectory = [IO.Path]::GetFullPath($env:ALIH_INSTALL_DIR)
        } else {
            $localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
            if (-not $localAppData) {
                throw "the user LocalApplicationData directory could not be resolved."
            }
            $installDirectory = Join-Path $localAppData "Alih\bin"
        }
        New-Item -ItemType Directory -Force -Path $installDirectory | Out-Null
        $installPath = Join-Path $installDirectory "alih.exe"
        if (Test-Path -LiteralPath $installPath -PathType Container) {
            throw "$installPath is a directory and cannot be replaced."
        }

        $temporaryInstallPath = Join-Path $installDirectory (".alih." + [Guid]::NewGuid().ToString("N") + ".tmp")
        Copy-Item -LiteralPath $binaryPath -Destination $temporaryInstallPath
        Move-Item -LiteralPath $temporaryInstallPath -Destination $installPath -Force
        $temporaryInstallPath = $null

        $normalizedInstallDirectory = [IO.Path]::GetFullPath($installDirectory).TrimEnd('\')
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $pathEntries = @($userPath -split ';' | Where-Object { $_ })
        $pathContainsInstallDirectory = $false
        foreach ($entry in $pathEntries) {
            try {
                $normalizedEntry = [IO.Path]::GetFullPath($entry).TrimEnd('\')
                if ($normalizedEntry.Equals($normalizedInstallDirectory, [StringComparison]::OrdinalIgnoreCase)) {
                    $pathContainsInstallDirectory = $true
                    break
                }
            } catch {
                # Preserve unrelated PATH entries that cannot be normalized.
            }
        }

        if (-not $pathContainsInstallDirectory) {
            if ($normalizedInstallDirectory.Contains(';')) {
                Write-Warning "The installation directory contains ';' and was not added to PATH."
            } else {
                try {
                    $newUserPath = if ($userPath) { "$userPath;$normalizedInstallDirectory" } else { $normalizedInstallDirectory }
                    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
                    $env:Path = "$env:Path;$normalizedInstallDirectory"
                    Write-Host "Added to the user PATH: $normalizedInstallDirectory"
                    Write-Host "Open a new terminal before running alih by name."
                    $pathContainsInstallDirectory = $true
                } catch {
                    Write-Warning "Alih was installed, but the user PATH could not be updated: $($_.Exception.Message)"
                }
            }
        }

        Write-Host "Alih $($tag.Substring(1)) installed successfully."
        Write-Host "Installed to: $installPath"
        if ($pathContainsInstallDirectory) {
            Write-Host "Next: alih --help"
        } else {
            Write-Host "Add this directory to your user PATH: $normalizedInstallDirectory"
            Write-Host "For now, run: & '$installPath' --help"
        }
    } finally {
        if ($temporaryInstallPath -and (Test-Path -LiteralPath $temporaryInstallPath)) {
            Remove-Item -LiteralPath $temporaryInstallPath -Force -ErrorAction SilentlyContinue
        }
        if ($temporaryDirectory -and (Test-Path -LiteralPath $temporaryDirectory)) {
            Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

try {
    Install-Alih
} catch {
    Write-Error "Alih installation failed: $($_.Exception.Message)"
    exit 1
}
