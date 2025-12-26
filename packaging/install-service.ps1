<#
.SYNOPSIS
  Register cf-ddns as a Windows scheduled task that starts at boot and stays
  running. Run from an elevated PowerShell prompt.

.DESCRIPTION
  Windows has no built-in supervisor for a plain console binary, so a scheduled
  task with a boot trigger and no execution time limit is the simplest way to
  run cf-ddns unattended. It runs as SYSTEM, restarts if it exits, and needs no
  extra software. If you prefer a real service entry, NSSM or WinSW can wrap the
  same binary instead.

.EXAMPLE
  .\install-service.ps1 -ExePath C:\cf-ddns\cf-ddns.exe -ConfigPath C:\cf-ddns\config.json
  .\install-service.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [string]$ConfigPath,
    [string]$TaskName = 'cf-ddns',
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "Removed scheduled task '$TaskName'"
    return
}

if (-not $ExePath)    { $ExePath    = Join-Path (Split-Path $PSScriptRoot -Parent) 'cf-ddns.exe' }
if (-not $ConfigPath) { $ConfigPath = Join-Path (Split-Path $ExePath -Parent) 'config.json' }

if (-not (Test-Path $ExePath))    { throw "binary not found: $ExePath" }
if (-not (Test-Path $ConfigPath)) { throw "config not found: $ConfigPath" }

$action = New-ScheduledTaskAction -Execute $ExePath `
    -Argument "--config `"$ConfigPath`"" -WorkingDirectory (Split-Path $ExePath -Parent)
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero)

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings -Force | Out-Null

Start-ScheduledTask -TaskName $TaskName
Write-Host "Installed and started scheduled task '$TaskName'"
Write-Host "  binary: $ExePath"
Write-Host "  config: $ConfigPath"
