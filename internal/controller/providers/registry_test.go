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

package providers

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestRegisterAndSetupAll(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	registry = nil

	var calls []string
	Register("alpha", Registration{
		Factory: func(mgr ctrl.Manager) error {
			calls = append(calls, "alpha")
			return nil
		},
	})
	Register("beta", Registration{
		Factory: func(mgr ctrl.Manager) error {
			calls = append(calls, "beta")
			return nil
		},
	})

	if len(registry) != 2 {
		t.Fatalf("expected 2 registered factories, got %d", len(registry))
	}

	if err := SetupAll(nil); err != nil {
		t.Fatalf("SetupAll() unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "alpha" || calls[1] != "beta" {
		t.Fatalf("expected calls [alpha, beta], got %v", calls)
	}
}

func TestSetupAllError(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	registry = nil

	Register("failing", Registration{
		Factory: func(mgr ctrl.Manager) error {
			return fmt.Errorf("setup failed")
		},
	})

	err := SetupAll(nil)
	if err == nil {
		t.Fatal("expected error from SetupAll")
	}
	if got := err.Error(); got != "provider failing: setup failed" {
		t.Fatalf("unexpected error message: %s", got)
	}
}

func TestSetupAllWithRequiredCRDsPresent(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	gv := schema.GroupVersion{Group: "gateway.networking.k8s.io", Version: "v1"}
	httpRouteGVK := gv.WithKind("HTTPRoute")

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	mapper.Add(httpRouteGVK, meta.RESTScopeNamespace)
	mgr := &fakeManager{mapper: mapper}

	registry = nil
	var calls []string
	Register("plain", Registration{
		Factory: func(mgr ctrl.Manager) error {
			calls = append(calls, "plain")
			return nil
		},
	})
	Register("withCRDs", Registration{
		Factory: func(mgr ctrl.Manager) error {
			calls = append(calls, "withCRDs")
			return nil
		},
		RequiredCRDs: []schema.GroupVersionKind{httpRouteGVK},
	})

	if err := SetupAll(mgr); err != nil {
		t.Fatalf("SetupAll() unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "plain" || calls[1] != "withCRDs" {
		t.Fatalf("expected calls [plain, withCRDs], got %v", calls)
	}
}

func TestSetupAllWithRequiredCRDsError(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	gv := schema.GroupVersion{Group: "gateway.networking.k8s.io", Version: "v1"}
	httpRouteGVK := gv.WithKind("HTTPRoute")

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	mapper.Add(httpRouteGVK, meta.RESTScopeNamespace)
	mgr := &fakeManager{mapper: mapper}

	registry = nil
	Register("failingWithCRDs", Registration{
		Factory: func(mgr ctrl.Manager) error {
			return fmt.Errorf("factory failed")
		},
		RequiredCRDs: []schema.GroupVersionKind{httpRouteGVK},
	})

	err := SetupAll(mgr)
	if err == nil {
		t.Fatal("expected error from SetupAll")
	}
	if got := err.Error(); got != "provider failingWithCRDs: factory failed" {
		t.Fatalf("unexpected error: %s", got)
	}
}
