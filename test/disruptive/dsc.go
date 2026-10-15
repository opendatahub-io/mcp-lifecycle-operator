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

// Package disruptive holds destructive e2e tests that mutate cluster-wide,
// shared state (e.g. the OpenDataHub/RHOAI DataScienceCluster). They are gated
// behind the `disruptive` build tag and live in their own package so they are
// never compiled into, or run alongside, the regular `-tags=e2e` suite.
//
// DataScienceCluster (DSC) access lives in test/e2e/framework (dsc.go); this
// package reuses those helpers via thin, t-aware adapters and adds only the
// oracles specific to the toggle test: the MCPLifecycleOperator module CR
// teardown check and the operator controller-manager pod lifecycle checks.
// Nothing here imports opendatahub-operator.
package disruptive

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
)

const (
	// moduleGroup / moduleKind identify the cluster-scoped MCPLifecycleOperator
	// module CR reconciled by the ODH operator.
	moduleGroup = "components.platform.opendatahub.io"
	moduleKind  = "MCPLifecycleOperator"

	// pollInterval is the shared poll cadence for the wait helpers.
	pollInterval = 3 * time.Second
)

// setMCPLOManagementState patches the DSC mcplifecycleoperator managementState
// via the shared framework helper, failing the test on error.
func setMCPLOManagementState(ctx context.Context, t *testing.T, c ctrlclient.Client, dscName, state string) {
	t.Helper()
	if err := f.SetMCPLOManagementState(ctx, c, dscName, state); err != nil {
		t.Fatalf("failed to set DSC %s %s.managementState=%s: %v", dscName, f.DSCComponentMCPLO, state, err)
	}
	t.Logf("set DSC %s spec.components.%s.managementState=%s", dscName, f.DSCComponentMCPLO, state)
}

// waitDSCComponentStatus waits until the DSC status reflects the expected
// mcplifecycleoperator managementState via the shared framework helper, failing
// the test on timeout.
func waitDSCComponentStatus(ctx context.Context, t *testing.T, c ctrlclient.Client, dscName, expected string, timeout time.Duration) {
	t.Helper()
	if err := f.WaitForDSCComponentManagementState(ctx, c, dscName, expected, timeout); err != nil {
		t.Fatalf("timed out after %s waiting for DSC %s status.components.%s.managementState=%s: %v",
			timeout, dscName, f.DSCComponentMCPLO, expected, err)
	}
	t.Logf("DSC %s status.components.%s.managementState settled to %s", dscName, f.DSCComponentMCPLO, expected)
}

// resolveMapping returns the preferred REST mapping for a GroupKind, or nil if
// the kind is not registered on the cluster (CRD not installed).
func resolveMapping(c ctrlclient.Client, group, kind string) (*meta.RESTMapping, error) {
	mapping, err := c.RESTMapper().RESTMapping(schema.GroupKind{Group: group, Kind: kind})
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return mapping, nil
}

// waitModuleCRGone polls until no MCPLifecycleOperator module CR remains on the
// cluster. It lists by kind (rather than getting a fixed name) so it does not
// assume the singleton's name and cannot false-pass on a name mismatch. It is a
// best-effort independent oracle: when the module CRD is not installed the check
// is skipped. Transient list errors are tolerated and retried until timeout.
func waitModuleCRGone(ctx context.Context, t *testing.T, c ctrlclient.Client, timeout time.Duration) {
	t.Helper()
	mapping, err := resolveMapping(c, moduleGroup, moduleKind)
	if err != nil {
		t.Fatalf("failed to resolve %s mapping: %v", moduleKind, err)
	}
	if mapping == nil {
		t.Logf("%s CRD not installed, skipping module CR teardown check", moduleKind)
		return
	}
	listGVK := mapping.GroupVersionKind.GroupVersion().WithKind(moduleKind + "List")
	err = wait.For(func(ctx context.Context) (bool, error) {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK)
		if err := c.List(ctx, list); err != nil {
			// Transient (throttling/unavailability); retry on the next tick.
			return false, nil
		}
		return len(list.Items) == 0, nil
	},
		wait.WithTimeout(timeout),
		wait.WithInterval(pollInterval),
		wait.WithContext(ctx),
	)
	if err != nil {
		t.Fatalf("timed out after %s waiting for all %s module CRs to be deleted: %v", timeout, moduleKind, err)
	}
	t.Logf("no %s module CRs remain", moduleKind)
}

// resolveOperatorNamespace determines the namespace the controller-manager runs
// in, independent of the framework's cached operator discovery (which can fall
// back to the wrong default when the operator is down at discovery time). It
// prefers the MCPLO_NAMESPACE override and otherwise waits for a Running
// controller-manager pod cluster-wide and returns its namespace. It therefore
// doubles as the "operator is up" precondition check.
func resolveOperatorNamespace(ctx context.Context, t *testing.T, cfg *envconf.Config, timeout time.Duration) string {
	t.Helper()
	if ns := os.Getenv("MCPLO_NAMESPACE"); ns != "" {
		waitOperatorPodRunning(ctx, t, cfg, ns, timeout)
		return ns
	}

	var namespace string
	err := wait.For(func(ctx context.Context) (bool, error) {
		var pods corev1.PodList
		if err := cfg.Client().Resources().List(ctx, &pods, resources.WithLabelSelector(f.OperatorPodSelector)); err != nil {
			// Transient list error; retry on the next tick.
			return false, nil
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
				namespace = p.Namespace
				return true, nil
			}
		}
		return false, nil
	},
		wait.WithTimeout(timeout),
		wait.WithInterval(pollInterval),
		wait.WithContext(ctx),
	)
	if err != nil {
		t.Fatalf("timed out after %s locating a Running operator pod (selector %q); set MCPLO_NAMESPACE if the operator is deployed elsewhere: %v",
			timeout, f.OperatorPodSelector, err)
	}
	return namespace
}

// operatorPodsPresent reports whether any pod matching the operator selector
// still exists in the namespace, regardless of phase or deletion state. Used by
// the teardown oracle so a Terminating pod still counts as present.
func operatorPodsPresent(ctx context.Context, cfg *envconf.Config, namespace string) (bool, error) {
	r := cfg.Client().Resources(namespace)
	var pods corev1.PodList
	if err := r.List(ctx, &pods, resources.WithLabelSelector(f.OperatorPodSelector)); err != nil {
		return false, err
	}
	return len(pods.Items) > 0, nil
}

// operatorPodRunning reports whether at least one Running, non-terminating
// operator pod exists in the namespace.
func operatorPodRunning(ctx context.Context, cfg *envconf.Config, namespace string) (bool, error) {
	r := cfg.Client().Resources(namespace)
	var pods corev1.PodList
	if err := r.List(ctx, &pods, resources.WithLabelSelector(f.OperatorPodSelector)); err != nil {
		return false, err
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning && pods.Items[i].DeletionTimestamp == nil {
			return true, nil
		}
	}
	return false, nil
}

// waitOperatorPodsGone polls until no operator pod (any phase, including
// Terminating) remains in namespace. Transient list errors are tolerated and
// retried until timeout rather than failing the test on a single blip.
func waitOperatorPodsGone(ctx context.Context, t *testing.T, cfg *envconf.Config, namespace string, timeout time.Duration) {
	t.Helper()
	err := wait.For(func(ctx context.Context) (bool, error) {
		present, err := operatorPodsPresent(ctx, cfg, namespace)
		if err != nil {
			return false, nil
		}
		return !present, nil
	},
		wait.WithTimeout(timeout),
		wait.WithInterval(pollInterval),
		wait.WithContext(ctx),
	)
	if err != nil {
		t.Fatalf("timed out after %s waiting for operator pods to disappear from %s: %v", timeout, namespace, err)
	}
	t.Logf("operator controller-manager pods torn down in %s", namespace)
}

// waitOperatorPodRunning polls until at least one Running operator pod exists in
// namespace. Transient list errors are tolerated and retried until timeout.
func waitOperatorPodRunning(ctx context.Context, t *testing.T, cfg *envconf.Config, namespace string, timeout time.Duration) {
	t.Helper()
	err := wait.For(func(ctx context.Context) (bool, error) {
		running, err := operatorPodRunning(ctx, cfg, namespace)
		if err != nil {
			return false, nil
		}
		return running, nil
	},
		wait.WithTimeout(timeout),
		wait.WithInterval(pollInterval),
		wait.WithContext(ctx),
	)
	if err != nil {
		t.Fatalf("timed out after %s waiting for an operator pod to become Running in %s: %v", timeout, namespace, err)
	}
	t.Logf("operator controller-manager pod is Running in %s", namespace)
}
