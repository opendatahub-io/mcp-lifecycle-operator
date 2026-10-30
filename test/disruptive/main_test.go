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
	"os"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	mcpv1alpha1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1alpha1"
	mcpv1beta1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1beta1"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	f.RegisterProfileFlag()
	cfg, err := envconf.NewFromFlags()
	if err != nil {
		panic(err)
	}

	if labels := f.ResolveProfile(); labels != nil {
		cfg.WithLabels(labels)
	}

	testenv = env.NewWithConfig(cfg)

	// Register MCPServer types so the functional "operator is healthy again"
	// check after re-enablement can create and observe an MCPServer. The DSC and
	// module CR are handled dynamically (unstructured) and need no scheme.
	scheme := cfg.Client().Resources().GetScheme()
	if err := mcpv1beta1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	// Pre-pull the MCPServer image so the post-re-enable functional check does
	// not race a cold-node image pull.
	testenv.Setup(f.PrewarmImages(f.DefaultMCPServerImage))

	// Create a unique namespace before each test (and tear it down after) so the
	// functional MCPServer check runs in isolation.
	f.WithNamespaceManagement(testenv, "disrupt")

	os.Exit(testenv.Run(m))
}
