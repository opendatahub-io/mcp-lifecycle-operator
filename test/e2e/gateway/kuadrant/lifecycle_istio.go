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

package kuadrant

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	gatewayAPIVersion = "v1.6.2"
	istioVersion      = "1.31.0"
)

type istioLifecycle struct{}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func ensureIstioctl(ctx context.Context) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", fmt.Errorf("finding module root: %w", err)
	}
	binDir := filepath.Join(root, "bin")

	versionedPath := filepath.Join(binDir, fmt.Sprintf("istioctl-%s", istioVersion))
	if _, err := os.Stat(versionedPath); err == nil {
		return versionedPath, nil
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("creating bin directory: %w", err)
	}

	installedPath := filepath.Join(binDir, "istioctl")
	_ = os.Remove(installedPath)

	pkg := fmt.Sprintf("istio.io/istio/istioctl/cmd/istioctl@%s", istioVersion)
	fmt.Printf("Building istioctl %s from source\n", istioVersion)
	cmd := exec.CommandContext(ctx, "go", "install", pkg)
	cmd.Env = append(os.Environ(), "GOBIN="+binDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go install %s: %w\n%s", pkg, err, string(out))
	}

	if err := os.Rename(installedPath, versionedPath); err != nil {
		return "", fmt.Errorf("renaming istioctl to versioned path: %w", err)
	}
	_ = os.Symlink(versionedPath, installedPath)

	return versionedPath, nil
}

func (l *istioLifecycle) Setup(ctx context.Context, cfg *envconf.Config) error {
	istioctl, err := ensureIstioctl(ctx)
	if err != nil {
		return fmt.Errorf("ensure istioctl: %w", err)
	}

	steps := []struct {
		name string
		cmd  []string
	}{
		{
			"install Gateway API CRDs",
			[]string{"kubectl", "apply", "-f",
				fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/standard-install.yaml", gatewayAPIVersion)},
		},
		{
			"wait for Gateway CRD",
			[]string{"kubectl", "wait", "--for=condition=Established", "--timeout=120s",
				"crd/gateways.gateway.networking.k8s.io"},
		},
		{
			"install Istio",
			[]string{istioctl, "install", "--set", "profile=minimal", "-y"},
		},
		{
			"wait for istiod",
			[]string{"kubectl", "wait", "--for=condition=Available", "--timeout=300s",
				"deployment/istiod", "-n", "istio-system"},
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

func (l *istioLifecycle) Teardown(ctx context.Context, _ *envconf.Config) error {
	istioctl, err := ensureIstioctl(ctx)
	if err != nil {
		return fmt.Errorf("ensure istioctl: %w", err)
	}

	cmd := exec.CommandContext(ctx, istioctl, "uninstall", "--purge", "-y")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uninstall istio: %w\n%s", err, string(out))
	}

	cmd = exec.CommandContext(ctx, "kubectl", "delete", "namespace", "istio-system", "--ignore-not-found")
	out, err = cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not found") {
		return fmt.Errorf("delete istio-system namespace: %w\n%s", err, string(out))
	}
	return nil
}
