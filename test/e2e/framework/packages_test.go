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
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

type fakeLifecycle struct {
	name     string
	setupErr error
	setupLog *[]string
	tdLog    *[]string
}

func (f *fakeLifecycle) Setup(_ context.Context, _ *envconf.Config) error {
	if f.setupLog != nil {
		*f.setupLog = append(*f.setupLog, "setup:"+f.name)
	}
	return f.setupErr
}

func (f *fakeLifecycle) Teardown(_ context.Context, _ *envconf.Config) error {
	if f.tdLog != nil {
		*f.tdLog = append(*f.tdLog, "teardown:"+f.name)
	}
	return nil
}

func TestLabelsIntersect(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string][]string
		want bool
	}{
		{
			name: "matching key and value",
			a:    map[string][]string{"scope": {"kuadrant"}},
			b:    map[string][]string{"scope": {"kuadrant", "gateway-conformance"}},
			want: true,
		},
		{
			name: "matching key but no value overlap",
			a:    map[string][]string{"scope": {"httproute"}},
			b:    map[string][]string{"scope": {"kuadrant", "gateway-conformance"}},
			want: false,
		},
		{
			name: "no matching key",
			a:    map[string][]string{"scope": {"kuadrant"}},
			b:    map[string][]string{"speed": {"fast"}},
			want: false,
		},
		{
			name: "both empty",
			a:    map[string][]string{},
			b:    map[string][]string{},
			want: false,
		},
		{
			name: "a nil",
			a:    nil,
			b:    map[string][]string{"scope": {"kuadrant"}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelsIntersect(tc.a, tc.b); got != tc.want {
				t.Errorf("labelsIntersect(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestRegisterPackagePanicsOnDuplicate(t *testing.T) {
	defer resetPackages()
	RegisterPackage("dup", PackageRegistration{})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	RegisterPackage("dup", PackageRegistration{})
}

func TestRegisterPackageLifecyclePanicsWithoutPackage(t *testing.T) {
	defer resetPackages()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when package not registered")
		}
	}()
	RegisterPackageLifecycle(DefaultTier, "nonexistent", &fakeLifecycle{name: "a"})
}

func TestResolveLifecyclesPlatformOverridesDefault(t *testing.T) {
	defer resetPackages()

	RegisterPackage("test-pkg", PackageRegistration{})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{name: "default-a"})
	RegisterPackageLifecycle(PlatformTier, "test-pkg", &fakeLifecycle{name: "platform-a"})

	lcs := resolveLifecycles("test-pkg")
	if len(lcs) != 1 {
		t.Fatalf("expected 1 lifecycle, got %d", len(lcs))
	}
	if lcs[0].(*fakeLifecycle).name != "platform-a" {
		t.Errorf("expected platform lifecycle, got %q", lcs[0].(*fakeLifecycle).name)
	}
}

func TestResolveLifecyclesFallsBackToDefault(t *testing.T) {
	defer resetPackages()

	RegisterPackage("test-pkg", PackageRegistration{})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{name: "default-a"})

	lcs := resolveLifecycles("test-pkg")
	if len(lcs) != 1 {
		t.Fatalf("expected 1 lifecycle, got %d", len(lcs))
	}
	if lcs[0].(*fakeLifecycle).name != "default-a" {
		t.Errorf("expected default lifecycle, got %q", lcs[0].(*fakeLifecycle).name)
	}
}

func TestResolveLifecyclesMultipleInOrder(t *testing.T) {
	defer resetPackages()

	RegisterPackage("test-pkg", PackageRegistration{})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{name: "first"})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{name: "second"})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{name: "third"})

	lcs := resolveLifecycles("test-pkg")
	if len(lcs) != 3 {
		t.Fatalf("expected 3 lifecycles, got %d", len(lcs))
	}
	expected := []string{"first", "second", "third"}
	for i, lc := range lcs {
		if lc.(*fakeLifecycle).name != expected[i] {
			t.Errorf("lifecycle[%d] = %q, want %q", i, lc.(*fakeLifecycle).name, expected[i])
		}
	}
}

func TestTeardownFILOOnSetupFailure(t *testing.T) {
	defer resetPackages()

	var setupLog, tdLog []string

	RegisterPackage("test-pkg", PackageRegistration{
		Labels: map[string][]string{"scope": {"test"}},
	})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{
		name: "first", setupLog: &setupLog, tdLog: &tdLog,
	})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{
		name: "second", setupLog: &setupLog, tdLog: &tdLog,
		setupErr: fmt.Errorf("boom"),
	})
	RegisterPackageLifecycle(DefaultTier, "test-pkg", &fakeLifecycle{
		name: "third", setupLog: &setupLog, tdLog: &tdLog,
	})

	ctx := context.Background()
	lcs := resolveLifecycles("test-pkg")

	var failIdx int
	for i, lc := range lcs {
		if err := lc.Setup(ctx, nil); err != nil {
			failIdx = i
			for j := i - 1; j >= 0; j-- {
				_ = lcs[j].Teardown(ctx, nil)
			}
			break
		}
	}

	if failIdx != 1 {
		t.Fatalf("expected failure at index 1, got %d", failIdx)
	}

	expectedSetup := []string{"setup:first", "setup:second"}
	if len(setupLog) != len(expectedSetup) {
		t.Fatalf("expected %d setup calls, got %d: %v", len(expectedSetup), len(setupLog), setupLog)
	}
	for i, s := range expectedSetup {
		if setupLog[i] != s {
			t.Errorf("setupLog[%d] = %q, want %q", i, setupLog[i], s)
		}
	}

	expectedTD := []string{"teardown:first"}
	if len(tdLog) != len(expectedTD) {
		t.Fatalf("expected %d teardown calls, got %d: %v", len(expectedTD), len(tdLog), tdLog)
	}
	for i, s := range expectedTD {
		if tdLog[i] != s {
			t.Errorf("tdLog[%d] = %q, want %q", i, tdLog[i], s)
		}
	}
}
