param(
  [Parameter(Mandatory = $true)][ValidatePattern('^https://[A-Za-z0-9._:/-]+$')][string]$Brain,
  [Parameter(Mandatory = $true)][ValidatePattern('^[A-Za-z0-9._-]+$')][string]$Worker,
  [Parameter(Mandatory = $true)][string]$TLSCA
)

$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw 'Install-Windows.ps1 must run as Administrator'
}

$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$packageDirectory = Split-Path -Parent $scriptDirectory
$sourceAgentd = Join-Path $packageDirectory 'bin\yufeng-agentd.exe'
$sourceRun = Join-Path $packageDirectory 'bin\yufeng-run.exe'
if (-not (Test-Path -LiteralPath $sourceAgentd -PathType Leaf) -or -not (Test-Path -LiteralPath $sourceRun -PathType Leaf)) {
  throw 'release package does not contain yufeng-agentd.exe and yufeng-run.exe'
}
if (-not (Test-Path -LiteralPath $TLSCA -PathType Leaf)) {
  throw 'TLS authority must be a regular file'
}

$installRoot = Join-Path $env:ProgramFiles 'YuFeng'
$stateDirectory = Join-Path $env:ProgramData 'YuFeng\agentd'
$binaryDirectory = Join-Path $installRoot 'bin'
New-Item -ItemType Directory -Force -Path $binaryDirectory, $stateDirectory | Out-Null
Copy-Item -LiteralPath $sourceAgentd, $sourceRun -Destination $binaryDirectory -Force
$installedCA = Join-Path $installRoot 'brain-ca.pem'
Copy-Item -LiteralPath $TLSCA -Destination $installedCA -Force

$acl = New-Object Security.AccessControl.DirectorySecurity
$acl.SetAccessRuleProtection($true, $false)
$systemRule = New-Object Security.AccessControl.FileSystemAccessRule('SYSTEM', 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
$adminRule = New-Object Security.AccessControl.FileSystemAccessRule('BUILTIN\Administrators', 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
$acl.AddAccessRule($systemRule)
$acl.AddAccessRule($adminRule)
Set-Acl -LiteralPath $stateDirectory -AclObject $acl

$installedAgentd = Join-Path $binaryDirectory 'yufeng-agentd.exe'
$enrollmentReceipt = Join-Path $stateDirectory 'enrollment.json'
$refreshState = Join-Path $stateDirectory 'worker-refresh'
if (-not (Test-Path -LiteralPath $enrollmentReceipt -PathType Leaf) -and -not (Test-Path -LiteralPath $refreshState -PathType Leaf)) {
  & $installedAgentd '-enroll' "-brain=$Brain" "-worker=$Worker" "-tls-ca=$installedCA" "-state-dir=$stateDirectory"
  if ($LASTEXITCODE -ne 0) {
    throw "worker enrollment failed with exit code $LASTEXITCODE"
  }
}
if (-not (Test-Path -LiteralPath $enrollmentReceipt -PathType Leaf) -and -not (Test-Path -LiteralPath $refreshState -PathType Leaf)) {
  throw 'worker enrollment did not create persistent state'
}

$arguments = @(
  "`"-brain=$Brain`"", "`"-worker=$Worker`"", "`"-tls-ca=$installedCA`"",
  "`"-state-dir=$stateDirectory`"", '"-activate"'
) -join ' '
$action = New-ScheduledTaskAction -Execute $installedAgentd -Argument $arguments
$trigger = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet -RestartCount 20 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
if (Get-ScheduledTask -TaskName 'YuFeng Agentd' -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName 'YuFeng Agentd' -ErrorAction SilentlyContinue
}
Register-ScheduledTask -TaskName 'YuFeng Agentd' -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
Start-ScheduledTask -TaskName 'YuFeng Agentd'
