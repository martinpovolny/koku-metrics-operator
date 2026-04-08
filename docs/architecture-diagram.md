# External Components Architecture

## System Overview

```mermaid
flowchart TB
    subgraph OpenShift_Cluster["OpenShift Cluster"]
        direction TB
        PVC[Persistent Volume Claim]
        Secret1[openshift-config/pull-secret]
        Secret2[Auth Secret -- basic/service-account]
        Prometheus[Prometheus/Thanos]
        ConfigMap[cluster-monitoring-config]
    end
    
    subgraph Operator["koku-metrics-operator"]
        direction TB
        Controller[MetricsConfigReconciler]
        Collector[PrometheusCollector]
        HTTPClient[Koku HTTP Client]
        Storage[Storage Manager]
    end
    
    subgraph External["External Systems"]
        direction TB
        KokuAPI[console.redhat.com API]
        ClusterVersion[ClusterVersion CR]
    end
    
    Controller -->|1. Collect Metrics| Collector
    Collector -->|Prometheus API| Prometheus
    Collector -.->|Auth Token| Secret1
    Collector -->|Test Connection| ConfigMap
    
    Controller -->|2. Generate Reports| Storage
    Storage -->|3. Package Files| PVC
    
    Controller -->|4. Validate Auth| HTTPClient
    HTTPClient -->|Bearer/Basic/SVC Account| Secret2
    HTTPClient -->|Upload Reports| KokuAPI
    Controller -->|5. Create Source| HTTPClient
    HTTPClient -->|POST to| KokuAPI
    
    Controller -.->|Read Cluster Info| ClusterVersion
```

## Component Details

### 1. Prometheus/Thanos Connection Flow

```mermaid
sequenceDiagram
    participant CR as MetricsConfig CR
    participant Ctrl as Controller
    participant Coll as Collector
    participant Prom as Prometheus/Thanos
    
    Note over CR,Ctrl: Reconciliation starts
    Ctrl->>Coll: GetPromConn()
    Coll->>Prom: Query "up" (test connection)
    
    alt Connection OK
        Coll->>Ctrl: Success
        Note over Ctrl: Set PrometheusConnected=true
    else Connection Failed
        Coll->>Coll: Retry with exponential backoff
        loop Up to 5 retries per query
            Coll->>Prom: QueryRange()
            alt Query Succeeds
                Coll-->>Ctrl: Return results
            else Query Fails
                Coll->>Coll: Increment retry count
                Note over Ctrl: Track in retryTracker
            end
        end
    end
```

### 2. Authentication Flow

```mermaid
flowchart LR
    subgraph TokenAuth["Token Authentication (Default)"]
        direction TB
        A[Read pull-secret] --> B[Extract .dockerconfigjson]
        B --> C[Get cloud.openshift.com token]
        C --> D[Use as Bearer Token]
    end
    
    subgraph BasicAuth["Basic Authentication (Deprecated)"]
        direction TB
        E[Read auth secret] --> F[Get username/password]
        F --> G[Validate every 24h]
    end
    
    subgraph ServiceAccount["Service Account Auth"]
        direction TB
        H[Read service-account secret] --> I[Get client_id/client_secret]
        I --> J[Exchange for token]
        J --> K[Validate every 24h]
    end
    
    TokenAuth -.->|Preferred| D
    BasicAuth -.->|Remove after 2024-12-31| G
```

### 3. Reporting Cycle

```mermaid
flowchart LR
    subgraph DataCollection["Data Collection Phase"]
        direction TB
        H2[Hour 02:00] --> H3[Hour 03:00]
        H3 --> H4[Hour 04:00]
        H4 --> H5[Hour 05:00]
    end
    
    subgraph Processing["Processing Phase"]
        direction TB
        PKG{Package files<br/>every 60m} --> UP
    end
    
    DataCollection ==> PKG
    PKG ==> UP[Upload to Koku]
```

### 4. Data Flow with External APIs

```mermaid
flowchart TD
    subgraph Prometheus_Tier["Prometheus Tier"]
        direction TB
        P1[Prometheus Query API]
        P2[Thanos Federation -- if enabled]
    end
    
    subgraph Koku_Tier["Koku Tier"]
        direction TB
        K1[Koku Ingest API]
        K2[Koku Integration API]
    end
    
    Controller -->|Query Range| P1
    P1 -.->|Queries via| P2
    P2 -->|Returns metrics| Controller
    
    Storage -->|POST multipart/form-data| K1
    K1 -->|202 Accepted| Controller
    
    Controller -->|POST sources API| K2
    K2 -->|Source Created| Controller
```

## External Dependencies

| Component | Purpose | Configuration Location |
|-----------|---------|----------------------|
| **Prometheus** | Metrics query endpoint | `spec.prometheusConfig.svcAddress` |
| **Thanos** | Long-term storage federation | Configured via Prometheus URL |
| **console.redhat.com** | Koku API for uploads | `spec.upload.apiURL` |
| **openshift-config/v1** | ClusterVersion CR | Auto-discovered |
| **ConfigMaps** | Monitoring config, TLS certs | `openshift-monitoring/cluster-monitoring-config` |
| **ServiceAccounts** | Token authentication | `openshift-config/pull-secret` |

## Environment Variables (External)

```bash
# Authentication
IN_CLUSTER=true/false          # Use in-cluster storage
WATCH_NAMESPACE=<ns>           # Namespace to watch (all if empty)

# Leader Election
LEADER_ELECTION_LEASE_DURATION=60s
LEADER_ELECTION_RENEW_DEADLINE=30s
LEADER_ELECTION_RETRY_PERIOD=5s

# Secrets Path
SECRET_ABSPATH=/path/to/secrets # Override default SA path
```

## Key External Interfaces

### Prometheus Query API
- **Endpoint**: Configured via `spec.prometheusConfig.svcAddress`
- **Auth**: ServiceAccount token (Bearer) or CA certificate
- **Queries**: Range queries for hourly metrics collection
- **Timeout**: Configurable via `spec.prometheusConfig.contextTimeout`

### Koku Upload API
- **Endpoint**: `spec.upload.apiURL + spec.upload.ingressAPIPath`
- **Auth**: Bearer token, Basic auth (deprecated), or ServiceAccount
- **Format**: Multipart form-data with tar.gz packages
- **Retry**: Exponential backoff on 4xx/5xx errors

### OpenShift Cluster APIs
- **ClusterVersion**: Read-only CR for cluster identity
- **ConfigMaps**: Monitoring configuration and certificates
- **Secrets**: Pull secret, auth credentials, service accounts
