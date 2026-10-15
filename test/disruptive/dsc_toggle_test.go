//go:build disruptive

/*
Copyright 2026 The Kubernetes Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package disruptive

import (
	"context"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mcpv1beta1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1beta1"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/scenario"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
)

// TestMCPLifecycleOperatorDSCToggle exercises the disruptive DataScienceCluster
// (DSC) managementState toggle for the mcplifecycleoperator component
// (RHOAIENG-81829). It drives the component through Managed -> Removed -> Managed
// and verifies that the operator is torn down when Removed and is fully
// functional again once re-enabled.
//
// This is a destructive, cluster-wide operation: it mutates the shared singleton
// DSC. It therefore does not run in parallel and lives behind the `disruptive`
// build tag so it never mixes with the regular `-tags=e2e` suite. When no DSC is
// present (i.e. a non-ODH/RHOAI cluster) the test skips.
//
// Run with:
//
//	go test -tags=disruptive ./test/disruptive/ -v -count=1 -timeout 1h
//
// For deployments where the operator does not live in the upstream default
// namespace, set MCPLO_NAMESPACE (and optionally MCPLO_SA_NAME /
// MCPLO_METRICS_SERVICE) so operator discovery resolves correctly.
func TestMCPLifecycleOperatorDSCToggle(t *testing.T) {
	// Opt-out valve, symmetric to the framework's E2E_MANAGE_DSC: because this
	// destructive suite is also registered in the e2e test image and runs by
	// default, E2E_RUN_DISRUPTIVE=false lets an operator disable it for a run
	// without rebuilding the image.
	if os.Getenv("E2E_RUN_DISRUPTIVE") == "false" {
		t.Skip("E2E_RUN_DISRUPTIVE=false; skipping disruptive DSC toggle test")
	}

	// Captured across the Assess and Teardown steps. dscName stays empty when
	// the test skips, which signals Teardown to leave the cluster untouched.
	var (
		dscName     string
		opNamespace string
	)

	const (
		// statusTimeout bounds DSC status reconciliation after a spec patch.
		statusTimeout = 5 * time.Minute
		// operatorReadyTimeout bounds the operator controller-manager rollout,
		// which on re-enable may include a cold operator-image pull (the operator
		// image is deployed by the platform and cannot be prewarmed here). Matches
		// the framework's dscReadyWait.
		operatorReadyTimeout = 10 * time.Minute
		// workloadTimeout bounds teardown observations (pods/module CR gone), which
		// do not involve an image pull.
		workloadTimeout = 3 * time.Minute
	)

	feature := features.New("MCPLifecycleOperator DSC managementState toggle").
		WithLabel(category.Label, category.Resilience).
		WithLabel(speed.Label, speed.Slow).
		WithLabel(scenario.Label, scenario.Recovery).
		Assess("toggling the DSC component Removed then Managed leaves the operator fully functional",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				c := cfg.Client().Resources().GetControllerRuntimeClient()

				name, found, err := f.FindDataScienceCluster(ctx, c)
				if err != nil {
					t.Fatalf("failed to look up DataScienceCluster: %v", err)
				}
				if !found {
					t.Skip("no DataScienceCluster found; skipping ODH/RHOAI-only disruptive DSC toggle test")
				}
				dscName = name

				// Precondition: component Managed, then locate the operator by its
				// Running pod (independent of cached discovery, which can resolve a
				// stale fallback namespace when the operator is initially down).
				setMCPLOManagementState(ctx, t, c, dscName, f.StateManaged)
				waitDSCComponentStatus(ctx, t, c, dscName, f.StateManaged, statusTimeout)
				opNamespace = resolveOperatorNamespace(ctx, t, cfg, operatorReadyTimeout)
				t.Logf("disruptive DSC toggle: dsc=%q operatorNamespace=%q", dscName, opNamespace)

				// Disruptive step: toggle Removed and expect the operator to be
				// torn down. The DSC status field is reconciled by the platform and
				// is an independent oracle from the spec field we patched.
				setMCPLOManagementState(ctx, t, c, dscName, f.StateRemoved)
				waitDSCComponentStatus(ctx, t, c, dscName, f.StateRemoved, statusTimeout)
				waitModuleCRGone(ctx, t, c, workloadTimeout)
				waitOperatorPodsGone(ctx, t, cfg, opNamespace, workloadTimeout)

				// Recovery step: toggle back to Managed and expect the operator to
				// return.
				setMCPLOManagementState(ctx, t, c, dscName, f.StateManaged)
				waitDSCComponentStatus(ctx, t, c, dscName, f.StateManaged, statusTimeout)
				waitOperatorPodRunning(ctx, t, cfg, opNamespace, operatorReadyTimeout)

				// Functional proof: a fresh MCPServer reconciles end-to-end
				// (Available + Verified), proving the re-enabled operator is not
				// merely running but actually serving.
				ctx = f.SetupMCPServer(ctx, t, cfg, "post-toggle-server", false)
				server := f.ServerFromContext(ctx)
				r := cfg.Client().Resources()
				f.WaitForMCPServerReconciledAndReady(ctx, t, r, server)
				assertServerWorkloadReady(ctx, t, r, server)
				t.Log("operator is fully functional after re-enablement")

				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Remove the functional-test MCPServer (no-op when none was created).
			ctx = f.TeardownMCPServer(ctx, t, cfg)

			// Always leave the component Managed so a failure midway does not
			// strand the cluster with mcplifecycleoperator disabled. Skipped when
			// the test never located a DSC.
			if dscName != "" {
				c := cfg.Client().Resources().GetControllerRuntimeClient()
				setMCPLOManagementState(ctx, t, c, dscName, f.StateManaged)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, feature)
}

// assertServerWorkloadReady verifies the MCPServer's owned Deployment has a ready
// replica and its Service exists. Reading the workload objects directly is an
// oracle independent of the MCPServer's own status conditions.
func assertServerWorkloadReady(ctx context.Context, t *testing.T, r *resources.Resources, server *mcpv1beta1.MCPServer) {
	t.Helper()

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, server.Name, server.Namespace, dep); err != nil {
		t.Fatalf("Deployment %s/%s not found after re-enablement: %v", server.Namespace, server.Name, err)
	}
	if dep.Status.ReadyReplicas < 1 {
		t.Fatalf("Deployment %s/%s has %d ready replicas, want >= 1",
			server.Namespace, server.Name, dep.Status.ReadyReplicas)
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, server.Name, server.Namespace, svc); err != nil {
		t.Fatalf("Service %s/%s not found after re-enablement: %v", server.Namespace, server.Name, err)
	}

	t.Logf("MCPServer workload ready: Deployment readyReplicas=%d, Service %s present",
		dep.Status.ReadyReplicas, svc.Name)
}
