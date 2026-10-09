//go:build e2e

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

package httproute

import (
	"context"
	"fmt"
	"os/exec"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const envoyGatewayVersion = "v1.9.0"

type envoyGatewayLifecycle struct{}

func (l *envoyGatewayLifecycle) Setup(ctx context.Context, cfg *envconf.Config) error {
	// Envoy Gateway's install.yaml bundles standard and experimental Gateway
	// API CRDs. The standard CRDs include a ValidatingAdmissionPolicy that
	// blocks the experimental CRDs in the same manifest. The partial apply
	// failure is expected; the wait steps below validate that the required
	// resources were created.
	installCmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "--force-conflicts", "-f",
		fmt.Sprintf("https://github.com/envoyproxy/gateway/releases/download/%s/install.yaml", envoyGatewayVersion))
	installCmd.CombinedOutput() //nolint:errcheck // partial failure expected

	steps := []struct {
		name string
		cmd  []string
	}{
		{
			"wait for Envoy Gateway",
			[]string{"kubectl", "wait", "--for=condition=Available", "--timeout=300s",
				"deployment/envoy-gateway", "-n", "envoy-gateway-system"},
		},
		{
			"wait for HTTPRoute CRD",
			[]string{"kubectl", "wait", "--for=condition=Established", "--timeout=120s",
				"crd/httproutes.gateway.networking.k8s.io"},
		},
	}

	for _, s := range steps {
		cmd := exec.CommandContext(ctx, s.cmd[0], s.cmd[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", s.name, err, string(out))
		}
	}
	return nil
}

func (l *envoyGatewayLifecycle) Teardown(ctx context.Context, _ *envconf.Config) error {
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "-f",
		fmt.Sprintf("https://github.com/envoyproxy/gateway/releases/download/%s/install.yaml", envoyGatewayVersion),
		"--ignore-not-found")
	_ = cmd.Run()
	return nil
}
