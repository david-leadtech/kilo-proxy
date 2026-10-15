# Sign only with the persistent identity pinned in the repository. Never generate keys here.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [string]$Certificate = (Join-Path $PSScriptRoot '../certificates/kilo-proxy-windows.cer'),
    [switch]$VerifyOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$binaryPath = (Resolve-Path -LiteralPath $Binary).Path
$certificatePath = (Resolve-Path -LiteralPath $Certificate).Path
$expected = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new($certificatePath)
if ($expected.HasPrivateKey) { throw 'The pinned public certificate must not contain a private key.' }
if (-not $VerifyOnly -and $expected.NotAfter.ToUniversalTime() -le [DateTime]::UtcNow) { throw 'Signing certificate expired.' }

# Match the runner CPU where the SDK provides a native tool; x64 also runs on ARM64.
$toolArchitectures = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { @('arm64', 'x64') } else { @('x64') }
$signTool = $null
foreach ($architecture in $toolArchitectures) {
    $candidates = @(Get-ChildItem "${env:ProgramFiles(x86)}/Windows Kits/10/bin/*/$architecture/signtool.exe" -ErrorAction SilentlyContinue |
        Sort-Object FullName -Descending)
    if ($candidates.Count -gt 0) { $signTool = $candidates[0].FullName; break }
}
if (-not $signTool) { throw 'SignTool is missing; install the Windows SDK.' }

$temporaryPfx = $null
$addedRoot = $false
$rootPath = "Cert:\LocalMachine\Root\$($expected.Thumbprint)"
try {
    if (-not $VerifyOnly) {
        if (-not $env:KILO_WINDOWS_SIGNING_PFX -or -not $env:KILO_WINDOWS_SIGNING_PASSWORD) {
            throw 'Release signing requires KILO_WINDOWS_SIGNING_PFX and KILO_WINDOWS_SIGNING_PASSWORD.'
        }
        $temporaryPfx = Join-Path ([System.IO.Path]::GetTempPath()) ("kilo-signing-" + [guid]::NewGuid() + '.pfx')
        [System.IO.File]::WriteAllBytes($temporaryPfx, [Convert]::FromBase64String($env:KILO_WINDOWS_SIGNING_PFX))
        $privateCertificate = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new(
            $temporaryPfx, $env:KILO_WINDOWS_SIGNING_PASSWORD,
            [System.Security.Cryptography.X509Certificates.X509KeyStorageFlags]::EphemeralKeySet)
        try {
            if (-not $privateCertificate.HasPrivateKey -or $privateCertificate.Thumbprint -ne $expected.Thumbprint) {
                throw 'PFX does not match the pinned Kilo Proxy certificate.'
            }
        } finally {
            $privateCertificate.Dispose()
        }
        Write-Host 'Signing executable and requesting its RFC 3161 timestamp.'
        & $signTool sign /f $temporaryPfx /p $env:KILO_WINDOWS_SIGNING_PASSWORD /sha1 $expected.Thumbprint /fd SHA256 `
            /tr http://timestamp.digicert.com /td SHA256 /d 'Kilo Proxy' $binaryPath
        if ($LASTEXITCODE -ne 0) { throw "Signing or timestamp failed (exit $LASTEXITCODE)." }
    }

    # The user root store can display a native consent dialog, even in CI.
    # Hosted runners are administrators: use their machine store and remove our addition.
    # Only the public leaf is imported; never install its private key.
    if (-not (Test-Path $rootPath)) {
        Write-Host 'Temporarily trusting the public certificate for Authenticode verification.'
        $null = Import-Certificate -FilePath $certificatePath -CertStoreLocation Cert:\LocalMachine\Root
        $addedRoot = $true
    }
    Write-Host 'Verifying the timestamped Authenticode signature.'
    & $signTool verify /pa /all /tw $binaryPath
    if ($LASTEXITCODE -ne 0) { throw "Authenticode verification failed (exit $LASTEXITCODE)." }
    $signature = Get-AuthenticodeSignature -LiteralPath $binaryPath
    if ($signature.Status -ne 'Valid' -or $null -eq $signature.SignerCertificate -or
        $signature.SignerCertificate.Thumbprint -ne $expected.Thumbprint -or
        $null -eq $signature.TimeStamperCertificate) {
        throw 'Signature must be valid, timestamped, and made by the pinned Kilo Proxy certificate.'
    }
    Write-Host "Verified Kilo Proxy signature: $($expected.Thumbprint)"
} finally {
    if ($temporaryPfx) { Remove-Item -LiteralPath $temporaryPfx -Force -ErrorAction SilentlyContinue }
    if ($addedRoot) { Remove-Item -LiteralPath $rootPath -Force }
    $expected.Dispose()
}
