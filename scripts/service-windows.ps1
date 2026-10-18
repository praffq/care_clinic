# Install the CARE Clinic host service as a Windows service (developer install).
#
#   scripts\service-windows.ps1 install           build, stage QEMU and install; start at boot
#   scripts\service-windows.ps1 uninstall [-Purge] stop and remove (-Purge also
#                                                 deletes the engine data disk;
#                                                 %LOCALAPPDATA%\care-clinic\logs
#                                                 is always kept)
#   scripts\service-windows.ps1 status            service and engine status
#
# Run from a normal PowerShell as your admin account: install builds as you,
# then re-runs itself elevated (UAC) for the system steps. Needs Go and 7-Zip.
[CmdletBinding()]
param(
    [Parameter(Position = 0)][string]$Command,
    [Parameter(Position = 1)][string]$Payload,
    [Parameter(Position = 2)][string]$UserSid,
    [switch]$Purge
)
$ErrorActionPreference = 'Stop'

$service = 'CareClinic'
$group = 'care-clinic'
$root = Join-Path $env:ProgramData 'CARE Clinic'
$bin = Join-Path $root 'bin\care-clinic-service.exe'
$log = Join-Path $env:LOCALAPPDATA 'care-clinic\logs\service.log'
$repo = Split-Path -Parent $PSScriptRoot

function Die([string]$msg) {
    [Console]::Error.WriteLine("service-windows: $msg")
    exit 1
}

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Re-run this script elevated with the given arguments and wait for it.
function Invoke-Elevated([string[]]$Arguments) {
    $quoted = $Arguments | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }
    $argv = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`"") + $quoted
    $p = Start-Process powershell -Verb RunAs -Wait -PassThru -ArgumentList $argv
    if ($p.ExitCode -ne 0) { Die "the elevated step failed (exit code $($p.ExitCode))" }
}

function Invoke-Native([string]$exe, [string[]]$argv) {
    & $exe @argv
    if ($LASTEXITCODE -ne 0) { throw "$exe $($argv -join ' ') failed with exit code $LASTEXITCODE" }
}

function Install-Built {
    if (-not (Test-Admin)) { Die 'install-built needs an elevated prompt' }
    $exe = Join-Path $Payload 'care-clinic-service.exe'
    if (-not (Test-Path $exe)) { Die "no service in $Payload" }

    if (-not (Get-LocalGroup -Name $group -ErrorAction SilentlyContinue)) {
        Write-Host "creating group $group"
        New-LocalGroup -Name $group -Description 'CARE Clinic service users' | Out-Null
    }
    $sid = New-Object Security.Principal.SecurityIdentifier $UserSid
    try {
        Add-LocalGroupMember -Group $group -Member $sid
        Write-Host "adding $($sid.Translate([Security.Principal.NTAccount])) to $group"
    } catch [Microsoft.PowerShell.Commands.MemberExistsException] {}

    $whpx = (Get-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform).State
    if ($whpx -ne 'Enabled' -and $whpx -ne 'EnablePending') {
        Write-Host 'turning on the Windows Hypervisor Platform'
        $r = Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform -All -NoRestart
        if ($r.RestartNeeded) { $whpx = 'EnablePending' }
    }

    # Copies the service and qemu\ to $root\bin, registers the service and
    # opens the firewall; stops a running service first.
    Invoke-Native $exe @('service', 'install', '--group', $group, '--log-user', $UserSid)
    Invoke-Native $bin @('service', 'start')
    Write-Host "installed and started $service"
    if ($whpx -eq 'EnablePending') {
        Write-Warning 'Restart Windows to finish turning on the Windows Hypervisor Platform; the engine cannot start until then.'
    }
}

function Show-NextSteps {
    $ctl = "& '$bin' ctl"
    Write-Host ''
    Write-Host 'Next steps (sign out and in again first if you were just added to the care-clinic group):'
    Write-Host "  $ctl status"
    $tarball = Get-ChildItem (Join-Path $repo 'dist') -Filter 'care-engine-*-amd64.tar.gz' -ErrorAction SilentlyContinue |
        Sort-Object Name | Select-Object -Last 1
    if ($tarball) {
        $version = $tarball.Name -replace '^care-engine-', '' -replace '-amd64\.tar\.gz$', ''
        $sum = (Get-FileHash -Algorithm SHA256 $tarball.FullName).Hash.ToLower()
        Write-Host "  $ctl install '$($tarball.FullName)' $sum"
        Write-Host "  $ctl activate $version"
    } else {
        Write-Host '  engine/build.sh amd64     # on Linux or macOS; no engine bundle in dist\ yet'
        Write-Host "  $ctl install dist\care-engine-<version>-amd64.tar.gz <sha256>"
        Write-Host "  $ctl activate <version>"
    }
    Write-Host "  $ctl watch                 # until the engine is ready"
    Write-Host "  $ctl config mdns_name=<name>  # advertise <name>.local instead of care.local"
    Write-Host ''
    Write-Host "Logs: $log"
}

function Install-Cmd {
    if (Test-Admin) { Die 'run install from a normal (not elevated) prompt; it builds as you, then asks for elevation' }
    $build = Join-Path $repo 'dist\service-windows'
    New-Item -ItemType Directory -Force $build | Out-Null
    try {
        # bundle-windows builds the service and stages the pinned QEMU (cached
        # in dist\cache) and the newest dist\care-engine-*-amd64.tar.gz into
        # $build\payload.
        Invoke-Native go @('-C', (Join-Path $repo 'app'), 'run', './cmd/bundle-windows', (Join-Path $build 'CARE Clinic.exe'))
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        Invoke-Elevated @('install-built', (Join-Path $build 'payload'), $sid)
    } finally {
        Remove-Item -Recurse -Force (Join-Path $build 'payload') -ErrorAction SilentlyContinue
    }
    Show-NextSteps
}

function Uninstall-Cmd {
    if (-not (Test-Admin)) {
        $a = @('uninstall')
        if ($Purge) { $a += '-Purge' }
        Invoke-Elevated $a
        return
    }
    if (Test-Path $bin) {
        # Stops the service (CARE and the VM first), deletes it, its firewall
        # rules and $root\bin.
        Invoke-Native $bin @('service', 'uninstall')
    } elseif (Get-Service $service -ErrorAction SilentlyContinue) {
        Stop-Service $service -ErrorAction SilentlyContinue
        Invoke-Native sc.exe @('delete', $service)
    }
    Write-Host "removed $service (the $group group is kept)"
    if ($Purge) {
        Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
        Write-Host "purged $root (the logs in %LOCALAPPDATA%\care-clinic\logs are kept)"
    } else {
        Write-Host "kept engine data and slots in $root (use -Purge to delete them)"
    }
}

function Status-Cmd {
    $s = Get-Service $service -ErrorAction SilentlyContinue
    if ($s) {
        Write-Host "service: $($s.Status) (start type $($s.StartType))"
    } else {
        Write-Host 'service: not installed'
    }
    try {
        Write-Host "Windows Hypervisor Platform: $((Get-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform).State)"
    } catch {
        Write-Host 'Windows Hypervisor Platform: unknown (run status elevated to check)'
    }
    if (Test-Path $bin) {
        & $bin ctl status
    }
}

switch ($Command) {
    'install' { Install-Cmd }
    'install-built' {
        if (-not $Payload -or -not $UserSid) { Die 'usage: install-built <payload dir> <user SID>' }
        # This runs in its own elevated window: keep it open on failure.
        try { Install-Built } catch {
            Write-Host "service-windows: $_" -ForegroundColor Red
            Read-Host 'Press Enter to close' | Out-Null
            exit 1
        }
    }
    'uninstall' {
        try { Uninstall-Cmd } catch {
            Write-Host "service-windows: $_" -ForegroundColor Red
            if (Test-Admin) { Read-Host 'Press Enter to close' | Out-Null }
            exit 1
        }
    }
    'status' { Status-Cmd }
    default {
        Get-Content $PSCommandPath | Select-Object -Skip 1 -First 10 | ForEach-Object { [Console]::Error.WriteLine($_) }
        exit 2
    }
}
