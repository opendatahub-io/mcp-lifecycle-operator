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

package framework_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/e2e-framework/pkg/types"

	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
)

const (
	testDSCName  = "default-dsc"
	stateManaged = "Managed"
	stateRemoved = "Removed"
)

// ---------------------------------------------------------------------------
// fake client helpers
// ---------------------------------------------------------------------------

func newFakeClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithObjects(objects...).Build()
}

func newDSC(managementState string) *unstructured.Unstructured {
	dsc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "datasciencecluster.opendatahub.io/v2",
		"kind":       "DataScienceCluster",
		"metadata":   map[string]any{"name": testDSCName},
		"spec": map[string]any{
			"components": map[string]any{
				"mcplifecycleoperator": map[string]any{
					"managementState": managementState,
				},
			},
		},
	}}
	return dsc
}

func newDSCWithCondition(managementState, condType, condStatus string) *unstructured.Unstructured {
	dsc := newDSC(managementState)
	_ = unstructured.SetNestedSlice(dsc.Object, []any{
		map[string]any{
			"type":   condType,
			"status": condStatus,
		},
	}, "status", "conditions")
	return dsc
}

func getDSCManagementState(t *testing.T, ctx context.Context, cl client.Client) string {
	t.Helper()
	dsc := &unstructured.Unstructured{}
	dsc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "datasciencecluster.opendatahub.io",
		Version: "v2",
		Kind:    "DataScienceCluster",
	})
	if err := cl.Get(ctx, client.ObjectKey{Name: testDSCName}, dsc); err != nil {
		t.Fatalf("failed to get DSC: %v", err)
	}
	state, _, _ := unstructured.NestedString(dsc.Object, "spec", "components", "mcplifecycleoperator", "managementState")
	return state
}

type hookSpy struct {
	setupCount  int
	finishCount int
}

func (s *hookSpy) Setup(funcs ...types.EnvFunc) types.Environment {
	s.setupCount += len(funcs)
	return nil
}

func (s *hookSpy) Finish(funcs ...types.EnvFunc) types.Environment {
	s.finishCount += len(funcs)
	return nil
}

// ---------------------------------------------------------------------------
// RegisterDSCLifecycle tests
// ---------------------------------------------------------------------------

// TestRegisterDSCLifecycle_DisabledByEnvVar verifies that when E2E_MANAGE_DSC=false,
// no Setup or Finish hooks are registered on the environment.
func TestRegisterDSCLifecycle_DisabledByEnvVar(t *testing.T) {
	t.Setenv("E2E_MANAGE_DSC", "false")

	spy := &hookSpy{}
	f.RegisterDSCLifecycle(spy)

	if spy.setupCount != 0 {
		t.Errorf("expected 0 Setup hooks registered, got %d", spy.setupCount)
	}
	if spy.finishCount != 0 {
		t.Errorf("expected 0 Finish hooks registered, got %d", spy.finishCount)
	}
}

// TestRegisterDSCLifecycle_EnabledByDefault verifies that when E2E_MANAGE_DSC is
// not set to "false", both a Setup and a Finish hook are registered.
func TestRegisterDSCLifecycle_EnabledByDefault(t *testing.T) {
	t.Setenv("E2E_MANAGE_DSC", "")

	spy := &hookSpy{}
	f.RegisterDSCLifecycle(spy)

	if spy.setupCount == 0 {
		t.Errorf("expected at least 1 Setup hook registered, got 0")
	}
	if spy.finishCount == 0 {
		t.Errorf("expected at least 1 Finish hook registered, got 0")
	}
}

// ---------------------------------------------------------------------------
// MaybeEnsureDSCManaged tests
// ---------------------------------------------------------------------------

// TestMaybeEnsureDSCManaged_NoDSC verifies that when no DSC object exists
// (but the CRD is registered), MaybeEnsureDSCManaged returns an error.
func TestMaybeEnsureDSCManaged_NoDSC(t *testing.T) {
	cl := newFakeClient()
	ctx := context.Background()

	_, err := f.MaybeEnsureDSCManaged(ctx, cl)

	if err == nil {
		t.Fatal("expected error when DSC object is missing, got nil")
	}
}

// TestMaybeEnsureDSCManaged_AlreadyManaged verifies that when the DSC exists
// with managementState=Managed and MCPLifecycleOperatorReady=True,
// MaybeEnsureDSCManaged returns without patching.
func TestMaybeEnsureDSCManaged_AlreadyManaged(t *testing.T) {
	cl := newFakeClient(newDSCWithCondition(stateManaged, "MCPLifecycleOperatorReady", "True"))
	ctx := context.Background()

	// When: MaybeEnsureDSCManaged is called.
	state, err := f.MaybeEnsureDSCManaged(ctx, cl)

	// Then: no error, Found=true, OriginalState=Managed, no patch.
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !state.Found {
		t.Errorf("expected Found=true")
	}
	if state.OriginalState != stateManaged {
		t.Errorf("expected OriginalState=Managed, got %q", state.OriginalState)
	}
	got := getDSCManagementState(t, ctx, cl)
	if got != stateManaged {
		t.Errorf("expected managementState=Managed after no-op, got %q", got)
	}
}

// TestMaybeEnsureDSCManaged_RemovedPatchesToManaged verifies that when the DSC
// exists with managementState=Removed, MaybeEnsureDSCManaged patches it to
// Managed and waits for the MCPLifecycleOperatorReady condition.
//
// The DSC is pre-seeded with the MCPLifecycleOperatorReady=True condition so
// the wait loop finds it immediately (the fake client has no controller to set it).
func TestMaybeEnsureDSCManaged_RemovedPatchesToManaged(t *testing.T) {
	// Given: DSC with managementState=Removed AND the ready condition already set.
	// The fake client preserves status across spec updates, so the wait loop
	// will find the condition immediately after the patch.
	cl := newFakeClient(newDSCWithCondition(stateRemoved, "MCPLifecycleOperatorReady", "True"))
	ctx := context.Background()

	// When: MaybeEnsureDSCManaged is called.
	state, err := f.MaybeEnsureDSCManaged(ctx, cl)

	// Then: no error, Found=true, OriginalState=Removed, DSC patched to Managed.
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !state.Found {
		t.Errorf("expected Found=true")
	}
	if state.OriginalState != stateRemoved {
		t.Errorf("expected OriginalState=Removed, got %q", state.OriginalState)
	}
	got := getDSCManagementState(t, ctx, cl)
	if got != stateManaged {
		t.Errorf("expected managementState=Managed after patch, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// MaybeRestoreDSCState tests
// ---------------------------------------------------------------------------

// TestMaybeRestoreDSCState_NotFound verifies that when state.Found=false,
// MaybeRestoreDSCState is a no-op and returns nil. No client is needed.
func TestMaybeRestoreDSCState_NotFound(t *testing.T) {
	// Given: state with Found=false.
	state := f.DSCState{
		Found:         false,
		OriginalState: stateRemoved,
		DSCName:       testDSCName,
	}

	// When: MaybeRestoreDSCState is called with a nil client.
	err := f.MaybeRestoreDSCState(context.Background(), nil, state)

	// Then: no error (early return before any client call).
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestMaybeRestoreDSCState_AlreadyManaged verifies that when
// state.OriginalState=Managed, MaybeRestoreDSCState is a no-op and returns nil.
func TestMaybeRestoreDSCState_AlreadyManaged(t *testing.T) {
	// Given: state with OriginalState=Managed.
	state := f.DSCState{
		Found:         true,
		OriginalState: stateManaged,
		DSCName:       testDSCName,
	}

	// When: MaybeRestoreDSCState is called with a nil client.
	err := f.MaybeRestoreDSCState(context.Background(), nil, state)

	// Then: no error (early return before any client call).
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestMaybeRestoreDSCState_RestoresRemoved verifies that when
// state.Found=true and state.OriginalState=Removed, MaybeRestoreDSCState
// patches the DSC managementState back to Removed.
func TestMaybeRestoreDSCState_RestoresRemoved(t *testing.T) {
	// Given: DSC currently at Managed (post-test state), original was Removed.
	cl := newFakeClient(newDSC(stateManaged))
	ctx := context.Background()
	state := f.DSCState{
		Found:         true,
		OriginalState: stateRemoved,
		DSCName:       testDSCName,
	}

	// When: MaybeRestoreDSCState is called.
	err := f.MaybeRestoreDSCState(ctx, cl, state)

	// Then: no error, DSC restored to Removed.
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	got := getDSCManagementState(t, ctx, cl)
	if got != stateRemoved {
		t.Errorf("expected managementState=Removed after restore, got %q", got)
	}
}
