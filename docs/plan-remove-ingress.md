# Plan: Remove Ingress Service from On-Prem Upload Path

## Problem

Current on-prem data flow:

```
koku-metrics-operator
  → POST tar.gz to Ingress service (/api/ingress/v1/upload)
    → Ingress stores tar.gz in S3 (insights-upload-perma bucket)
    → Ingress publishes to Kafka (platform.upload.announce)
      → koku/masu listener downloads tar.gz from S3
        → processes and ingests cost data
```

The Ingress service is a cloud.redhat.com component — running it on-prem adds
complexity, an extra network hop, and a dependency on a service that provides
no value in the on-prem case (auth validation against cloud.redhat.com, routing
to multiple downstream consumers). The operator can write directly to S3 and
publish to Kafka itself.

Target on-prem flow:

```
koku-metrics-operator
  → PUT tar.gz to S3 (insights-upload-perma bucket, path: uploads/<uuid>.tar.gz)
  → Publish to Kafka (platform.upload.announce) with URL + b64_identity
    → koku/masu listener downloads tar.gz from S3
      → processes and ingests cost data
```

The Ingress service can then be removed from the cost-onprem-chart entirely.

---

## Repositories involved

| Repo | Changes |
|------|---------|
| `koku-metrics-operator` | New on-prem upload backend (S3 + Kafka) |
| `cost-onprem-chart` | Remove ingress templates; wire operator CR with S3/Kafka config |

No changes needed to `koku` — koku receives a standard `platform.upload.announce`
message and processes it identically to today.

---

## Part 1 — koku-metrics-operator

### 1.1  New dependencies

```
go get github.com/aws/aws-sdk-go-v2/aws
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/credentials
go get github.com/aws/aws-sdk-go-v2/service/s3
go get github.com/segmentio/kafka-go
go mod vendor
```

`segmentio/kafka-go` is chosen over `confluent-kafka-go` because it is pure Go
(no CGo), simpler to vendor, and sufficient for single-topic produce.

### 1.2  API types (`api/v1beta1/metricsconfig_types.go`)

Add a new `OnPremUploadSpec` struct and embed it in `UploadSpec`:

```go
// OnPremUploadSpec configures direct-to-S3 + Kafka upload, bypassing the
// Ingress service. Used only in on-prem deployments.
type OnPremUploadSpec struct {
    // Enabled switches the upload path from Ingress HTTP POST to direct S3+Kafka.
    // +kubebuilder:default=false
    Enabled bool `json:"enabled,omitempty"`

    // S3 holds the object storage endpoint where tarballs are written.
    S3 OnPremS3Spec `json:"s3,omitempty"`

    // Kafka holds the broker address and topic for upload announcements.
    Kafka OnPremKafkaSpec `json:"kafka,omitempty"`

    // OrgID is the organization identifier embedded in the Kafka message identity.
    // Defaults to the cluster ID when not set.
    OrgID string `json:"orgID,omitempty"`
}

type OnPremS3Spec struct {
    // Endpoint is the S3-compatible service host (e.g. s4.s4.svc.cluster.local).
    Endpoint string `json:"endpoint"`
    // Port is the S3 service port (default 7480 for S4/Ceph RGW).
    // +kubebuilder:default=7480
    Port int `json:"port,omitempty"`
    // UseSSL controls TLS for the S3 connection.
    // +kubebuilder:default=false
    UseSSL bool `json:"useSSL,omitempty"`
    // Bucket is the name of the bucket where tarballs are written.
    Bucket string `json:"bucket"`
    // CredentialsSecret is the name of a Secret in the operator namespace
    // with keys "access-key" and "secret-key".
    CredentialsSecret string `json:"credentialsSecret"`
}

type OnPremKafkaSpec struct {
    // Bootstrap is the Kafka bootstrap server (host:port).
    Bootstrap string `json:"bootstrap"`
    // Topic is the Kafka topic for upload announcements.
    // +kubebuilder:default="platform.upload.announce"
    Topic string `json:"topic,omitempty"`
}
```

Add `OnPrem OnPremUploadSpec` to `UploadSpec`.

Add a matching `OnPremUploadStatus` to `UploadStatus`:

```go
type OnPremUploadStatus struct {
    Enabled   bool   `json:"enabled,omitempty"`
    LastS3Key string `json:"lastS3Key,omitempty"`
}
```

Run `make generate manifests` after to regenerate CRD YAML.

### 1.3  S3 cleanup — two options

In the cloud flow, the Ingress service consumes `platform.upload.validation`
and deletes the staged S3 object once koku confirms success. Without Ingress,
the operator must handle cleanup itself. Two viable approaches:

---

#### Option A — Kafka validation consumer (acknowledgement-driven)

The operator subscribes to `platform.upload.validation` and deletes the S3
object for any `request_id` it recognizes as its own.

Validation message koku publishes (from `send_confirmation`):
```json
{"request_id": "<uuid>", "validation": "success|failure"}
```

Implementation:
- A goroutine started at operator startup consumes `platform.upload.validation`
  using a dedicated `kafka-go` consumer group.
- The operator keeps an in-memory map `uploadedKeys map[string]string`
  (`request_id → S3 key`) for files uploaded in the current session.
- On a `success` or `failure` message whose `request_id` is in the map,
  call `s3.DeleteObject` and remove the entry.
- Only runs when `cr.Spec.Upload.OnPrem.Enabled` is true.

Pros:
- Files deleted promptly after koku processes them
- Deletion is causally linked to koku confirming it received the payload

Cons:
- Goroutine adds complexity to operator lifecycle management
- In-memory map lost on restart; files uploaded before restart leak until
  a separate cleanup mechanism kicks in
- Requires the operator to know its own Kafka consumer group ID

---

#### Option B — Rolling retention (count-based, stateless) ✓ Recommended

After each successful S3 PUT, retain only the N most recent objects under the
`uploads/` prefix and delete the rest.

**New spec field** (in `OnPremS3Spec`):
```go
// MaxPayloads is the number of uploaded tarballs to retain in S3.
// After each upload, objects beyond this count (oldest by LastModified) are
// deleted. Default 10 gives roughly 10 × UploadCycle minutes of retention,
// ensuring koku has time to process before the file is removed.
// +kubebuilder:default=10
MaxPayloads int `json:"maxPayloads,omitempty"`
```

**Logic** (in `onpremupload.PruneOldPayloads`, called after every successful PUT):
```
list all objects with prefix "uploads/"
sort by LastModified ascending (oldest first)
if len(objects) > MaxPayloads:
    delete objects[0 : len(objects)-MaxPayloads]
```

Uses `s3.ListObjectsV2` + `s3.DeleteObjects` (batch delete, up to 1000/call).

Pros:
- No Kafka consumer goroutine
- Fully stateless — survives operator restarts
- State lives in S3, not memory
- Bounds S4 disk usage regardless of koku processing latency
- Operator restart or crash leaves at most `MaxPayloads` objects in S3

Cons:
- Deletion is time-based rather than acknowledgement-based; a very slow koku
  could theoretically have a file deleted before it finishes processing
  (mitigated by setting `MaxPayloads` ≥ `UploadCycle / processing_time`)
- Does not distinguish "koku processed successfully" from "koku hasn't got to
  it yet"

**Default `MaxPayloads=10`**: with the default `UploadCycle=360` minutes (6h),
this retains 60 hours of payloads — well beyond any realistic koku processing
delay in a healthy on-prem deployment.

---

**Decision: implement Option B first.** Option A can be added later if
fine-grained acknowledgement-driven cleanup is needed. The `MaxPayloads` field
is added to the spec regardless so users can tune retention.

### 1.4  New package: `internal/onpremupload/`

Single file `upload.go` with two exported functions and no global state:

```go
package onpremupload

// PutFile uploads the tar.gz at localPath to S3 under key uploads/<uuid>.tar.gz
// and returns the HTTP URL that koku can GET (cluster-internal, no auth required
// because koku pods have S3 credentials in their environment).
func PutFile(ctx context.Context, cfg S3Config, localPath string, uuid string) (string, error)

// Announce publishes a platform.upload.announce message to Kafka.
// b64Identity is constructed by BuildIdentity; url is the S3 HTTP URL from PutFile.
func Announce(ctx context.Context, cfg KafkaConfig, msg AnnounceMessage) error

// BuildIdentity constructs the base64-encoded x-rh-identity JSON that koku
// expects in the Kafka message, using clusterID as both org_id and account_number.
func BuildIdentity(clusterID, orgID string) string
```

`S3Config` and `KafkaConfig` are plain structs mirroring the spec fields plus
resolved credentials (populated by the controller from the Secret).

The S3 object key format: `uploads/<uuid>.tar.gz`
The S3 URL format: `http://<endpoint>:<port>/<bucket>/uploads/<uuid>.tar.gz`
  — directly GETtable by koku pods since all services share the same cluster.

Kafka message JSON (matches exactly what koku's `handle_message()` expects):

```json
{
  "request_id": "<uuid>",
  "account":    "<clusterID or orgID>",
  "org_id":     "<clusterID or orgID>",
  "category":   "tar",
  "url":        "http://<s3-endpoint>:<port>/<bucket>/uploads/<uuid>.tar.gz",
  "b64_identity": "<base64(identity JSON)>",
  "metadata":   {"reporter": "", "stale_timestamp": "0001-01-01T00:00:00Z"}
}
```

Kafka header: `service=hccm` (required for koku to route the message).

`b64_identity` JSON structure (same as what tests/utils.py constructs):

```json
{
  "org_id": "<orgID>",
  "identity": {
    "org_id": "<orgID>",
    "account_number": "<orgID>",
    "type": "User",
    "user": {
      "username": "metrics-operator",
      "email": "metrics-operator@cluster.local",
      "is_org_admin": true
    }
  },
  "entitlements": {
    "cost_management": {"is_entitled": true}
  }
}
```

### 1.4  Controller changes (`internal/controller/costmanagementmetricsconfig_controller.go`)

In `setUploadDefaults()` (around line 155): add `StringReflectSpec` call for the
new `OnPrem.Kafka.Topic` default.

Replace `uploadFiles()` (lines 588–635) with a branching implementation:

```go
func (r *MetricsConfigReconciler) uploadFiles(...) error {
    if cr.Spec.Upload.OnPrem.Enabled {
        return r.uploadFilesOnPrem(ctx, cr, dirCfg, packager, uploadFiles)
    }
    return r.uploadFilesIngress(authConfig, cr, dirCfg, packager, uploadFiles)
}
```

`uploadFilesIngress` is the existing logic, extracted verbatim.

`uploadFilesOnPrem` for each file:
1. Read S3 credentials from the Secret named in `cr.Spec.Upload.OnPrem.S3.CredentialsSecret`
2. Call `onpremupload.PutFile(...)` → get S3 URL
3. Construct `orgID`: `cr.Spec.Upload.OnPrem.OrgID`, falling back to `cr.Status.ClusterID`
4. Call `onpremupload.BuildIdentity(cr.Status.ClusterID, orgID)`
5. Call `onpremupload.Announce(...)` with the URL and identity
6. Update `cr.Status.Upload.*` fields (LastPayloadName, LastSuccessfulUploadTime, etc.)
7. Remove the local tar.gz on success (same as ingress path)

The Secret read uses the controller's existing client:
```go
secret := &corev1.Secret{}
r.Get(ctx, types.NamespacedName{
    Namespace: cr.Namespace,
    Name:      cr.Spec.Upload.OnPrem.S3.CredentialsSecret,
}, secret)
accessKey := string(secret.Data["access-key"])
secretKey := string(secret.Data["secret-key"])
```

### 1.5  RBAC

Add `secrets` `get` to the controller's ClusterRole (in `config/rbac/role.yaml`)
so it can read the credentials secret. Scope it to the operator namespace via
RoleBinding rather than ClusterRoleBinding if possible.

### 1.6  Tests

- Unit tests for `onpremupload.BuildIdentity` — verify output matches koku's expected format
- Unit tests for `PutFile` and `Announce` using mocks (S3 mock via `aws/smithy-go` transport
  interceptor; Kafka mock via in-process test server from `segmentio/kafka-go`)
- Controller integration test: mock onpremupload functions, verify `uploadFilesOnPrem`
  sets status fields correctly and removes the local file

---

## Part 2 — cost-onprem-chart

### 2.1  Remove ingress templates

Delete:
- `cost-onprem/templates/ingress/deployment.yaml`
- `cost-onprem/templates/ingress/service.yaml`
- `cost-onprem/templates/ingress/networkpolicy.yaml`

Remove `ingress:` section from `values.yaml`.

Update `docs/` references and the CLAUDE.md component table.

### 2.2  Add metricsOperator values

In `values.yaml`, add a new top-level section:

```yaml
# -----------------------------------------------------------------------------
# Koku Metrics Operator — CostManagementMetricsConfig CR
# -----------------------------------------------------------------------------
metricsOperator:
  # Namespace where the operator is deployed (typically same as chart namespace)
  namespace: cost-onprem

  # clusterID is auto-detected from the cluster's ClusterVersion; only set to
  # override (e.g. in dev environments without a real ClusterVersion resource).
  clusterID: ""

  # orgID embedded in the Kafka message identity. Defaults to clusterID.
  orgID: ""

  onPremUpload:
    enabled: true
    s3:
      # endpoint: resolved from the s4 service name at deploy time
      endpoint: "s4.{{ .Release.Namespace }}.svc.cluster.local"
      port: 7480
      useSSL: false
      bucket: "insights-upload-perma"
      # credentialsSecret: must exist before chart install (created by install-helm-chart.sh)
      credentialsSecret: "s4-credentials"
    kafka:
      bootstrap: "redpanda.kafka.svc.cluster.local:9092"
      topic: "platform.upload.announce"
```

### 2.3  New template: `CostManagementMetricsConfig` CR

Add `cost-onprem/templates/metrics-operator/costmanagementmetricsconfig.yaml`:

```yaml
{{- if .Values.metricsOperator.onPremUpload.enabled }}
apiVersion: costmanagement-metrics-cfg.openshift.io/v1beta1
kind: CostManagementMetricsConfig
metadata:
  name: costmanagementmetricsconfig
  namespace: {{ .Values.metricsOperator.namespace }}
spec:
  clusterID: {{ .Values.metricsOperator.clusterID | quote }}
  upload:
    upload_toggle: true
    onPrem:
      enabled: true
      orgID: {{ .Values.metricsOperator.orgID | quote }}
      s3:
        endpoint: {{ tpl .Values.metricsOperator.onPremUpload.s3.endpoint . | quote }}
        port: {{ .Values.metricsOperator.onPremUpload.s3.port }}
        useSSL: {{ .Values.metricsOperator.onPremUpload.s3.useSSL }}
        bucket: {{ .Values.metricsOperator.onPremUpload.s3.bucket | quote }}
        credentialsSecret: {{ .Values.metricsOperator.onPremUpload.s3.credentialsSecret | quote }}
      kafka:
        bootstrap: {{ .Values.metricsOperator.onPremUpload.kafka.bootstrap | quote }}
        topic: {{ .Values.metricsOperator.onPremUpload.kafka.topic | quote }}
{{- end }}
```

### 2.4  install-helm-chart.sh

The `s4-credentials` secret is already created by `deploy-s4-test.sh` in the S4
namespace (`s4-test`). The operator lives in `cost-onprem`. Options:

a) Cross-namespace secret reference — not supported natively; would need a
   copy job or ExternalSecret.

b) **Simpler**: after deploying S4, `install-helm-chart.sh` copies the secret
   into the operator's namespace:

```bash
kubectl get secret s4-credentials -n s4-test -o yaml \
  | sed "s/namespace: s4-test/namespace: cost-onprem/" \
  | kubectl apply -f -
```

c) Deploy S4 directly into `cost-onprem` namespace (simplest — no copy needed,
   `credentialsSecret: s4-credentials` just works).

**Recommendation: option (c)** — deploy S4 into the same namespace as the
chart. The `deploy-s4-test.sh` `NAMESPACE` parameter already supports this.
Update the default in `deploy-to-crc.sh` / `install-helm-chart.sh` accordingly.

### 2.5  Kafka topic creation

`deploy-redpanda.sh` already creates `platform.upload.announce`. No change needed.

### 2.6  Tests

Update `tests/suites/infrastructure/test_kafka.py` and
`tests/suites/api/test_ingress.py`:
- `test_ingress.py`: replace ingress HTTP upload test with a test that verifies
  the operator writes to S3 and the Kafka message appears
- `test_kafka.py`: add assertion that `platform.upload.announce` message arrives
  with a valid URL and `b64_identity` within the operator's upload cycle window

---

## Part 3 — Sequencing

```
1. Add Go dependencies (aws-sdk-go-v2, kafka-go) and vendor
2. Add API types + regenerate CRD manifests
3. Implement onpremupload package + unit tests
4. Wire controller (branch uploadFiles, add Secret read)
5. Update RBAC config
6. make generate manifests  (regenerates bundle/ and config/crd/)
7. --- operator PR ready ---
8. Update cost-onprem-chart values + add CR template
9. Remove ingress templates from chart
10. Update install-helm-chart.sh (S4 namespace / secret copy)
11. Update chart tests
12. --- chart PR ready ---
13. Deploy to foobar CRC and run full test suite
```

---

## Open questions

1. **S4 namespace**: resolved — S4 is not deployed by the chart. The chart expects
   S3 storage to pre-exist (ODF NooBaa, S4 via `deploy-s4-test.sh`, or AWS S3)
   and references it via `objectStorage.*` values. The credentials secret and
   endpoint are already in the chart namespace via `cost-onprem.storage.secretName`
   and `cost-onprem.storage.endpoint` helpers. The `CostManagementMetricsConfig`
   CR template reuses those same helpers — no secret copy needed.

2. **Operator deployment**: resolved — the Helm chart is a temporary solution;
   on-prem deployment will be managed by a dedicated operator in the future.
   Implications:
   - Don't over-engineer the chart wiring — it only needs to work well enough
     to validate the approach and run tests.
   - The `CostManagementMetricsConfig` CR template in the chart is temporary
     scaffolding; the future on-prem operator will own that CR.
   - The koku-metrics-operator API changes (new `OnPremUpload` spec) need to be
     solid and forward-compatible since they will outlive the chart.

3. **Upload cycle during transition**: while deploying the new operator version,
   old tarballs in the local PVC upload directory will be picked up by the new
   code. This is safe — they are valid tar.gz files with correct manifests.

4. **S3 cleanup**: resolved — Option B (rolling retention, `MaxPayloads=10`) is the
   implementation target. Option A (Kafka validation consumer) documented as future
   enhancement if acknowledgement-driven cleanup is later required.

5. **koku `download_payload` auth**: resolved — option (a). Set the
   `insights-upload-perma` bucket to public-read ACL after creation. koku's
   `download_payload` does a plain `requests.get(url)` with no credentials;
   the bucket is cluster-internal only (not exposed outside OpenShift), so
   the security posture is acceptable for on-prem.
   Implementation: add `aws s3api put-bucket-acl --acl public-read` to the
   bucket creation job in `install-helm-chart.sh` (after the existing
   `aws s3 mb` call).
