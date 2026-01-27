package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	sdk "github.com/openshift-online/ocm-sdk-go"
	hypershiftv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
	"github.com/openshift/osdctl/pkg/k8s"
	"github.com/openshift/osdctl/pkg/printer"
	"github.com/openshift/osdctl/pkg/utils"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	workv1 "open-cluster-management.io/api/work/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type auditOpts struct {
	mgmtClusterID string
	output        string
	showOnly      string
	noHeaders     bool

	mgmtClient client.Client
}

type hostedClusterAuditInfo struct {
	ClusterID   string            `json:"cluster_id" yaml:"cluster_id"`
	ClusterName string            `json:"cluster_name" yaml:"cluster_name"`
	Namespace   string            `json:"namespace" yaml:"namespace"`
	CurrentSize string            `json:"current_size" yaml:"current_size"`
	Category    string            `json:"category" yaml:"category"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

type auditResults struct {
	MgmtClusterID     string                   `json:"mgmt_cluster_id" yaml:"mgmt_cluster_id"`
	TotalScanned      int                      `json:"total_scanned" yaml:"total_scanned"`
	NeedsLabelRemoval []hostedClusterAuditInfo `json:"needs_label_removal" yaml:"needs_label_removal"`
	ReadyForMigration []hostedClusterAuditInfo `json:"ready_for_migration" yaml:"ready_for_migration"`
	AlreadyConfigured []hostedClusterAuditInfo `json:"already_configured" yaml:"already_configured"`
	Errors            []auditError             `json:"errors,omitempty" yaml:"errors,omitempty"`
}

type auditError struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	Error     string `json:"error" yaml:"error"`
}

type migrateOpts struct {
	serviceClusterID string
	mgmtClusterID    string
	dryRun           bool
	skipConfirmation bool
	serviceClient    client.Client
	mgmtClient       client.Client
	ocmConn          *sdk.Connection
	mgmtClusterName  string
}

type migrationResult struct {
	ClusterID   string `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	VerifiedAt  string `json:"verified_at,omitempty"`
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "hcp-node-autoscaling",
		Short: "HCP node autoscaling audit and migration tool",
		Long: `A tool for auditing and migrating hosted clusters on ROSA HCP management clusters
for node autoscaling readiness.

Use the audit subcommand to analyze clusters and the migrate subcommand to perform
the actual migration.`,
	}

	rootCmd.AddCommand(newAuditCmd())
	rootCmd.AddCommand(newMigrateCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// newAuditCmd creates the audit subcommand for analyzing hosted clusters.
func newAuditCmd() *cobra.Command {
	opts := &auditOpts{}
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit hosted clusters on a management cluster for autoscaling migration readiness",
		Long: `Analyze all hosted clusters on a management cluster and categorize them based on their
autoscaling migration readiness. Clusters are categorized into:
- Group A: Needs annotation removal (have cluster-size-override annotation)
- Group B: Ready for migration (missing required autoscaling annotations)
- Already configured (have autoscaling annotations set)`,
		Example: `
  # Audit all hosted clusters on a management cluster
  hcp-node-autoscaling audit --mgmt-cluster-id mgmt-cluster-123

  # Show only clusters that need annotation removal
  hcp-node-autoscaling audit --mgmt-cluster-id mgmt-cluster-123 --show-only needs-removal

  # Export to JSON for scripting
  hcp-node-autoscaling audit --mgmt-cluster-id mgmt-cluster-123 --output json

  # Export to CSV for spreadsheet analysis
  hcp-node-autoscaling audit --mgmt-cluster-id mgmt-cluster-123 --output csv
`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.run(context.Background())
		},
	}

	cmd.Flags().StringVar(&opts.mgmtClusterID, "mgmt-cluster-id", "", "The management cluster ID to audit")
	cmd.Flags().StringVar(&opts.output, "output", "text", "Output format: text, json, yaml, csv")
	cmd.Flags().StringVar(&opts.showOnly, "show-only", "", "Filter results: needs-removal, ready-for-migration")
	cmd.Flags().BoolVar(&opts.noHeaders, "no-headers", false, "Skip headers in output (for text and csv formats)")
	_ = cmd.MarkFlagRequired("mgmt-cluster-id")

	return cmd
}

// newMigrateCmd creates the migrate subcommand for migrating clusters to autoscaling.
func newMigrateCmd() *cobra.Command {
	opts := &migrateOpts{}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Enable autoscaling for hosted clusters on a service cluster",
		Long: `Perform autoscaling migration for hosted clusters that are ready.

This command will:
1. Audit the management cluster to find clusters ready for migration
2. Display the list and ask for confirmation
3. Patch ManifestWork resources on the service cluster
4. Verify the annotations are synced to the management cluster
5. Report results`,
		Example: `
  # Migrate clusters with confirmation
  hcp-node-autoscaling migrate \
    --service-cluster-id svc-123 \
    --mgmt-cluster-id mgmt-456

  # Dry run to see what would be migrated
  hcp-node-autoscaling migrate \
    --service-cluster-id svc-123 \
    --mgmt-cluster-id mgmt-456 \
    --dry-run

  # Skip confirmation prompt (use with caution)
  hcp-node-autoscaling migrate \
    --service-cluster-id svc-123 \
    --mgmt-cluster-id mgmt-456 \
    --skip-confirmation`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.run(context.Background())
		},
	}

	cmd.Flags().StringVar(&opts.serviceClusterID, "service-cluster-id", "",
		"The service cluster ID where ManifestWork resources exist")
	cmd.Flags().StringVar(&opts.mgmtClusterID, "mgmt-cluster-id", "",
		"The management cluster ID to migrate")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false,
		"Preview changes without applying them")
	cmd.Flags().BoolVar(&opts.skipConfirmation, "skip-confirmation", false,
		"Skip confirmation prompt (use with caution)")

	_ = cmd.MarkFlagRequired("service-cluster-id")
	_ = cmd.MarkFlagRequired("mgmt-cluster-id")

	return cmd
}

// run executes the audit command to analyze hosted clusters for autoscaling readiness.
func (a *auditOpts) run(ctx context.Context) error {
	if err := utils.IsValidClusterKey(a.mgmtClusterID); err != nil {
		return err
	}

	validOutputs := map[string]bool{"text": true, "json": true, "yaml": true, "csv": true}
	if !validOutputs[a.output] {
		return fmt.Errorf("invalid output format '%s'. Valid options: text, json, yaml, csv", a.output)
	}

	if a.showOnly != "" {
		validFilters := map[string]bool{"needs-removal": true, "ready-for-migration": true}
		if !validFilters[a.showOnly] {
			return fmt.Errorf("invalid show-only filter '%s'. Valid options: needs-removal, ready-for-migration", a.showOnly)
		}
	}

	connection, err := utils.CreateConnection()
	if err != nil {
		return fmt.Errorf("failed to create OCM connection: %v", err)
	}
	defer connection.Close()

	cluster, err := utils.GetCluster(connection, a.mgmtClusterID)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %v", err)
	}

	isMC, err := utils.IsManagementCluster(cluster.ID())
	if err != nil {
		return fmt.Errorf("failed to verify if cluster is a management cluster: %v", err)
	}
	if !isMC {
		return fmt.Errorf("cluster %s is not a management cluster", cluster.ID())
	}

	a.mgmtClusterID = cluster.ID()

	fmt.Printf("Auditing management cluster: %s (%s)\n", cluster.Name(), cluster.ID())

	scheme := runtime.NewScheme()
	if err := hypershiftv1beta1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add hypershift scheme: %v", err)
	}

	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add core v1 scheme: %v", err)
	}

	mgmtClient, err := k8s.New(a.mgmtClusterID, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create management cluster client: %v", err)
	}
	a.mgmtClient = mgmtClient

	namespaces, err := a.listOcmNamespaces(ctx)
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %v", err)
	}

	fmt.Printf("Found %d OCM namespaces to audit (production and staging)\n", len(namespaces))

	results := &auditResults{
		MgmtClusterID:     a.mgmtClusterID,
		NeedsLabelRemoval: []hostedClusterAuditInfo{},
		ReadyForMigration: []hostedClusterAuditInfo{},
		AlreadyConfigured: []hostedClusterAuditInfo{},
		Errors:            []auditError{},
	}

	for _, ns := range namespaces {
		info, err := a.auditNamespace(ctx, ns.Name)
		if err != nil {
			results.Errors = append(results.Errors, auditError{
				Namespace: ns.Name,
				Error:     err.Error(),
			})
			continue
		}

		switch info.Category {
		case "needs-removal":
			results.NeedsLabelRemoval = append(results.NeedsLabelRemoval, *info)
		case "ready-for-migration":
			results.ReadyForMigration = append(results.ReadyForMigration, *info)
		case "already-configured":
			results.AlreadyConfigured = append(results.AlreadyConfigured, *info)
		}
	}

	results.TotalScanned = len(results.NeedsLabelRemoval) +
		len(results.ReadyForMigration) +
		len(results.AlreadyConfigured)

	if a.showOnly != "" {
		results = a.applyFilter(results)
	}

	return a.outputResults(results)
}

// listOcmNamespaces returns OCM production and staging namespaces from the management cluster.
func (a *auditOpts) listOcmNamespaces(ctx context.Context) ([]corev1.Namespace, error) {
	nsList := &corev1.NamespaceList{}
	if err := a.mgmtClient.List(ctx, nsList); err != nil {
		return nil, err
	}

	var filtered []corev1.Namespace
	ocmNamespacePattern := regexp.MustCompile(`^ocm-(production|staging)-[a-zA-Z0-9]+$`)

	for _, ns := range nsList.Items {
		if ocmNamespacePattern.MatchString(ns.Name) {
			filtered = append(filtered, ns)
		}
	}

	return filtered, nil
}

// auditNamespace analyzes a single namespace and returns audit information for the hosted cluster.
func (a *auditOpts) auditNamespace(ctx context.Context, namespace string) (*hostedClusterAuditInfo, error) {
	hc, err := a.getHostedClusterInNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}

	clusterID := hc.Labels["api.openshift.com/id"]
	currentSize := hc.Labels["hypershift.openshift.io/hosted-cluster-size"]

	category := a.categorizeCluster(hc)

	return &hostedClusterAuditInfo{
		ClusterID:   clusterID,
		ClusterName: hc.Name,
		Namespace:   namespace,
		CurrentSize: currentSize,
		Category:    category,
		Labels:      hc.Labels,
		Annotations: hc.Annotations,
	}, nil
}

// getHostedClusterInNamespace retrieves the HostedCluster resource from a namespace.
func (a *auditOpts) getHostedClusterInNamespace(ctx context.Context, namespace string) (*hypershiftv1beta1.HostedCluster, error) {
	hcList := &hypershiftv1beta1.HostedClusterList{}
	listOpts := []client.ListOption{client.InNamespace(namespace)}

	if err := a.mgmtClient.List(ctx, hcList, listOpts...); err != nil {
		return nil, err
	}

	if len(hcList.Items) == 0 {
		return nil, fmt.Errorf("no HostedCluster found")
	}

	if len(hcList.Items) > 1 {
		return nil, fmt.Errorf("found %d HostedClusters, expected 1", len(hcList.Items))
	}

	return &hcList.Items[0], nil
}

// categorizeCluster determines the migration category for a hosted cluster.
func (a *auditOpts) categorizeCluster(hc *hypershiftv1beta1.HostedCluster) string {
	if _, hasOverride := hc.Annotations["hypershift.openshift.io/cluster-size-override"]; hasOverride {
		return "needs-removal"
	}

	topology, hasTopology := hc.Annotations["hypershift.openshift.io/topology"]
	autoScaling, hasAutoScaling := hc.Annotations["hypershift.openshift.io/resource-based-cp-auto-scaling"]

	hasCorrectTopology := hasTopology && topology == "dedicated-request-serving-components"
	hasCorrectAutoScaling := hasAutoScaling && autoScaling == "true"

	if hasCorrectTopology && hasCorrectAutoScaling {
		return "already-configured"
	}

	return "ready-for-migration"
}

// applyFilter filters audit results based on the showOnly option.
func (a *auditOpts) applyFilter(results *auditResults) *auditResults {
	filtered := &auditResults{
		MgmtClusterID: results.MgmtClusterID,
		Errors:        results.Errors,
	}

	switch a.showOnly {
	case "needs-removal":
		filtered.NeedsLabelRemoval = results.NeedsLabelRemoval
		filtered.TotalScanned = len(results.NeedsLabelRemoval)
	case "ready-for-migration":
		filtered.ReadyForMigration = results.ReadyForMigration
		filtered.TotalScanned = len(results.ReadyForMigration)
	default:
		return results
	}

	return filtered
}

// outputResults formats and prints audit results in the specified output format.
func (a *auditOpts) outputResults(results *auditResults) error {
	switch a.output {
	case "json":
		return a.printJSONOutput(results)
	case "yaml":
		return a.printYAMLOutput(results)
	case "csv":
		return a.printCSVOutput(results)
	default:
		return a.printTextOutput(results)
	}
}

// printTextOutput prints audit results in human-readable text format.
func (a *auditOpts) printTextOutput(results *auditResults) error {
	fmt.Printf("\nManagement Cluster: %s\n", results.MgmtClusterID)
	fmt.Printf("Total Hosted Clusters Scanned: %d\n\n", results.TotalScanned)

	if len(results.NeedsLabelRemoval) > 0 {
		fmt.Printf("=== GROUP A: Needs Annotation Removal (%d clusters) ===\n", len(results.NeedsLabelRemoval))
		fmt.Println("These clusters have the cluster-size-override annotation that must be removed:")

		p := printer.NewTablePrinter(os.Stdout, 20, 1, 3, ' ')
		if !a.noHeaders {
			p.AddRow([]string{"CLUSTER ID", "CLUSTER NAME", "NAMESPACE", "CURRENT SIZE"})
		}

		sort.Slice(results.NeedsLabelRemoval, func(i, j int) bool {
			return results.NeedsLabelRemoval[i].ClusterID < results.NeedsLabelRemoval[j].ClusterID
		})

		for _, c := range results.NeedsLabelRemoval {
			p.AddRow([]string{c.ClusterID, c.ClusterName, c.Namespace, c.CurrentSize})
		}
		p.Flush()
		fmt.Println()
	}

	if len(results.ReadyForMigration) > 0 {
		fmt.Printf("=== GROUP B: Ready for Migration (%d clusters) ===\n", len(results.ReadyForMigration))
		fmt.Println("These clusters can be immediately migrated to autoscaling:")

		p := printer.NewTablePrinter(os.Stdout, 20, 1, 3, ' ')
		if !a.noHeaders {
			p.AddRow([]string{"CLUSTER ID", "CLUSTER NAME", "NAMESPACE", "CURRENT SIZE"})
		}

		sort.Slice(results.ReadyForMigration, func(i, j int) bool {
			return results.ReadyForMigration[i].ClusterID < results.ReadyForMigration[j].ClusterID
		})

		for _, c := range results.ReadyForMigration {
			p.AddRow([]string{c.ClusterID, c.ClusterName, c.Namespace, c.CurrentSize})
		}
		p.Flush()
		fmt.Println()
	}

	if a.showOnly == "" && len(results.AlreadyConfigured) > 0 {
		fmt.Printf("=== Already Configured (%d clusters) ===\n", len(results.AlreadyConfigured))
		fmt.Println("These clusters already have autoscaling annotations set:")

		p := printer.NewTablePrinter(os.Stdout, 20, 1, 3, ' ')
		if !a.noHeaders {
			p.AddRow([]string{"CLUSTER ID", "CLUSTER NAME", "NAMESPACE", "CURRENT SIZE"})
		}

		sort.Slice(results.AlreadyConfigured, func(i, j int) bool {
			return results.AlreadyConfigured[i].ClusterID < results.AlreadyConfigured[j].ClusterID
		})

		for _, c := range results.AlreadyConfigured {
			p.AddRow([]string{c.ClusterID, c.ClusterName, c.Namespace, c.CurrentSize})
		}
		p.Flush()
		fmt.Println()
	}

	if len(results.Errors) > 0 {
		fmt.Printf("=== Errors (%d) ===\n", len(results.Errors))
		p := printer.NewTablePrinter(os.Stdout, 30, 1, 3, ' ')
		p.AddRow([]string{"NAMESPACE", "ERROR"})
		for _, e := range results.Errors {
			p.AddRow([]string{e.Namespace, e.Error})
		}
		p.Flush()
		fmt.Println()
	}

	fmt.Println("Summary:")
	fmt.Printf("  - Group A (Needs annotation removal): %d clusters\n", len(results.NeedsLabelRemoval))
	fmt.Printf("  - Group B (Ready for migration): %d clusters\n", len(results.ReadyForMigration))
	fmt.Printf("  - Already configured: %d clusters\n", len(results.AlreadyConfigured))
	fmt.Printf("  - Errors: %d namespaces\n", len(results.Errors))

	return nil
}

// printJSONOutput prints audit results in JSON format.
func (a *auditOpts) printJSONOutput(results *auditResults) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}

// printYAMLOutput prints audit results in YAML format.
func (a *auditOpts) printYAMLOutput(results *auditResults) error {
	data, err := yaml.Marshal(results)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// printCSVOutput prints audit results in CSV format.
func (a *auditOpts) printCSVOutput(results *auditResults) error {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()

	if !a.noHeaders {
		w.Write([]string{"cluster_id", "cluster_name", "namespace", "current_size", "category"})
	}

	allClusters := append(append(results.NeedsLabelRemoval, results.ReadyForMigration...), results.AlreadyConfigured...)
	for _, c := range allClusters {
		w.Write([]string{c.ClusterID, c.ClusterName, c.Namespace, c.CurrentSize, c.Category})
	}

	return nil
}

// run executes the migrate command to patch clusters with autoscaling annotations.
func (m *migrateOpts) run(ctx context.Context) error {
	if err := m.initialize(ctx); err != nil {
		return fmt.Errorf("initialization failed: %v", err)
	}
	defer m.ocmConn.Close()

	candidates, err := m.getCandidatesForMigration(ctx)
	if err != nil {
		return fmt.Errorf("failed to get migration candidates: %v", err)
	}

	if len(candidates) == 0 {
		fmt.Println("No clusters found ready for migration")
		return nil
	}

	m.displayCandidates(candidates)

	if !m.skipConfirmation && !m.dryRun {
		if !utils.ConfirmPrompt() {
			return fmt.Errorf("migration cancelled by user")
		}
	}

	if m.dryRun {
		fmt.Println("\n[DRY RUN] No changes will be applied")
		return nil
	}

	results := m.migrateClusters(ctx, candidates)

	m.displayResults(results)

	return nil
}

// initialize validates inputs and creates OCM connections and Kubernetes clients.
func (m *migrateOpts) initialize(ctx context.Context) error {
	if err := utils.IsValidClusterKey(m.serviceClusterID); err != nil {
		return fmt.Errorf("invalid service cluster ID: %v", err)
	}
	if err := utils.IsValidClusterKey(m.mgmtClusterID); err != nil {
		return fmt.Errorf("invalid management cluster ID: %v", err)
	}

	conn, err := utils.CreateConnection()
	if err != nil {
		return fmt.Errorf("failed to create OCM connection: %v", err)
	}
	m.ocmConn = conn

	serviceCluster, err := utils.GetCluster(conn, m.serviceClusterID)
	if err != nil {
		return fmt.Errorf("failed to get service cluster: %v", err)
	}

	mgmtCluster, err := utils.GetCluster(conn, m.mgmtClusterID)
	if err != nil {
		return fmt.Errorf("failed to get management cluster: %v", err)
	}

	isMC, err := utils.IsManagementCluster(mgmtCluster.ID())
	if err != nil {
		return fmt.Errorf("failed to verify management cluster: %v", err)
	}
	if !isMC {
		return fmt.Errorf("cluster %s is not a management cluster", mgmtCluster.ID())
	}

	m.serviceClusterID = serviceCluster.ID()
	m.mgmtClusterID = mgmtCluster.ID()
	m.mgmtClusterName = mgmtCluster.Name()

	fmt.Printf("Service Cluster: %s (%s)\n", serviceCluster.Name(), serviceCluster.ID())
	fmt.Printf("Management Cluster: %s (%s)\n", mgmtCluster.Name(), mgmtCluster.ID())
	fmt.Printf("ManifestWork Namespace: %s\n\n", m.mgmtClusterName)

	if err := m.createClients(ctx); err != nil {
		return err
	}

	return nil
}

// createClients initializes Kubernetes clients for service and management clusters.
// The service cluster client uses elevated permissions to patch ManifestWork resources.
func (m *migrateOpts) createClients(ctx context.Context) error {
	scheme := runtime.NewScheme()
	if err := hypershiftv1beta1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add hypershift scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add core v1 scheme: %v", err)
	}
	if err := workv1.Install(scheme); err != nil {
		return fmt.Errorf("failed to add work v1 scheme: %v", err)
	}

	elevationReason := "SREP-2821 - Migrating hosted clusters to node autoscaling"
	serviceClient, err := k8s.NewAsBackplaneClusterAdminWithConn(
		m.serviceClusterID,
		client.Options{Scheme: scheme},
		m.ocmConn,
		elevationReason,
	)
	if err != nil {
		return fmt.Errorf("failed to create service cluster client with elevated permissions: %v", err)
	}
	m.serviceClient = serviceClient

	mgmtClient, err := k8s.New(m.mgmtClusterID, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create management cluster client: %v", err)
	}
	m.mgmtClient = mgmtClient

	return nil
}

// getCandidatesForMigration audits the management cluster to find clusters ready for migration.
func (m *migrateOpts) getCandidatesForMigration(ctx context.Context) ([]hostedClusterAuditInfo, error) {
	auditOpts := &auditOpts{
		mgmtClusterID: m.mgmtClusterID,
		mgmtClient:    m.mgmtClient,
	}

	namespaces, err := auditOpts.listOcmNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Printf("Scanning %d namespaces for migration candidates...\n", len(namespaces))

	var candidates []hostedClusterAuditInfo

	for _, ns := range namespaces {
		info, err := auditOpts.auditNamespace(ctx, ns.Name)
		if err != nil {
			fmt.Printf("Warning: failed to audit namespace %s: %v\n", ns.Name, err)
			continue
		}

		if info.Category == "ready-for-migration" {
			candidates = append(candidates, *info)
		}
	}

	return candidates, nil
}

// migrateClusters migrates a list of candidate clusters by patching their ManifestWork resources.
func (m *migrateOpts) migrateClusters(ctx context.Context, candidates []hostedClusterAuditInfo) []migrationResult {
	results := make([]migrationResult, 0, len(candidates))

	for i, candidate := range candidates {
		fmt.Printf("\n[%d/%d] Migrating cluster %s (%s)...\n",
			i+1, len(candidates), candidate.ClusterName, candidate.ClusterID)

		result := m.migrateCluster(ctx, candidate)
		results = append(results, result)

		if result.Status == "success" {
			fmt.Printf("✓ Successfully migrated %s\n", candidate.ClusterID)
		} else {
			fmt.Printf("✗ Failed to migrate %s: %s\n", candidate.ClusterID, result.Error)
		}
	}

	return results
}

// migrateCluster migrates a single cluster by patching its ManifestWork and verifying sync.
func (m *migrateOpts) migrateCluster(ctx context.Context, info hostedClusterAuditInfo) migrationResult {
	result := migrationResult{
		ClusterID:   info.ClusterID,
		ClusterName: info.ClusterName,
	}

	if err := m.patchManifestWork(ctx, info.ClusterID); err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("failed to patch ManifestWork: %v", err)
		return result
	}

	fmt.Printf("  - Patched ManifestWork on service cluster\n")

	if err := m.waitForSync(ctx, info); err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("sync verification failed: %v", err)
		return result
	}

	result.Status = "success"
	result.VerifiedAt = time.Now().Format(time.RFC3339)
	return result
}

// patchManifestWork adds autoscaling annotations to the HostedCluster manifest in ManifestWork.
func (m *migrateOpts) patchManifestWork(ctx context.Context, clusterID string) error {
	manifestWork := &workv1.ManifestWork{}
	err := m.serviceClient.Get(ctx,
		types.NamespacedName{
			Name:      clusterID,
			Namespace: m.mgmtClusterName,
		},
		manifestWork)

	if err != nil {
		return fmt.Errorf("failed to get ManifestWork %s/%s: %v",
			m.mgmtClusterName, clusterID, err)
	}

	modified := false
	for i, manifest := range manifestWork.Spec.Workload.Manifests {
		if manifest.Raw == nil {
			continue
		}

		var manifestData map[string]interface{}
		if err := json.Unmarshal(manifest.Raw, &manifestData); err != nil {
			continue
		}

		kind, _ := manifestData["kind"].(string)
		if kind != "HostedCluster" {
			continue
		}

		metadata, ok := manifestData["metadata"].(map[string]interface{})
		if !ok {
			metadata = make(map[string]interface{})
			manifestData["metadata"] = metadata
		}

		annotations, ok := metadata["annotations"].(map[string]interface{})
		if !ok {
			annotations = make(map[string]interface{})
			metadata["annotations"] = annotations
		}

		annotations["hypershift.openshift.io/topology"] = "dedicated-request-serving-components"
		annotations["hypershift.openshift.io/resource-based-cp-auto-scaling"] = "true"

		jsonData, err := json.Marshal(manifestData)
		if err != nil {
			return fmt.Errorf("failed to marshal modified manifest: %v", err)
		}

		manifestWork.Spec.Workload.Manifests[i].Raw = jsonData
		modified = true
		break
	}

	if !modified {
		return fmt.Errorf("HostedCluster not found in ManifestWork manifests")
	}

	if err := m.serviceClient.Update(ctx, manifestWork); err != nil {
		return fmt.Errorf("failed to update ManifestWork: %v", err)
	}

	return nil
}

// waitForSync polls the management cluster until annotations sync or timeout occurs.
func (m *migrateOpts) waitForSync(ctx context.Context, info hostedClusterAuditInfo) error {
	const (
		pollInterval = 15 * time.Second
		timeout      = 5 * time.Minute
	)

	fmt.Printf("  - Waiting for sync (timeout: 5 minutes)...\n")

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled")
		case <-ticker.C:
			attempt++

			hc, err := m.getHostedClusterFromMgmt(ctx, info.Namespace, info.ClusterName)
			if err != nil {
				fmt.Printf("  - Attempt %d: failed to get HostedCluster: %v\n", attempt, err)

				if time.Now().After(deadline) {
					return fmt.Errorf("timeout waiting for sync after %v", timeout)
				}
				continue
			}

			if m.hasRequiredAnnotations(hc) {
				fmt.Printf("  - Verified: Annotations synced to management cluster\n")
				return nil
			}

			fmt.Printf("  - Attempt %d: Annotations not yet synced\n", attempt)

			if time.Now().After(deadline) {
				return fmt.Errorf("timeout: annotations did not sync after %v", timeout)
			}
		}
	}
}

// getHostedClusterFromMgmt retrieves a HostedCluster from the management cluster.
func (m *migrateOpts) getHostedClusterFromMgmt(ctx context.Context, namespace, name string) (*hypershiftv1beta1.HostedCluster, error) {
	hc := &hypershiftv1beta1.HostedCluster{}
	err := m.mgmtClient.Get(ctx,
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		hc)
	return hc, err
}

// hasRequiredAnnotations checks if a HostedCluster has the required autoscaling annotations.
func (m *migrateOpts) hasRequiredAnnotations(hc *hypershiftv1beta1.HostedCluster) bool {
	annotations := hc.Annotations
	if annotations == nil {
		return false
	}

	topology, hasTopology := annotations["hypershift.openshift.io/topology"]
	autoScaling, hasAutoScaling := annotations["hypershift.openshift.io/resource-based-cp-auto-scaling"]

	return hasTopology && topology == "dedicated-request-serving-components" &&
		hasAutoScaling && autoScaling == "true"
}

// displayCandidates prints the list of clusters ready for migration.
func (m *migrateOpts) displayCandidates(candidates []hostedClusterAuditInfo) {
	fmt.Printf("\n=== Clusters Ready for Migration (%d) ===\n\n", len(candidates))

	p := printer.NewTablePrinter(os.Stdout, 20, 1, 3, ' ')
	p.AddRow([]string{"CLUSTER ID", "CLUSTER NAME", "NAMESPACE", "CURRENT SIZE"})

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ClusterID < candidates[j].ClusterID
	})

	for _, c := range candidates {
		p.AddRow([]string{c.ClusterID, c.ClusterName, c.Namespace, c.CurrentSize})
	}
	p.Flush()
	fmt.Println()

	fmt.Println("These clusters will receive the following annotations:")
	fmt.Println("  - hypershift.openshift.io/topology: dedicated-request-serving-components")
	fmt.Println("  - hypershift.openshift.io/resource-based-cp-auto-scaling: \"true\"")
	fmt.Println()
}

// displayResults prints a summary of the migration results.
func (m *migrateOpts) displayResults(results []migrationResult) {
	var migrated, failed []migrationResult

	for _, r := range results {
		switch r.Status {
		case "success":
			migrated = append(migrated, r)
		case "failed":
			failed = append(failed, r)
		}
	}

	fmt.Printf("\n\n=== Migration Summary ===\n\n")
	fmt.Printf("Total candidates: %d\n", len(results))
	fmt.Printf("Successfully migrated: %d\n", len(migrated))
	fmt.Printf("Failed: %d\n\n", len(failed))

	if len(migrated) > 0 {
		fmt.Println("✓ Successfully Migrated:")
		for _, r := range migrated {
			fmt.Printf("  - %s (%s)\n", r.ClusterName, r.ClusterID)
		}
		fmt.Println()
	}

	if len(failed) > 0 {
		fmt.Println("✗ Failed Migrations:")
		p := printer.NewTablePrinter(os.Stdout, 20, 1, 3, ' ')
		p.AddRow([]string{"CLUSTER ID", "CLUSTER NAME", "ERROR"})
		for _, r := range failed {
			p.AddRow([]string{r.ClusterID, r.ClusterName, r.Error})
		}
		p.Flush()
		fmt.Println()
	}
}
