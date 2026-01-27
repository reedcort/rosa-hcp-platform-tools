# HCP Node Autoscaling Migration Audit Tool

A standalone tool to audit hosted clusters on a management cluster for autoscaling migration readiness.

## Overview

This tool analyzes all hosted clusters on a ROSA HCP management cluster and categorizes them based on their readiness for autoscaling migration. It inspects cluster annotations and produces detailed reports.

## Installation

### From Source

```bash
git clone https://github.com/openshift-online/rosa-hcp-platform-tools.git
cd rosa-hcp-platform-tools/tools/hcp-node-autoscaling
go build -o hcp-node-autoscaling .
```

### Binary

The compiled binary will be created in the current directory as `hcp-node-autoscaling`.

## Usage

### Basic Audit

```bash
hcp-node-autoscaling --mgmt-cluster-id <MANAGEMENT_CLUSTER_ID>
```

### Output Formats

#### Text (Default)
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123
```

#### JSON
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --output json
```

#### YAML
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --output yaml
```

#### CSV
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --output csv > audit.csv
```

### Filtering Results

#### Show only clusters that need annotation removal
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --show-only needs-removal
```

#### Show only clusters ready for migration
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --show-only ready-for-migration
```

### Advanced Options

#### Skip headers (useful for piping to other tools)
```bash
hcp-node-autoscaling --mgmt-cluster-id mgmt-123 --output csv --no-headers
```

## Cluster Categories

The tool categorizes hosted clusters into three groups:

### Group A: Needs Annotation Removal

Clusters that have the `hypershift.openshift.io/cluster-size-override` annotation.

**Required Action**: Remove the `cluster-size-override` annotation before enabling autoscaling.

### Group B: Ready for Migration

Clusters that meet ALL conditions:
- Do NOT have `hypershift.openshift.io/cluster-size-override` annotation
- Missing one or both of the required autoscaling annotations:
  - `hypershift.openshift.io/topology: dedicated-request-serving-components`
  - `hypershift.openshift.io/resource-based-cp-auto-scaling: "true"`

**Required Action**: Add the missing autoscaling annotations to enable autoscaling.

### Already Configured

Clusters that have BOTH required autoscaling annotations properly set.

**Required Action**: None - autoscaling is already configured.

## Environment Support

The tool supports both production and staging environments:
- **Production**: Scans namespaces matching `ocm-production-${CLUSTER_ID}`
- **Staging**: Scans namespaces matching `ocm-staging-${CLUSTER_ID}`

Both environments are scanned automatically without additional configuration.

## Authentication

The tool uses the OCM SDK for authentication. Ensure you have:
1. Valid OCM credentials configured (via `ocm login`)
2. Access to the management cluster via backplane

## Example Output

### Text Format

```
Auditing management cluster: mgmt-cluster-prod (abc123def456)
Found 150 OCM namespaces to audit (production and staging)

Management Cluster: abc123def456
Total Hosted Clusters Scanned: 150

=== GROUP A: Needs Annotation Removal (5 clusters) ===
These clusters have the cluster-size-override annotation that must be removed:

CLUSTER ID           CLUSTER NAME         NAMESPACE                    CURRENT SIZE
cluster-001          prod-app-01          ocm-production-cluster-001   m54xl
cluster-002          staging-app-02       ocm-staging-cluster-002      m52xl
...

=== GROUP B: Ready for Migration (120 clusters) ===
These clusters can be immediately migrated to autoscaling:

CLUSTER ID           CLUSTER NAME         NAMESPACE                    CURRENT SIZE
cluster-003          prod-api-01          ocm-production-cluster-003   m52xl
cluster-004          staging-web-01       ocm-staging-cluster-004      m5xl
...

=== Already Configured (25 clusters) ===
These clusters already have autoscaling annotations set:

CLUSTER ID           CLUSTER NAME         NAMESPACE                    CURRENT SIZE
cluster-005          prod-db-01           ocm-production-cluster-005   large
cluster-006          staging-cache-01     ocm-staging-cluster-006      m52xl
...

Summary:
  - Group A (Needs annotation removal): 5 clusters
  - Group B (Ready for migration): 120 clusters
  - Already configured: 25 clusters
  - Errors: 0 namespaces
```

### JSON Format

```json
{
  "mgmt_cluster_id": "abc123def456",
  "total_scanned": 150,
  "needs_label_removal": [
    {
      "cluster_id": "cluster-001",
      "cluster_name": "prod-app-01",
      "namespace": "ocm-production-cluster-001",
      "current_size": "large",
      "category": "needs-removal",
      "annotations": {
        "hypershift.openshift.io/cluster-size-override": "large"
      }
    }
  ],
  "ready_for_migration": [...],
  "already_configured": [...],
  "errors": []
}
```

## Flags Reference

| Flag | Description | Default | Required |
|------|-------------|---------|----------|
| `--mgmt-cluster-id` | Management cluster ID to audit | - | Yes |
| `--output` | Output format: text, json, yaml, csv | text | No |
| `--show-only` | Filter: needs-removal, ready-for-migration | - | No |
| `--no-headers` | Skip headers in text/csv output | false | No |
| `-h, --help` | Show help message | - | No |

## Error Handling

The tool uses graceful degradation:
- If a namespace fails to audit, the error is recorded and the tool continues
- All errors are reported in the "Errors" section of the output
- Non-fatal errors: Missing HostedClusters, label read errors
- Fatal errors: K8s client creation, OCM connection, invalid management cluster

## Read-Only Operation

This tool performs **read-only** operations:
- Lists namespaces
- Reads HostedCluster resources
- Reads annotations and labels
- **Does NOT modify** any cluster resources

The tool uses non-elevated permissions (does not require cluster-admin).

## Dependencies

- OCM SDK (`github.com/openshift-online/ocm-sdk-go`)
- osdctl utilities (`github.com/openshift/osdctl/pkg`)
- HyperShift API (`github.com/openshift/hypershift/api`)
- Kubernetes client libraries
- Cobra CLI framework

## Contributing

This tool is part of the ROSA HCP Platform Tools repository. For issues or feature requests, please open an issue in the repository.

## License

See the LICENSE file in the root of the rosa-hcp-platform-tools repository.
