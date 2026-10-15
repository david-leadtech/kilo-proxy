# Windows release signing

Windows x64 and ARM64 release executables use the same dedicated, self-signed
Authenticode identity. The key is generated once, not by CI, and is not tied to a
release number. Reusing it lets an enterprise allow future versions with one
Defender certificate indicator.

## Public identity for IT

The public certificate is [kilo-proxy-windows.cer](../certificates/kilo-proxy-windows.cer).
It contains no private key and can be distributed to administrators.

- Subject: `CN=Kilo Proxy Release Signing`
- Valid until: **12 October 2036, 17:29:39 UTC**
- SHA-1 thumbprint: `3863C61C575422D1C0273C5A606D11AC115F6395`
- SHA-256 certificate fingerprint: `F6C747507F18C05B0DDCA5E564C0DA83165F22D0E89F01FBC092FF0006F337A9`
- RSA 3072-bit key; digital-signature usage and code-signing EKU; not a CA.

SHA-1 above identifies the certificate for Defender/Windows; executable signatures
and RFC 3161 timestamps use SHA-256.

IT needs to do both of the following:

1. Distribute this public certificate through the enterprise's managed certificate
   policy to the **Local Computer → Trusted Root Certification Authorities** store.
   For a controlled manual test, an elevated PowerShell session can use:

   ```powershell
   $certificate = Get-PfxCertificate -FilePath .\kilo-proxy-windows.cer
   if ($certificate.Thumbprint -ne '3863C61C575422D1C0273C5A606D11AC115F6395') {
       throw 'Unexpected Kilo Proxy certificate'
   }
   Import-Certificate -FilePath .\kilo-proxy-windows.cer -CertStoreLocation Cert:\LocalMachine\Root
   ```

2. In Microsoft Defender, open **Settings → Endpoints → Indicators → Certificates**,
   add this leaf certificate with action **Allow**, choose the appropriate device
   group and expiration, and allow policy propagation (up to three hours).

Microsoft requires a trusted certificate path even for self-signed certificate
indicators. Defender Antivirus must be active with cloud protection enabled.
An Allow certificate indicator does not override every independent application
control policy: IT should check any remaining SmartScreen, AppLocker or App
Control block separately. See Microsoft's [certificate indicator requirements](https://learn.microsoft.com/en-us/defender-endpoint/indicator-certificates)
and [policy conflict handling](https://learn.microsoft.com/en-us/defender-endpoint/indicator-file#policy-conflict-handling-for-file-indicators).

The indicator applies to code signed by this exact certificate. Keep its
expiration current. Renewal or emergency rotation requires deploying the new
certificate and updating the indicator; timestamps do not extend an Allow policy.

## CI behavior

Only release tags sign executables. Ordinary branch/PR artifacts remain unsigned,
and PR jobs do not receive production signing secrets. The Release workflow passes
these repository Actions secrets explicitly to the reusable build workflow:

- `KILO_WINDOWS_SIGNING_PFX`: base64-encoded, password-protected PFX.
- `KILO_WINDOWS_SIGNING_PASSWORD`: its password.

After all validation gates pass, each Windows package job signs its downloaded,
tested executable with `scripts/windows_sign.ps1`. It uses SHA-256 and a DigiCert
RFC 3161 timestamp. The script checks that the PFX matches the public certificate
pinned in the repository. Missing secrets, a different signer, timestamp failures,
or an invalid signature fail packaging, so the release cannot silently publish
unsigned Windows assets.

Signing occurs before ZIP creation and SHA-256 manifests. The workflow extracts
the final ZIP, verifies its timestamped Authenticode signature and pinned signer,
compares its bytes to the signed input, then runs the existing desktop archive
smoke. Signing never recompiles the executable.

The signing script removes its temporary PFX. It temporarily trusts only the
public certificate in the disposable runner user's root store for verification,
then removes that trust if it added it. It does not install the private key into
a persistent certificate store.

Both native Windows CI runners exercise signing, ZIP extraction, missing-secret
and signer-mismatch rejection, and tamper rejection with a disposable identity,
without production credentials. To run this check locally on Windows, set
`$env:KILO_TEST_WINDOWS_SIGNING = '1'` and run
`python -m unittest discover -s scripts -p test_windows_signing.py -v`.

## Backup and setup

The original private backup is outside the checkout at
`~/.config/kilo-proxy/windows-signing/`. Keep `signing.pfx`, `password.txt` and
`signing.cer` together in a secure backup. The PFX plus its password grants the
ability to sign software accepted by the enterprise rule. Never commit them or
send them to IT; IT only needs the public `.cer`.

The initial identity and Actions secrets were configured using:

```sh
python3 scripts/setup_windows_signing.py --upload-secrets
```

This requires Python, OpenSSL and an authenticated `gh` with permission to set
repository Actions secrets. Rerunning reuses the existing private identity and
checks it against the repository certificate. If the private backup is missing
but the public certificate exists, setup refuses to generate another identity.
Restore the original backup rather than deleting the certificate or generating
a key on each run. There is no guarantee of keeping the identity forever: it has
a fixed expiry, and a lost or compromised private key requires rotation.

To verify an extracted release on a Windows machine with the Windows SDK:

```powershell
./scripts/windows_sign.ps1 -VerifyOnly -Binary '.\Kilo Proxy.exe'
```

This enterprise signing identity does not provide public publisher trust outside
devices where its certificate has been deployed.
