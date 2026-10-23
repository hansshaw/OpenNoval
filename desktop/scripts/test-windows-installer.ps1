#requires -Version 7.0
<#
Run only on an isolated Windows GitHub Actions runner:
  ./desktop/scripts/test-windows-installer.ps1 -PreviousInstaller old-setup.exe `
    -Installer new-setup.exe -ExpectedVersion 0.1.0
The old package may be a CI fixture built with a lower version. No /D argument
is passed to the upgrade: retaining the first custom path is what this tests.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$PreviousInstaller,
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][ValidatePattern('^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$')][string]$ExpectedVersion,
    [string]$ReportPath = (Join-Path $PSScriptRoot '../dist/reports/windows-installer-upgrade.json')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or $env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_OS -ne 'Windows' -or [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    throw 'Installer acceptance is restricted to Windows GitHub Actions with RUNNER_TEMP set.'
}
$previous = (Resolve-Path -LiteralPath $PreviousInstaller).Path
$current = (Resolve-Path -LiteralPath $Installer).Path
if ($previous -eq $current) { throw 'Supply distinct old and new installers.' }
$metadata = Get-Content -LiteralPath (Join-Path $PSScriptRoot '../package.json') -Raw | ConvertFrom-Json
$installerGuid = [string]$metadata.build.nsis.guid
if ($installerGuid -ne 'e8db1337-b330-59d5-a41f-5283be9b2bcf') { throw 'Unexpected installer identity; review the upgrade migration before running this test.' }
$productName = [string]$metadata.build.productName
$uninstallRoot = 'Software\Microsoft\Windows\CurrentVersion\Uninstall'
$installKey = "Software\$installerGuid"
$reportFile = [IO.Path]::GetFullPath($ReportPath)
$root = Join-Path ([IO.Path]::GetFullPath($env:RUNNER_TEMP)) ("ainovel-installer-" + [Guid]::NewGuid().ToString('N'))
$installDirectory = Join-Path $root "Custom Install With Spaces\$productName"
$report = [ordered]@{
    schemaVersion = 1; suite = 'windows-installer-upgrade'; ok = $false
    previousInstaller = [IO.Path]::GetFileName($previous); installer = [IO.Path]::GetFileName($current)
    expectedVersion = $ExpectedVersion; guid = $installerGuid; installDirectory = $installDirectory
    steps = [Collections.Generic.List[object]]::new(); cleanup = $false
    limitations = @('The previous package can be a lower-version CI fixture, not a historical public release.', 'No real model configuration or paid API is used.')
}
$ownedDataFolders = [Collections.Generic.List[string]]::new()
$sentinels = [Collections.Generic.List[object]]::new()
$started = [DateTime]::UtcNow

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Read-Installations {
    # HKCU keys can be shared by both views. Deduplicate the same registration,
    # but retain different paths/versions if two conflicting entries exist.
    $found = @{}
    foreach ($hive in @([Microsoft.Win32.RegistryHive]::CurrentUser, [Microsoft.Win32.RegistryHive]::LocalMachine)) {
        foreach ($view in @([Microsoft.Win32.RegistryView]::Registry64, [Microsoft.Win32.RegistryView]::Registry32)) {
            $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey($hive, $view)
            try {
                $install = $base.OpenSubKey($installKey)
                $location = ''
                if ($null -ne $install) {
                    try { $location = [string]$install.GetValue('InstallLocation', '') }
                    finally { $install.Dispose() }
                }
                $uninstall = $base.OpenSubKey($uninstallRoot)
                $identitySeen = $false
                if ($null -ne $uninstall) {
                    try {
                        foreach ($name in $uninstall.GetSubKeyNames()) {
                            $entry = $uninstall.OpenSubKey($name)
                            if ($null -eq $entry) { continue }
                            try {
                                $display = [string]$entry.GetValue('DisplayName', '')
                                if ($name -ne $installerGuid -and -not $display.StartsWith($productName, [StringComparison]::OrdinalIgnoreCase)) { continue }
                                if ($name -eq $installerGuid) { $identitySeen = $true }
                                $version = [string]$entry.GetValue('DisplayVersion', '')
                                $command = [string]$entry.GetValue('UninstallString', '')
                                $key = "$hive|$name|$location|$version|$command"
                                $found[$key] = [pscustomobject]@{ Hive = [string]$hive; Guid = $name; Directory = $location; Version = $version; Uninstall = $command }
                            } finally { $entry.Dispose() }
                        }
                    } finally { $uninstall.Dispose() }
                }
                if ($location -and -not $identitySeen) {
                    $found["$hive|orphan|$location"] = [pscustomobject]@{ Hive = [string]$hive; Guid = $installerGuid; Directory = $location; Version = ''; Uninstall = '' }
                }
            } finally { $base.Dispose() }
        }
    }
    return @($found.Values)
}

function Invoke-TestProcess([string]$File, [string]$Arguments, [hashtable]$Overrides = @{}, [int]$TimeoutSeconds = 180) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $File
    $info.Arguments = $Arguments
    $info.UseShellExecute = $false
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.CreateNoWindow = $true
    foreach ($name in @($info.Environment.Keys)) {
        if ($name -match '(?i)KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL' -or $name -eq 'ELECTRON_RUN_AS_NODE' -or $name -eq 'AINOVEL_APPEARANCE_SCREENSHOTS') {
            [void]$info.Environment.Remove($name)
        }
    }
    foreach ($name in $Overrides.Keys) { $info.Environment[$name] = [string]$Overrides[$name] }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    try {
        Assert-True ($process.Start()) "Could not start $([IO.Path]::GetFileName($File))"
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $process.Kill($true)
            $process.WaitForExit()
            throw "$([IO.Path]::GetFileName($File)) timed out after $TimeoutSeconds seconds."
        }
        $output = $stdout.GetAwaiter().GetResult()
        $errors = $stderr.GetAwaiter().GetResult()
        Assert-True ($process.ExitCode -eq 0) "$([IO.Path]::GetFileName($File)) exited $($process.ExitCode): $errors`n$output"
        return $output
    } finally { $process.Dispose() }
}

function Assert-Registration([string]$Version = '') {
    $entries = @(Read-Installations)
    Assert-True ($entries.Count -eq 1) "Expected one installation identity, found $($entries.Count): $($entries | ConvertTo-Json -Compress)"
    $entry = $entries[0]
    Assert-True ($entry.Hive -eq 'CurrentUser' -and $entry.Guid -eq $installerGuid) 'Installer changed identity or installation scope.'
    Assert-True ([IO.Path]::GetFullPath($entry.Directory).TrimEnd('\') -eq $installDirectory.TrimEnd('\')) "Installer did not retain the custom directory: $($entry.Directory)"
    if ($Version) { Assert-True ($entry.Version -eq $Version) "Expected registered version $Version, found $($entry.Version)" }
    Assert-True (Test-Path -LiteralPath (Join-Path $installDirectory "$productName.exe")) 'Installed application executable is missing.'
    Assert-True (Test-Path -LiteralPath (Join-Path $installDirectory 'resources/bin/ainovel-sidecar.exe')) 'Installed Go sidecar is missing.'
    return $entry
}

function Invoke-InstalledSmoke([string]$Phase) {
    $smokeRoot = Join-Path $root "smoke-$Phase"
    $environment = @{
        AINOVEL_DESKTOP_TEST_ROOT = $smokeRoot; AINOVEL_DESKTOP_TEST_PHASE = 'initial'
        HOME = (Join-Path $smokeRoot 'home'); USERPROFILE = (Join-Path $smokeRoot 'home')
        APPDATA = (Join-Path $smokeRoot 'config'); LOCALAPPDATA = (Join-Path $smokeRoot 'cache')
        HTTP_PROXY = ''; HTTPS_PROXY = ''; ALL_PROXY = ''; NO_PROXY = '127.0.0.1,localhost,::1'
    }
    foreach ($folder in @('home', 'config', 'cache')) { [void](New-Item -ItemType Directory -Path (Join-Path $smokeRoot $folder) -Force) }
    $output = Invoke-TestProcess (Join-Path $installDirectory "$productName.exe") '--smoke-test' $environment 120
    $line = @($output -split '\r?\n' | Where-Object { $_.StartsWith('AINOVEL_DESKTOP_TEST_RESULT ') })
    Assert-True ($line.Count -eq 1) 'Installed application did not report a completed smoke test.'
    $result = $line[0].Substring('AINOVEL_DESKTOP_TEST_RESULT '.Length) | ConvertFrom-Json
    Assert-True ($result.ok -and $result.sidecarStopped -and $result.sidecarShutdown.graceful -and -not $result.sidecarShutdown.forced) 'Installed application did not pass smoke or shut down cleanly.'
    Assert-True ([IO.Path]::GetFullPath($result.sqlitePath).StartsWith($smokeRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) 'Smoke test escaped its isolated data directory.'
    return $result
}

try {
    Assert-True (@(Read-Installations).Count -eq 0) 'An existing installation was detected; refusing to modify it.'
    $dataDirectories = @((Join-Path $env:APPDATA $metadata.name), (Join-Path $env:APPDATA $productName)) | Select-Object -Unique
    foreach ($folder in $dataDirectories) {
        Assert-True (-not (Test-Path -LiteralPath $folder)) "Existing app data detected; refusing to modify $folder"
    }
    [void](New-Item -ItemType Directory -Path $root)
    # NSIS /D must be the final argument and its value must not be quoted.
    [void](Invoke-TestProcess $previous "/S /currentuser /D=$installDirectory")
    $before = Assert-Registration
    Assert-True ([version](($before.Version -split '-')[0]) -lt [version](($ExpectedVersion -split '-')[0])) "Previous version $($before.Version) must precede $ExpectedVersion."
    $report.steps.Add(@{ name = 'install-previous-in-custom-directory'; ok = $true; version = $before.Version })

    foreach ($folder in $dataDirectories) {
        [void](New-Item -ItemType Directory -Path $folder)
        $ownedDataFolders.Add($folder)
        foreach ($relative in @('upgrade-preservation.txt', 'output/novel/chapters/chapter-1.md', 'preferences.json')) {
            $file = Join-Path $folder $relative
            [void](New-Item -ItemType Directory -Path ([IO.Path]::GetDirectoryName($file)) -Force)
            [IO.File]::WriteAllText($file, "Synthetic installer preservation fixture $([Guid]::NewGuid())")
            $sentinels.Add(@{ path = $file; sha256 = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash })
        }
    }

    # Deliberately omit /D. This exercises discovery of the previous InstallLocation.
    [void](Invoke-TestProcess $current '/S /currentuser')
    $after = Assert-Registration $ExpectedVersion
    Assert-True ($before.Guid -eq $after.Guid -and $before.Directory -eq $after.Directory) 'Upgrade created another installation identity or directory.'
    foreach ($sentinel in $sentinels) {
        Assert-True ((Test-Path -LiteralPath $sentinel.path) -and (Get-FileHash -LiteralPath $sentinel.path -Algorithm SHA256).Hash -eq $sentinel.sha256) "Upgrade changed user data: $($sentinel.path)"
    }
    $report.steps.Add(@{ name = 'upgrade-reuses-directory-and-preserves-user-data'; ok = $true; version = $after.Version; preservedFiles = $sentinels.Count })
    $smoke = Invoke-InstalledSmoke 'upgraded'
    Assert-True ($smoke.versions.app -eq $ExpectedVersion) "Installed runtime reports $($smoke.versions.app), expected $ExpectedVersion."
    $report.steps.Add(@{ name = 'installed-new-version-smoke'; ok = $true; result = $smoke })
    $report.ok = $true
} catch {
    $report.ok = $false
    $report.error = $_.Exception.Message
} finally {
    try {
        $registrations = @(Read-Installations)
        foreach ($registration in $registrations) {
            if ($registration.Directory -ne $installDirectory) { continue }
            $uninstaller = Join-Path $installDirectory "Uninstall $productName.exe"
            Assert-True (Test-Path -LiteralPath $uninstaller) 'Cannot clean up the test installation: uninstaller is missing.'
            [void](Invoke-TestProcess $uninstaller "/S /currentuser _?=$installDirectory")
            break
        }
        # Do not touch pre-existing installations: only assert cleanup if this
        # invocation actually created its temporary installation directory.
        if (Test-Path -LiteralPath $root) {
            Assert-True (@(Read-Installations | Where-Object { $_.Directory -eq $installDirectory }).Count -eq 0) 'Test installation registry entries remained after uninstall.'
            foreach ($folder in $ownedDataFolders) {
                $marker = $sentinels | Where-Object { $_.path -eq (Join-Path $folder 'upgrade-preservation.txt') } | Select-Object -First 1
                if ((Test-Path -LiteralPath $folder) -and $null -ne $marker -and (Test-Path -LiteralPath $marker.path)) {
                    Assert-True ((Get-FileHash -LiteralPath $marker.path -Algorithm SHA256).Hash -eq $marker.sha256) 'Test data marker changed; refusing to remove that folder.'
                    Remove-Item -LiteralPath $folder -Recurse -Force
                }
            }
            Remove-Item -LiteralPath $root -Recurse -Force
        }
        $report.cleanup = $true
    } catch {
        $report.ok = $false
        $report.cleanupError = $_.Exception.Message
    }
    $report.durationMs = [int]([DateTime]::UtcNow - $started).TotalMilliseconds
    [void](New-Item -ItemType Directory -Path ([IO.Path]::GetDirectoryName($reportFile)) -Force)
    $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $reportFile -Encoding utf8
}
if (-not $report.ok) { throw "Windows installer acceptance failed. Report: $reportFile`n$($report | ConvertTo-Json -Depth 3 -Compress)" }
Write-Host "PASS Windows installer upgrade; report: $reportFile"
