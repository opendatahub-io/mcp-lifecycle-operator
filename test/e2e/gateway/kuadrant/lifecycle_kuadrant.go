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
	"os/exec"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
)

const (
	mcpGatewayVersion  = "v0.9.0"
	gatewayNamespace   = "gateway-system"
	gatewayName        = "mcp-gateway"
	mcpSystemNamespace = "mcp-system"
	referenceGrantName = "allow-mcp-system"
)

type kuadrantLifecycle struct{}

func (l *kuadrantLifecycle) Setup(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	// Create gateway-system namespace.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gatewayNamespace}}
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", gatewayNamespace, err)
	}

	// Create the Gateway.
	fromAll := gatewayv1.NamespacesFromAll
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayName,
			Namespace: gatewayNamespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "istio",
			Listeners: []gatewayv1.Listener{{
				Name:     "mcp",
				Port:     80,
				Protocol: gatewayv1.HTTPProtocolType,
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &fromAll,
					},
				},
			}},
		},
	}
	if err := r.Create(ctx, gw); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Gateway %s/%s: %w", gatewayNamespace, gatewayName, err)
	}

	// Create ReferenceGrant allowing mcp-system to reference gateway-system resources.
	grant := &gatewayv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      referenceGrantName,
			Namespace: gatewayNamespace,
		},
		Spec: gatewayv1.ReferenceGrantSpec{
			From: []gatewayv1.ReferenceGrantFrom{
				{
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Namespace: gatewayv1.Namespace(mcpSystemNamespace),
				},
				{
					Group:     "mcp.kuadrant.io",
					Kind:      "MCPGatewayExtension",
					Namespace: gatewayv1.Namespace(mcpSystemNamespace),
				},
			},
			To: []gatewayv1.ReferenceGrantTo{
				{Group: "", Kind: "Service"},
				{Group: "gateway.networking.k8s.io", Kind: "Gateway"},
			},
		},
	}
	if err := r.Create(ctx, grant); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ReferenceGrant %s/%s: %w", gatewayNamespace, referenceGrantName, err)
	}

	// Install Kuadrant MCP Gateway CRDs and controller via kustomize overlays.
	steps := []struct {
		name string
		cmd  []string
	}{
		{
			"install Kuadrant MCP Gateway CRDs",
			[]string{"kubectl", "apply", "-k",
				fmt.Sprintf("https://github.com/Kuadrant/mcp-gateway/config/crd?ref=%s", mcpGatewayVersion)},
		},
		{
			"wait for MCPServerRegistration CRD",
			[]string{"kubectl", "wait", "--for=condition=Established", "--timeout=120s",
				"crd/mcpserverregistrations.mcp.kuadrant.io"},
		},
		{
			"install Kuadrant MCP Gateway controller",
			[]string{"kubectl", "apply", "-k",
				fmt.Sprintf("https://github.com/Kuadrant/mcp-gateway/config/mcp-gateway/overlays/mcp-system?ref=%s", mcpGatewayVersion)},
		},
		{
			"wait for mcp-gateway-controller",
			[]string{"kubectl", "wait", "--for=condition=Available", "--timeout=300s",
				"deployment/mcp-gateway-controller", "-n", mcpSystemNamespace},
		},
		{
			"wait for gateway accepted",
			[]string{"kubectl", "wait", "--for=condition=Accepted", "--timeout=120s",
				fmt.Sprintf("gateway/%s", gatewayName), "-n", gatewayNamespace},
		},
		{
			"wait for extension ready",
			[]string{"kubectl", "wait", "--for=condition=Ready", "--timeout=300s",
				"mcpgatewayextension/mcp-gateway-extension", "-n", mcpSystemNamespace},
		},
		{
			"wait for mcp-system deployments",
			[]string{"kubectl", "wait", "--for=condition=Available", "--timeout=300s",
				"deployment", "--all", "-n", mcpSystemNamespace},
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

func (l *kuadrantLifecycle) Teardown(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	// Delete MCPGatewayExtensions first and wait — their finalizer requires
	// the mcp-gateway-controller to still be running.
	extList := &kuadrantapi.MCPGatewayExtensionList{}
	if err := r.WithNamespace(mcpSystemNamespace).List(ctx, extList); err == nil {
		for i := range extList.Items {
			_ = r.Delete(ctx, &extList.Items[i])
		}
	}
	cmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=delete",
		"mcpgatewayextension", "--all", "-n", mcpSystemNamespace, "--timeout=120s")
	_ = cmd.Run()

	steps := [][]string{
		{"kubectl", "delete", "-k",
			fmt.Sprintf("https://github.com/Kuadrant/mcp-gateway/config/mcp-gateway/overlays/mcp-system?ref=%s", mcpGatewayVersion),
			"--ignore-not-found"},
		{"kubectl", "delete", "-k",
			fmt.Sprintf("https://github.com/Kuadrant/mcp-gateway/config/crd?ref=%s", mcpGatewayVersion),
			"--ignore-not-found"},
	}

	for _, args := range steps {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		_ = cmd.Run()
	}

	// Delete the gateway resources we created.
	grant := &gatewayv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: referenceGrantName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, grant)

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, gw)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gatewayNamespace}}
	_ = r.Delete(ctx, ns)

	return nil
}
