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

package framework

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	cr "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	dscKind                  = "DataScienceCluster"
	dscDefaultName           = "default-dsc"
	componentPath            = "mcplifecycleoperator"
	stateManaged             = "Managed"
	stateRemoved             = "Removed"
	dscReadyWait             = 10 * time.Minute
	condMCPLifecycleOperator = "MCPLifecycleOperatorReady"
)

// LifecycleEnvironment is the subset of env.Environment needed by
// RegisterDSCLifecycle. Satisfied by env.Environment and test spies.
type LifecycleEnvironment interface {
	Setup(...types.EnvFunc) types.Environment
	Finish(...types.EnvFunc) types.Environment
}

type DSCState struct {
	Found         bool
	OriginalState string
	DSCName       string
}

// RegisterDSCLifecycle registers Setup/Finish hooks on the given environment
// to manage the DSC mcplifecycleoperator component state. No-op when
// E2E_MANAGE_DSC=false.
func RegisterDSCLifecycle(testenv LifecycleEnvironment) {
	if os.Getenv("E2E_MANAGE_DSC") == "false" {
		return
	}
	var state DSCState
	testenv.Setup(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		cl, err := cr.New(cfg.Client().RESTConfig(), cr.Options{})
		if err != nil {
			return ctx, fmt.Errorf("failed to create controller-runtime client: %w", err)
		}
		state, err = MaybeEnsureDSCManaged(ctx, cl)
		if err != nil {
			return ctx, fmt.Errorf("DSC setup failed: %w", err)
		}
		return ctx, nil
	})
	testenv.Finish(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		cl, err := cr.New(cfg.Client().RESTConfig(), cr.Options{})
		if err != nil {
			return ctx, fmt.Errorf("failed to create controller-runtime client: %w", err)
		}
		if err := MaybeRestoreDSCState(ctx, cl, state); err != nil {
			return ctx, fmt.Errorf("DSC restore failed: %w", err)
		}
		return ctx, nil
	})
}

// MaybeEnsureDSCManaged checks the DataScienceCluster resource and, if the
// mcplifecycleoperator component is Removed, patches it to Managed and
// waits for the MCPLifecycleOperatorReady condition.
//
// Returns DSCState for later restoration. If the DSC CRD does not exist
// (standalone deployment), returns a zero DSCState with Found=false (skip).
func MaybeEnsureDSCManaged(ctx context.Context, cl cr.Client) (DSCState, error) {
	state := DSCState{DSCName: dscDefaultName}

	dsc := &unstructured.Unstructured{}
	dsc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "datasciencecluster.opendatahub.io",
		Version: "v2",
		Kind:    dscKind,
	})

	// DSC is cluster-scoped, so no namespace.
	err := cl.Get(ctx, cr.ObjectKey{Name: dscDefaultName}, dsc)
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			log.Printf("[e2e-dsc] DataScienceCluster CRD not found, skipping DSC management (standalone mode)")
			return state, nil
		}
		return state, fmt.Errorf("failed to get DataScienceCluster %q: %w", dscDefaultName, err)
	}
	state.Found = true

	currentState, _, _ := unstructured.NestedString(
		dsc.Object,
		"spec", "components", componentPath, "managementState",
	)
	state.OriginalState = currentState
	log.Printf("[e2e-dsc] DataScienceCluster %q mcplifecycleoperator.managementState = %q", dscDefaultName, currentState)

	if currentState != stateManaged {
		if err := unstructured.SetNestedField(
			dsc.Object, stateManaged,
			"spec", "components", componentPath, "managementState",
		); err != nil {
			return state, fmt.Errorf("failed to set managementState: %w", err)
		}
		if err := cl.Update(ctx, dsc); err != nil {
			return state, fmt.Errorf("failed to patch DataScienceCluster to Managed: %w", err)
		}
		log.Printf("[e2e-dsc] patched mcplifecycleoperator to Managed")
	} else {
		log.Printf("[e2e-dsc] already Managed, no patch needed")
	}

	log.Printf("[e2e-dsc] waiting for %s condition (timeout %s)", condMCPLifecycleOperator, dscReadyWait)
	if err := waitForDSCCondition(ctx, cl, dscDefaultName, condMCPLifecycleOperator, dscReadyWait); err != nil {
		return state, err
	}
	log.Printf("[e2e-dsc] DSC reports %s=True", condMCPLifecycleOperator)
	return state, nil
}

func waitForDSCCondition(ctx context.Context, cl cr.Client, name, condType string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		dsc := &unstructured.Unstructured{}
		dsc.SetGroupVersionKind(dscGVK())
		if err := cl.Get(ctx, cr.ObjectKey{Name: name}, dsc); err != nil {
			return false, fmt.Errorf("failed to get DSC while waiting for %s: %w", condType, err)
		}

		status, msg := findCondition(dsc, condType)
		if status == "True" {
			return true, nil
		}

		log.Printf("[e2e-dsc] %s=%s: %s", condType, status, msg)
		return false, nil
	})
}

func findCondition(dsc *unstructured.Unstructured, condType string) (status, message string) {
	conditions, ok, _ := unstructured.NestedSlice(dsc.Object, "status", "conditions")
	if !ok {
		return "", "no status.conditions"
	}
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cType, _, _ := unstructured.NestedString(c, "type")
		if cType == condType {
			s, _, _ := unstructured.NestedString(c, "status")
			m, _, _ := unstructured.NestedString(c, "message")
			return s, m
		}
	}
	return "", "condition not yet present"
}

// MaybeRestoreDSCState restores the mcplifecycleoperator managementState to its
// original value. No-op if DSC was not found or state was already Managed.
func MaybeRestoreDSCState(ctx context.Context, cl cr.Client, state DSCState) error {
	if !state.Found {
		return nil
	}
	if state.OriginalState == stateManaged {
		// Was already Managed before tests, nothing to restore.
		return nil
	}

	dsc := &unstructured.Unstructured{}
	dsc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "datasciencecluster.opendatahub.io",
		Version: "v2",
		Kind:    dscKind,
	})

	if err := cl.Get(ctx, cr.ObjectKey{Name: state.DSCName}, dsc); err != nil {
		return fmt.Errorf("failed to get DataScienceCluster for restore: %w", err)
	}

	restoreState := state.OriginalState
	if restoreState == "" {
		restoreState = stateRemoved
	}

	if err := unstructured.SetNestedField(
		dsc.Object, restoreState,
		"spec", "components", componentPath, "managementState",
	); err != nil {
		return fmt.Errorf("failed to set managementState for restore: %w", err)
	}

	if err := cl.Update(ctx, dsc); err != nil {
		return fmt.Errorf("failed to restore DataScienceCluster state to %q: %w", restoreState, err)
	}
	log.Printf("[e2e-dsc] restored mcplifecycleoperator to %q", restoreState)
	return nil
}

func dscGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "datasciencecluster.opendatahub.io",
		Version: "v2",
		Kind:    dscKind,
	}
}
