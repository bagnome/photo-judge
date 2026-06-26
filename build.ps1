<#
.SYNOPSIS
  Build Photo Judge and self-sign the resulting photo-judge.exe.

.DESCRIPTION
  1. Builds photo-judge.exe (`go build`). Skip with -NoBuild to sign an existing exe.
  2. Finds or creates a reusable self-signed code-signing certificate in the
     CurrentUser\My store (matched by subject), so every build is signed by the SAME
     identity. The private key never leaves the Windows cert store — no .pfx on disk.
  3. Signs the exe with an Authenticode SHA-256 signature, adding a best-effort
     RFC-3161 timestamp (so the signature stays valid after the cert expires).
  4. Verifies and prints the result.

  With -Trust, the script also installs the certificate's PUBLIC part into this user's
  "Trusted Root" and "Trusted Publishers" stores, so the signature verifies as Valid on
  THIS machine. With -ExportCert it writes the public cert (.cer) next to the exe so you
  can install it on other machines (see the printed instructions).

.NOTES
  A self-signed certificate does NOT remove Windows SmartScreen / "unknown publisher"
  prompts on a machine that hasn't trusted it. Real removal needs a certificate from a
  trusted CA, OR this self-signed cert installed into Trusted Root + Trusted Publishers
  on each target PC (-Trust does that locally; -ExportCert helps you do it elsewhere).
  What self-signing always buys you: a stable publisher identity and a tamper-evident
  integrity signature on the binary.

.EXAMPLE
  .\build.ps1
  Build and sign with the default self-signed cert (created on first run).

.EXAMPLE
  .\build.ps1 -Trust
  Build, sign, and trust the cert on this machine so the signature reads "Valid" here.

.EXAMPLE
  .\build.ps1 -NoBuild -ExportCert
  Re-sign the existing exe and drop the public .cer next to it for distribution.
#>

[CmdletBinding()]
param(
  [string]$ExePath      = (Join-Path $PSScriptRoot 'photo-judge.exe'),
  [string]$Subject      = 'Photo Judge (self-signed)',
  [string]$TimestampUrl = 'http://timestamp.digicert.com',
  [int]   $ValidYears   = 5,
  [switch]$NoBuild,
  [switch]$Trust,
  [switch]$ExportCert
)

$ErrorActionPreference = 'Stop'

function Get-OrCreateCodeSigningCert {
  param([string]$Subject, [int]$ValidYears)
  $cn = "CN=$Subject"
  # Reuse a still-valid code-signing cert with this subject if one already exists.
  $existing = Get-ChildItem Cert:\CurrentUser\My |
    Where-Object {
      $_.Subject -eq $cn -and $_.HasPrivateKey -and $_.NotAfter -gt (Get-Date) -and
      ($_.EnhancedKeyUsageList | Where-Object { $_.ObjectId -eq '1.3.6.1.5.5.7.3.3' })  # Code Signing EKU
    } |
    Sort-Object NotAfter -Descending | Select-Object -First 1
  if ($existing) {
    Write-Host "Reusing certificate $($existing.Thumbprint) (expires $($existing.NotAfter.ToString('yyyy-MM-dd')))."
    return $existing
  }
  Write-Host "Creating self-signed code-signing certificate: $cn"
  return New-SelfSignedCertificate -Subject $cn -Type CodeSigningCert `
    -KeyAlgorithm RSA -KeyLength 2048 -HashAlgorithm SHA256 `
    -CertStoreLocation Cert:\CurrentUser\My -KeyUsage DigitalSignature `
    -KeyExportPolicy Exportable -NotAfter (Get-Date).AddYears($ValidYears)
}

function Install-CertTrusted {
  param([System.Security.Cryptography.X509Certificates.X509Certificate2]$Cert)
  # Import only the PUBLIC cert (no private key) into the user's trust stores. No admin
  # rights needed for CurrentUser. Idempotent: re-adding the same cert is a no-op.
  $pub = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new($Cert.Export('Cert'))
  foreach ($storeName in 'Root', 'TrustedPublisher') {
    $store = Get-Item "Cert:\CurrentUser\$storeName"
    $store.Open('ReadWrite'); $store.Add($pub); $store.Close()
    Write-Host "Trusted cert in CurrentUser\$storeName."
  }
}

Push-Location $PSScriptRoot
try {
  # 1. Build ----------------------------------------------------------------
  if (-not $NoBuild) {
    Write-Host "Building $ExePath ..."
    & go build -o $ExePath .
    if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)." }
  }
  if (-not (Test-Path $ExePath)) { throw "Executable not found: $ExePath (build it, or drop -NoBuild)." }

  # 2. Certificate ----------------------------------------------------------
  $cert = Get-OrCreateCodeSigningCert -Subject $Subject -ValidYears $ValidYears
  if ($Trust) { Install-CertTrusted -Cert $cert }

  # 3. Sign (timestamp is best-effort: fall back if the server is unreachable) --
  # NOTE: for a self-signed cert the returned Status is 'UnknownError' (untrusted root)
  # even on a fully successful sign+timestamp, so we must NOT treat Status as the success
  # signal here. A genuinely unreachable timestamp server throws (ErrorAction Stop) and is
  # caught below; whether a timestamp actually landed is confirmed in the verify step.
  Write-Host "Signing ..."
  try {
    Set-AuthenticodeSignature -FilePath $ExePath -Certificate $cert `
      -HashAlgorithm SHA256 -TimestampServer $TimestampUrl | Out-Null
  } catch {
    Write-Warning "Timestamp server $TimestampUrl unreachable ($($_.Exception.Message.Trim())); signing without a timestamp."
    Set-AuthenticodeSignature -FilePath $ExePath -Certificate $cert -HashAlgorithm SHA256 | Out-Null
  }

  # 4. Verify & report ------------------------------------------------------
  $v = Get-AuthenticodeSignature -FilePath $ExePath
  if (-not $v.SignerCertificate) {
    throw "Signing failed: no signature was applied (status: $($v.Status))."
  }
  $ts = if ($v.TimeStamperCertificate) { "yes ($($v.TimeStamperCertificate.Subject))" } else { 'no' }
  Write-Host ""
  Write-Host "  File      : $ExePath"
  Write-Host "  Signer    : $($v.SignerCertificate.Subject)"
  Write-Host "  Thumbprint: $($v.SignerCertificate.Thumbprint)"
  Write-Host "  Timestamp : $ts"
  Write-Host "  Status    : $($v.Status)"
  if ($v.Status -eq 'Valid') {
    Write-Host "Signed and trusted on this machine." -ForegroundColor Green
  } else {
    # Expected for a self-signed cert that hasn't been added to Trusted Root here.
    Write-Host "Signed. Status '$($v.Status)' means the signature is applied but this self-signed" -ForegroundColor Yellow
    Write-Host "publisher isn't trusted on this machine yet. Re-run with -Trust to trust it here." -ForegroundColor Yellow
  }

  # 5. Optional: export the public cert for installing on other machines -----
  if ($ExportCert) {
    $cerPath = Join-Path $PSScriptRoot 'photo-judge-codesign.cer'
    Export-Certificate -Cert $cert -FilePath $cerPath -Type CERT | Out-Null
    Write-Host ""
    Write-Host "Public certificate exported: $cerPath"
    Write-Host "To trust it on another PC (PowerShell as that user):"
    Write-Host "  Import-Certificate -FilePath .\photo-judge-codesign.cer -CertStoreLocation Cert:\CurrentUser\Root"
    Write-Host "  Import-Certificate -FilePath .\photo-judge-codesign.cer -CertStoreLocation Cert:\CurrentUser\TrustedPublisher"
  }
}
finally {
  Pop-Location
}
