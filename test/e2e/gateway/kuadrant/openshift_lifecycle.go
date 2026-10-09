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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
)

const (
	mcpGatewayOLMPackage = "mcp-gateway"
	mcpGatewayChannel    = "preview"
	mcpGatewayCatalog    = "redhat-operators"
	mcpGatewayMarketplNs = "openshift-marketplace"

	openshiftGatewayClass = "openshift-default"
)

type mcpGatewayOLMLifecycle struct{}

func (l *mcpGatewayOLMLifecycle) Setup(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	// Create mcp-system namespace for the OLM-managed operator.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mcpSystemNamespace}}
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", mcpSystemNamespace, err)
	}

	// Create OperatorGroup (required for namespaced OLM installs).
	og := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1",
		"kind":       "OperatorGroup",
		"metadata": map[string]any{
			"name":      mcpGatewayOLMPackage,
			"namespace": mcpSystemNamespace,
		},
		"spec": map[string]any{},
	}}
	if err := r.Create(ctx, og); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create OperatorGroup: %w", err)
	}

	// Create Subscription.
	sub := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata": map[string]any{
			"name":      mcpGatewayOLMPackage,
			"namespace": mcpSystemNamespace,
		},
		"spec": map[string]any{
			"channel":             mcpGatewayChannel,
			"name":                mcpGatewayOLMPackage,
			"source":              mcpGatewayCatalog,
			"sourceNamespace":     mcpGatewayMarketplNs,
			"installPlanApproval": "Automatic",
			"config": map[string]any{
				"env": []any{
					map[string]any{
						"name":  "WATCH_NAMESPACE",
						"value": "",
					},
				},
			},
		},
	}}
	if err := r.Create(ctx, sub); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Subscription: %w", err)
	}

	// Wait for the CSV to reach Succeeded.
	if err := waitForCSVSucceeded(ctx, r, mcpSystemNamespace, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for CSV: %w", err)
	}

	// Create gateway-system namespace.
	gwNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gatewayNamespace}}
	if err := r.Create(ctx, gwNs); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", gatewayNamespace, err)
	}

	// Create Gateway with dual listeners using openshift-default class.
	fromAll := gatewayv1.NamespacesFromAll
	wildcardHost := gatewayv1.Hostname("*.mcp.e2e.test")
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayName,
			Namespace: gatewayNamespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: openshiftGatewayClass,
			Listeners: []gatewayv1.Listener{
				{
					Name:     "mcp",
					Port:     80,
					Protocol: gatewayv1.HTTPProtocolType,
					AllowedRoutes: &gatewayv1.AllowedRoutes{
						Namespaces: &gatewayv1.RouteNamespaces{From: &fromAll},
					},
				},
				{
					Name:     "mcps",
					Hostname: &wildcardHost,
					Port:     8080,
					Protocol: gatewayv1.HTTPProtocolType,
					AllowedRoutes: &gatewayv1.AllowedRoutes{
						Namespaces: &gatewayv1.RouteNamespaces{From: &fromAll},
					},
				},
			},
		},
	}
	if err := r.Create(ctx, gw); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Gateway %s/%s: %w", gatewayNamespace, gatewayName, err)
	}

	// Register Kuadrant types for the MCPGatewayExtension.
	if err := kuadrantapi.AddToScheme(r.GetScheme()); err != nil {
		return fmt.Errorf("register Kuadrant types: %w", err)
	}

	// Create MCPGatewayExtension targeting the catch-all listener.
	ext := &kuadrantapi.MCPGatewayExtension{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mcp.kuadrant.io/v1alpha1",
			Kind:       "MCPGatewayExtension",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-gateway-extension",
			Namespace: mcpSystemNamespace,
		},
		Spec: kuadrantapi.MCPGatewayExtensionSpec{
			TargetRef: kuadrantapi.TargetReference{
				Group:       gatewayv1.GroupName,
				Kind:        "Gateway",
				Name:        gatewayName,
				Namespace:   gatewayNamespace,
				SectionName: "mcp",
			},
		},
	}
	if err := r.Create(ctx, ext); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create MCPGatewayExtension: %w", err)
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
		return fmt.Errorf("create ReferenceGrant: %w", err)
	}

	// Wait for extension ready.
	return waitForExtensionReady(ctx, r, "mcp-gateway-extension", mcpSystemNamespace, 5*time.Minute)
}

func (l *mcpGatewayOLMLifecycle) Teardown(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	// Delete MCPGatewayExtension first — its finalizer requires the controller.
	ext := &kuadrantapi.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-gateway-extension", Namespace: mcpSystemNamespace},
	}
	_ = r.Delete(ctx, ext)
	_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, 120*time.Second, true,
		func(ctx context.Context) (bool, error) {
			check := &kuadrantapi.MCPGatewayExtension{}
			err := r.Get(ctx, "mcp-gateway-extension", mcpSystemNamespace, check)
			return apierrors.IsNotFound(err), nil
		})

	grant := &gatewayv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: referenceGrantName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, grant)

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, gw)

	// Delete OLM resources.
	sub := &unstructured.Unstructured{}
	sub.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
	})
	sub.SetName(mcpGatewayOLMPackage)
	sub.SetNamespace(mcpSystemNamespace)
	_ = r.Delete(ctx, sub)

	csvList := &unstructured.UnstructuredList{}
	csvList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1alpha1", Kind: "ClusterServiceVersionList",
	})
	if err := r.WithNamespace(mcpSystemNamespace).List(ctx, csvList); err == nil {
		for i := range csvList.Items {
			_ = r.Delete(ctx, &csvList.Items[i])
		}
	}

	og := &unstructured.Unstructured{}
	og.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1", Kind: "OperatorGroup",
	})
	og.SetName(mcpGatewayOLMPackage)
	og.SetNamespace(mcpSystemNamespace)
	_ = r.Delete(ctx, og)

	for _, ns := range []string{mcpSystemNamespace, gatewayNamespace} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = r.Delete(ctx, nsObj)
	}

	return nil
}

func waitForCSVSucceeded(ctx context.Context, r *resources.Resources, ns string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			csvList := &unstructured.UnstructuredList{}
			csvList.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "operators.coreos.com", Version: "v1alpha1", Kind: "ClusterServiceVersionList",
			})
			if err := r.WithNamespace(ns).List(ctx, csvList); err != nil {
				return false, nil
			}
			for _, csv := range csvList.Items {
				phase, _, _ := unstructured.NestedString(csv.Object, "status", "phase")
				if phase == "Succeeded" {
					return true, nil
				}
			}
			return false, nil
		})
}

func waitForExtensionReady(ctx context.Context, r *resources.Resources, name, ns string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			ext := &kuadrantapi.MCPGatewayExtension{}
			if err := r.Get(ctx, name, ns, ext); err != nil {
				return false, nil
			}
			for _, c := range ext.Status.Conditions {
				if c.Type == "Ready" && c.Status == metav1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		})
}
