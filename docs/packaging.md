# Packaging & Upload

## Packaging

Compresses CSV files from `reports/` into `upload/*.tar.gz` archives.

- Each archive contains CSV files that fit within `spec.packaging.maxSize` (default 100 MB)
- Includes `manifest.json` with UUID, cluster ID, creation timestamp, and file list
- Runs every `UploadCycle` minutes (default 360 min, minimum 60 min) or at end-of-day

### End-of-Day Handling

| Time | Behavior |
|------|----------|
| Hour 23 (end-of-day) | Files are **moved** out of `reports/` — no more appending |
| Other hours | Files are **copied** — originals remain for continued appending |

## Upload Flow

Skipped entirely when `spec.upload.uploadToggle` is `false` (disconnected clusters).

### Sequence

1. **Resolve credentials** — `setAuthentication()`
2. **Validate credentials** — `validateCredentials()` (skipped for token auth)
3. **Source check** — `checkSource()` (every 1440 min or when spec changes)
4. **Upload** — `uploadFiles()`

### Source Check

- Verifies OpenShift integration exists in console.redhat.com Sources API
- Runs every `CheckCycle` minutes (default 1440 min) or when `spec.source` changes
- If missing and `spec.source.createSource` is `true`, creates via `POST /api/sources/v1.0/sources`
- **Upload is blocked until a valid source exists** — files accumulate in `upload/`

### Upload

- POST `multipart/form-data` to `${APIURL}/api/ingress/v1/upload`
- Random jitter (`UploadWait`, 0–34 s) before first upload to spread load

### HTTP Response Handling

| Status | Action |
|--------|--------|
| 202 | Delete local tar.gz, record `LastSuccessfulUploadTime` |
| 401 | Re-validate credentials immediately |
| Other | Keep file, log error, update CR status |

## Trim Old Packages

After upload, if files in `upload/` exceed `spec.packaging.maxReports`, oldest archives are deleted.
