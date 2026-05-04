# Koku Metrics Operator — Flow Diagrams

## System Components — In-cluster

```mermaid
graph TB
    subgraph cluster["OpenShift Cluster"]
        kube_api["kube-apiserver\nClusterVersion · Secrets · CR"]

        prom["Prometheus\nopenshift-monitoring ns"]
        thanos["thanos-querier :9091\nopenshift-monitoring ns"]

        op["koku-metrics-operator\nkoku-metrics-operator ns"]
        pvc["PVC / emptyDir\nkoku-metrics-operator ns\nreports/  upload/"]

        secrets["Secrets\npull-secret in openshift-config ns — token auth (default)\nclient_id + client_secret in operator ns — service-account auth\nusername + password in operator ns — basic auth (deprecated)"]

        prom -->|"federation"| thanos
        kube_api -->|"CR + ClusterVersion"| op
        op -->|"PromQL hourly"| thanos
        op -->|"read/write"| pvc
        op -->|"read auth credentials"| secrets
        op -->|"update status"| kube_api
    end
```

## System Components — External connections

```mermaid
graph TB
    op["koku-metrics-operator\n(inside OpenShift Cluster)"]

    sso["sso.redhat.com\n/openid-connect/token\nclient_credentials grant\n(service-account auth only)"]

    subgraph rh_cloud["console.redhat.com"]
        sources_api["Sources API\n/api/sources/v1.0/\nverify or create OCP integration\nevery 1440 min"]
        ingress_api["Ingress API\n/api/ingress/v1/upload\nPOST multipart tar.gz\nevery 360 min"]
    end

    koku["Koku backend\nparse CSVs · store to S3\ngenerate cost reports"]

    op -->|"SA auth: exchange\nclient_id+secret for\nBearer token"| sso
    sso -.->|"Bearer token"| op
    op -->|"source check\n(upload blocked until\nintegration exists)"| sources_api
    op -->|"upload tar.gz"| ingress_api
    ingress_api -->|"async"| koku
```

---

## Reconcile Loop

See [exploration-reconcile.md](./exploration-reconcile.md) for the full diagram and step-by-step description.

---

## Prometheus Collection → CSV Reports

For each hour in the time range the operator queries thanos-querier and writes one row per metric series to CSV files on the PVC.

```mermaid
flowchart LR
    subgraph Prometheus["Prometheus / thanos-querier"]
        P1["kube_* metrics\ncontainer_* metrics\nkubevirt_* metrics\nDCGM_FI_* metrics (NVIDIA DCGM)"]
    end

    subgraph Collector["internal/collector — per hour"]
        direction TB
        Q1["cost:* queries\n─────────────\nnodes, pods, namespaces\nstorage / PVC\nvirtual machines\nNVIDIA GPU"]
        Q2["ros:* queries\n─────────────\ncontainer CPU/memory\nrequest · limit · usage\nCPU throttle\nGPU utilisation\n(opt-in namespaces only)"]
    end

    subgraph CSVFiles["CSV files on PVC  reports/"]
        C1[cm-openshift-node-usage-*]
        C2[cm-openshift-pod-usage-*]
        C3[cm-openshift-namespace-usage-*]
        C4[cm-openshift-storage-usage-*]
        C5[cm-openshift-vm-usage-*]
        C6[cm-openshift-nvidia-gpu-usage-*]
        C7[ros-openshift-container-*]
        C8[ros-openshift-namespace-*]
    end

    P1 --> Q1 & Q2
    Q1 --> C1 & C2 & C3 & C4 & C5 & C6
    Q2 --> C7 & C8
```

ROS data collection is opt-in: namespaces must carry the label `insights_cost_management_optimizations=true` or `cost_management_optimizations=true`.

---

## Packaging & Upload Pipeline

```mermaid
flowchart LR
    A["CSV files\nreports/"] -->|PackageReports| B["TAR.GZ archives\nupload/\n+ manifest.json with UUID"]
    B -->|checkCycle\nevery UploadCycle min\ndefault 360 min| C{Source defined\nin console.redhat.com?}
    C -- no --> D[Hold files\nuntil integration\nexists]
    C -- yes --> E["POST multipart/form-data\nconsole.redhat.com\n/api/ingress/v1/upload"]
    E -->|HTTP 202| F[Delete local TAR.GZ\nupdate LastSuccessfulUploadTime]
    E -->|HTTP 401| G[Re-validate credentials\nupdate CR status error]
    E -->|other error| H[Keep file\nlog error\nupdate CR status]
```

---

## Authentication Flow

Three auth types are supported. The type is set in `spec.authentication.authType`.

```mermaid
flowchart TD
    A[setAuthentication] --> B{authType}

    B -- token --> C["Read openshift-config/pull-secret\n.dockerconfigjson → cloud.openshift.com\nBearer token string"]
    C --> D[No credential\nvalidation needed]
    D --> E([proceed to upload])

    B -- service-account\ndefault / recommended --> F["Read Secret\nclient_id + client_secret"]
    F --> G["POST /oauth/token\nspec.authentication.tokenURL\nget short-lived access token"]
    G --> E

    B -- basic\ndeprecated --> H["Read Secret\nusername + password"]
    H --> I["GET /api/sources/v3.1/sources\nvalidate credentials\ncached per checkCycle 1440 min"]
    I --> E
```

---

## Source (Integration) Check

Before uploading, the operator verifies that an integration (source) exists in console.redhat.com. This runs every `CheckCycle` minutes (default 1440 min = 24 h) or immediately when `spec.source` changes.

```mermaid
flowchart TD
    A[checkSource] --> B{Source name\nconfigured?}
    B -- no --> Z([skip])
    B -- yes --> C{CheckCycle elapsed\nor spec changed?}
    C -- no --> Z
    C -- yes --> D["GET /api/sources/v3.1/sources\n?filter name=sourceName"]
    D --> E{Found?}
    E -- yes --> F[SourceDefined = true\nupdate LastSourceCheckTime]
    E -- no --> G{CreateSource\nenabled?}
    G -- yes --> H["POST /api/sources/v3.1/sources\ncreate integration"]
    H --> F
    G -- no --> I[SourceDefined = false\nlog error]
```