//go:build e2e && openshift

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
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
)

const (
	mcpGatewayOLMPackage = "mcp-gateway"
	mcpGatewayChannel    = "preview"
	mcpGatewayCatalog    = "redhat-operators"
	mcpGatewayMarketplNs = "openshift-marketplace"

	ossmOLMPackage = "servicemeshoperator3"
	ossmChannel    = "stable"
	ossmNamespace  = "openshift-operators"

	istioNamespace    = "istio-system"
	istioCNINamespace = "istio-cni"

	openshiftGatewayClass = "istio"
	openshiftPublicHost   = "mcp.e2e.test"
)

type mcpGatewayOLMLifecycle struct{}

func (l *mcpGatewayOLMLifecycle) Setup(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	if err := setupOSSM(ctx, r); err != nil {
		return err
	}
	if err := setupMCPGateway(ctx, r); err != nil {
		return err
	}
	if err := setupGatewayInfrastructure(ctx, r); err != nil {
		return err
	}

	log.Println("waiting for MCPGatewayExtension to become ready...")
	if err := waitForExtensionReady(ctx, r, "mcp-gateway-extension", mcpSystemNamespace, 5*time.Minute); err != nil {
		return err
	}
	log.Println("setup complete")
	return nil
}

func (l *mcpGatewayOLMLifecycle) Teardown(ctx context.Context, cfg *envconf.Config) error {
	r := cfg.Client().Resources()

	teardownGatewayInfrastructure(ctx, r)
	teardownMCPGateway(ctx, r)
	teardownOSSM(ctx, r)

	for _, ns := range []string{mcpSystemNamespace, gatewayNamespace, istioNamespace, istioCNINamespace} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = r.Delete(ctx, nsObj)
	}

	return nil
}

func setupOSSM(ctx context.Context, r *resources.Resources) error {
	log.Println("installing OSSM3 operator...")
	ossmSub := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata": map[string]any{
			"name":      ossmOLMPackage,
			"namespace": ossmNamespace,
		},
		"spec": map[string]any{
			"channel":             ossmChannel,
			"name":                ossmOLMPackage,
			"source":              mcpGatewayCatalog,
			"sourceNamespace":     mcpGatewayMarketplNs,
			"installPlanApproval": "Automatic",
		},
	}}
	if err := r.Create(ctx, ossmSub); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create OSSM Subscription: %w", err)
	}

	log.Println("waiting for OSSM3 CSV to succeed...")
	if err := waitForCSVSucceeded(ctx, r, ossmOLMPackage, ossmNamespace, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for OSSM CSV: %w", err)
	}

	log.Println("creating IstioCNI and Istio control plane...")
	istioCNINs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: istioCNINamespace}}
	if err := r.Create(ctx, istioCNINs); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", istioCNINamespace, err)
	}

	istioCNI := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sailoperator.io/v1",
		"kind":       "IstioCNI",
		"metadata": map[string]any{
			"name": "default",
		},
		"spec": map[string]any{
			"version":   "v1.30-latest",
			"namespace": istioCNINamespace,
		},
	}}
	if err := r.Create(ctx, istioCNI); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create IstioCNI: %w", err)
	}

	istioNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: istioNamespace}}
	if err := r.Create(ctx, istioNs); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", istioNamespace, err)
	}

	istio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sailoperator.io/v1",
		"kind":       "Istio",
		"metadata": map[string]any{
			"name": "default",
		},
		"spec": map[string]any{
			"version":   "v1.30-latest",
			"namespace": istioNamespace,
		},
	}}
	if err := r.Create(ctx, istio); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Istio: %w", err)
	}

	log.Printf("waiting for GatewayClass %s...", openshiftGatewayClass)
	if err := waitForGatewayClass(ctx, r, openshiftGatewayClass, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for GatewayClass %s: %w", openshiftGatewayClass, err)
	}
	log.Println("OSSM3 ready")

	return nil
}

func setupMCPGateway(ctx context.Context, r *resources.Resources) error {
	log.Println("installing mcp-gateway operator via OLM...")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mcpSystemNamespace}}
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", mcpSystemNamespace, err)
	}

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

	log.Println("waiting for mcp-gateway CSV to succeed...")
	if err := waitForCSVSucceeded(ctx, r, mcpGatewayOLMPackage, mcpSystemNamespace, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for CSV: %w", err)
	}
	log.Println("mcp-gateway operator ready")

	return nil
}

func setupGatewayInfrastructure(ctx context.Context, r *resources.Resources) error {
	log.Println("creating gateway infrastructure...")
	gwNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gatewayNamespace}}
	if err := r.Create(ctx, gwNs); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", gatewayNamespace, err)
	}

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

	if err := kuadrantapi.AddToScheme(r.GetScheme()); err != nil {
		return fmt.Errorf("register Kuadrant types: %w", err)
	}

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
			PublicHost: openshiftPublicHost,
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

	if err := gatewayv1beta1.Install(r.GetScheme()); err != nil {
		return fmt.Errorf("register Gateway API v1beta1 types: %w", err)
	}

	grant := &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      referenceGrantName,
			Namespace: gatewayNamespace,
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Namespace: gatewayv1beta1.Namespace(mcpSystemNamespace),
				},
				{
					Group:     "mcp.kuadrant.io",
					Kind:      "MCPGatewayExtension",
					Namespace: gatewayv1beta1.Namespace(mcpSystemNamespace),
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{Group: "", Kind: "Service"},
				{Group: "gateway.networking.k8s.io", Kind: "Gateway"},
			},
		},
	}
	if err := r.Create(ctx, grant); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ReferenceGrant: %w", err)
	}

	return nil
}

func teardownGatewayInfrastructure(ctx context.Context, r *resources.Resources) {
	log.Println("tearing down gateway infrastructure...")
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

	grant := &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: referenceGrantName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, grant)

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName, Namespace: gatewayNamespace},
	}
	_ = r.Delete(ctx, gw)
}

func teardownMCPGateway(ctx context.Context, r *resources.Resources) {
	log.Println("tearing down mcp-gateway operator...")
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
}

func teardownOSSM(ctx context.Context, r *resources.Resources) {
	log.Println("tearing down OSSM3...")
	istio := &unstructured.Unstructured{}
	istio.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "sailoperator.io", Version: "v1", Kind: "Istio",
	})
	istio.SetName("default")
	_ = r.Delete(ctx, istio)

	istioCNI := &unstructured.Unstructured{}
	istioCNI.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "sailoperator.io", Version: "v1", Kind: "IstioCNI",
	})
	istioCNI.SetName("default")
	_ = r.Delete(ctx, istioCNI)

	ossmSub := &unstructured.Unstructured{}
	ossmSub.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
	})
	ossmSub.SetName(ossmOLMPackage)
	ossmSub.SetNamespace(ossmNamespace)
	_ = r.Delete(ctx, ossmSub)

	ossmCSVList := &unstructured.UnstructuredList{}
	ossmCSVList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1alpha1", Kind: "ClusterServiceVersionList",
	})
	if err := r.WithNamespace(ossmNamespace).List(ctx, ossmCSVList); err == nil {
		for i := range ossmCSVList.Items {
			_ = r.Delete(ctx, &ossmCSVList.Items[i])
		}
	}
}

func waitForCSVSucceeded(ctx context.Context, r *resources.Resources, subName, ns string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			sub := &unstructured.Unstructured{}
			sub.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
			})
			if err := r.Get(ctx, subName, ns, sub); err != nil {
				return false, nil
			}
			csvName, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
			if csvName == "" {
				return false, nil
			}

			csv := &unstructured.Unstructured{}
			csv.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "operators.coreos.com", Version: "v1alpha1", Kind: "ClusterServiceVersion",
			})
			if err := r.Get(ctx, csvName, ns, csv); err != nil {
				return false, nil
			}
			phase, _, _ := unstructured.NestedString(csv.Object, "status", "phase")
			return phase == "Succeeded", nil
		})
}

func waitForGatewayClass(ctx context.Context, r *resources.Resources, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			gc := &gatewayv1.GatewayClass{}
			if err := r.Get(ctx, name, "", gc); err != nil {
				return false, nil
			}
			for _, c := range gc.Status.Conditions {
				if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
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
