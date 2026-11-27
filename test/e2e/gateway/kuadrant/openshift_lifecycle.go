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
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
)

const (
	ossmOLMPackage       = "servicemeshoperator3"
	ossmChannel          = "stable"
	ossmNamespace        = "openshift-operators"
	ossmCatalog          = "redhat-operators"
	ossmCatalogNamespace = "openshift-marketplace"

	sailGroup         = "sailoperator.io"
	ossmIstioVersion  = "v1.30-latest"
	istioNamespace    = "istio-system"
	istioCNINamespace = "istio-cni"

	openshiftGatewayClass = "istio"
	openshiftPublicHost   = "mcp.e2e.test"
)

var olmV1alpha1 = schema.GroupVersion{Group: "operators.coreos.com", Version: "v1alpha1"}

// ownedFixtures records objects that a lifecycle created, so teardown deletes
// only those and never shared objects that already existed.
type ownedFixtures struct {
	objs []k8s.Object
}

// create creates obj and records it. An object that already exists is shared
// and is not recorded.
func (o *ownedFixtures) create(ctx context.Context, r *resources.Resources, obj k8s.Object) error {
	if err := r.Create(ctx, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	o.objs = append(o.objs, obj)
	return nil
}

// deleteAll deletes the recorded objects in reverse creation order, so objects
// are removed before the ones they depend on. It stops at the first object
// that is not confirmed gone and keeps it and its dependencies recorded.
func (o *ownedFixtures) deleteAll(ctx context.Context, r *resources.Resources) error {
	for len(o.objs) > 0 {
		last := len(o.objs) - 1
		if err := deleteOwnedFixture(ctx, r, o.objs[last]); err != nil {
			return err
		}
		o.objs = o.objs[:last]
	}
	return nil
}

// deleteOwnedFixture deletes obj and waits until the object with its recorded
// UID is gone. It retries the DELETE through transient API errors. NotFound or
// a different UID at the same name (a replacement this run does not own)
// counts as gone.
func deleteOwnedFixture(ctx context.Context, r *resources.Resources, obj k8s.Object) error {
	uid := obj.GetUID()
	if uid == "" {
		return fmt.Errorf("missing UID for fixture %T %s/%s", obj, obj.GetNamespace(), obj.GetName())
	}
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			current, ok := obj.DeepCopyObject().(k8s.Object)
			if !ok {
				return false, fmt.Errorf("fixture %T is not a k8s.Object", obj)
			}
			err := r.Get(ctx, obj.GetName(), obj.GetNamespace(), current)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				lastErr = err
				return false, nil
			}
			if current.GetUID() != uid {
				return true, nil
			}
			if current.GetDeletionTimestamp() != nil {
				// DELETE accepted; wait for finalizers.
				return false, nil
			}
			err = r.Delete(ctx, current, func(opts *metav1.DeleteOptions) {
				opts.Preconditions = &metav1.Preconditions{UID: &uid}
			})
			if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				lastErr = err
			}
			// Confirm the result with the next Get.
			return false, nil
		})
	if err != nil {
		return fmt.Errorf("delete fixture %T %s/%s: %w", obj, obj.GetNamespace(), obj.GetName(), errors.Join(err, lastErr))
	}
	return nil
}

// ossmLifecycle provides the istio GatewayClass that mcp-gateway requires. It
// reuses an existing OSSM3/Istio installation and installs only the parts
// that are missing. Teardown removes only what this run installed.
type ossmLifecycle struct {
	ownedFixtures
}

func (l *ossmLifecycle) Setup(ctx context.Context, cfg *envconf.Config) (err error) {
	r := cfg.Client().Resources()

	if gatewayClassAccepted(ctx, r, openshiftGatewayClass) {
		log.Printf("GatewayClass %s already present, using the existing Istio installation", openshiftGatewayClass)
		return nil
	}

	defer func() {
		// RunPackage only tears down lifecycles whose setup completed.
		if err != nil {
			err = errors.Join(err, l.Teardown(context.WithoutCancel(ctx), cfg))
		}
	}()

	subName, subNamespace, found, err := findOSSMSubscription(ctx, r)
	if err != nil {
		return err
	}
	if found {
		// Shared install: wait until its APIs are served, but do not record it.
		log.Printf("OSSM3 Subscription %s/%s already exists, waiting for its CSV...", subNamespace, subName)
		if err := waitForCSVSucceeded(ctx, r, subName, subNamespace, 5*time.Minute, nil); err != nil {
			return fmt.Errorf("wait for existing OSSM CSV: %w", err)
		}
	} else if err := l.installOSSMOperator(ctx, r); err != nil {
		return err
	}

	log.Println("ensuring IstioCNI and Istio control plane...")
	for _, obj := range []k8s.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: istioCNINamespace}},
		sailResource("IstioCNI", istioCNINamespace),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: istioNamespace}},
		sailResource("Istio", istioNamespace),
	} {
		if err := l.create(ctx, r, obj); err != nil {
			return fmt.Errorf("create %T %s: %w", obj, obj.GetName(), err)
		}
	}

	log.Printf("waiting for GatewayClass %s...", openshiftGatewayClass)
	if err := waitForGatewayClass(ctx, r, openshiftGatewayClass, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for GatewayClass %s: %w", openshiftGatewayClass, err)
	}
	log.Println("OSSM3 ready")
	return nil
}

func (l *ossmLifecycle) Teardown(ctx context.Context, cfg *envconf.Config) error {
	return l.deleteAll(ctx, cfg.Client().Resources())
}

// installOSSMOperator subscribes to the OSSM3 operator and records the
// Subscription and the CSV it installed. The CSV is recorded before the
// Subscription, so the reverse-order teardown deletes the Subscription first
// and OLM cannot reinstall the CSV in between.
func (l *ossmLifecycle) installOSSMOperator(ctx context.Context, r *resources.Resources) error {
	log.Println("installing OSSM3 operator...")
	sub := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"channel":             ossmChannel,
			"name":                ossmOLMPackage,
			"source":              ossmCatalog,
			"sourceNamespace":     ossmCatalogNamespace,
			"installPlanApproval": "Automatic",
		},
	}}
	sub.SetGroupVersionKind(olmV1alpha1.WithKind("Subscription"))
	sub.SetName(ossmOLMPackage)
	sub.SetNamespace(ossmNamespace)
	subIndex := len(l.objs)
	if err := l.create(ctx, r, sub); err != nil {
		return fmt.Errorf("create OSSM Subscription: %w", err)
	}

	var recordCSV func(*unstructured.Unstructured)
	if len(l.objs) > subIndex {
		// This run created the Subscription, so it also owns its CSVs. Record
		// each one as soon as it appears, so a failed wait still removes it.
		seen := map[types.UID]bool{}
		recordCSV = func(csv *unstructured.Unstructured) {
			if seen[csv.GetUID()] {
				return
			}
			seen[csv.GetUID()] = true
			l.objs = slices.Insert(l.objs, subIndex, k8s.Object(csv))
		}
	}

	log.Println("waiting for OSSM3 CSV to succeed...")
	if err := waitForCSVSucceeded(ctx, r, ossmOLMPackage, ossmNamespace, 5*time.Minute, recordCSV); err != nil {
		return fmt.Errorf("wait for OSSM CSV: %w", err)
	}
	return nil
}

// gatewayClassAccepted reports whether the GatewayClass exists and is Accepted.
func gatewayClassAccepted(ctx context.Context, r *resources.Resources, name string) bool {
	gc := &gatewayv1.GatewayClass{}
	if err := r.Get(ctx, name, "", gc); err != nil {
		return false
	}
	for _, c := range gc.Status.Conditions {
		if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// findOSSMSubscription looks for a Subscription for the OSSM3 package in any
// namespace. A second Subscription for the same operator would break OLM
// resolution, and CRDs alone are not a reliable signal because OLM leaves them
// behind after an uninstall.
func findOSSMSubscription(ctx context.Context, r *resources.Resources) (name, namespace string, found bool, err error) {
	subs := &unstructured.UnstructuredList{}
	subs.SetGroupVersionKind(olmV1alpha1.WithKind("SubscriptionList"))
	if err := r.List(ctx, subs); err != nil {
		return "", "", false, fmt.Errorf("list Subscriptions: %w", err)
	}
	for i := range subs.Items {
		pkg, _, _ := unstructured.NestedString(subs.Items[i].Object, "spec", "name")
		if pkg == ossmOLMPackage {
			return subs.Items[i].GetName(), subs.Items[i].GetNamespace(), true, nil
		}
	}
	return "", "", false, nil
}

func sailResource(kind, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"version":   ossmIstioVersion,
			"namespace": namespace,
		},
	}}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: sailGroup, Version: "v1", Kind: kind})
	obj.SetName("default")
	return obj
}

// waitForCSVSucceeded waits until the Subscription's installed CSV reports
// phase Succeeded. If onFound is set, it is called with
// the CSV each time it is read, before its phase is checked.
func waitForCSVSucceeded(ctx context.Context, r *resources.Resources, subName, ns string, timeout time.Duration, onFound func(*unstructured.Unstructured)) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			sub := &unstructured.Unstructured{}
			sub.SetGroupVersionKind(olmV1alpha1.WithKind("Subscription"))
			if err := r.Get(ctx, subName, ns, sub); err != nil {
				return false, nil
			}
			csvName, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
			if csvName == "" {
				return false, nil
			}

			current := &unstructured.Unstructured{}
			current.SetGroupVersionKind(olmV1alpha1.WithKind("ClusterServiceVersion"))
			if err := r.Get(ctx, csvName, ns, current); err != nil {
				return false, nil
			}
			if onFound != nil {
				onFound(current)
			}
			phase, _, _ := unstructured.NestedString(current.Object, "status", "phase")
			return phase == "Succeeded", nil
		})
}

// rhclLifecycle uses the shared RHCL installation. The fixture namespaces can
// contain shared resources and are left in place.
type rhclLifecycle struct {
	ownedFixtures
}

func (l *rhclLifecycle) Setup(ctx context.Context, cfg *envconf.Config) (err error) {
	r := cfg.Client().Resources()

	log.Println("waiting for mcp-gateway-controller (provided by RHCL)...")
	if err := waitForDeploymentAvailable(ctx, r, "app.kubernetes.io/name=mcp-gateway,component=controller", 5*time.Minute); err != nil {
		return fmt.Errorf("mcp-gateway-controller not available: %w", err)
	}

	defer func() {
		// RunPackage only tears down lifecycles whose setup completed.
		if err != nil {
			err = errors.Join(err, l.Teardown(context.WithoutCancel(ctx), cfg))
		}
	}()
	if err := l.setupGatewayInfrastructure(ctx, r); err != nil {
		return err
	}

	log.Println("waiting for MCPGatewayExtension to become ready...")
	if err := waitForExtensionReady(ctx, r, "mcp-gateway-extension", mcpSystemNamespace, 5*time.Minute); err != nil {
		return err
	}
	log.Println("setup complete")
	return nil
}

func (l *rhclLifecycle) Teardown(ctx context.Context, cfg *envconf.Config) error {
	return l.deleteAll(ctx, cfg.Client().Resources())
}

func (l *rhclLifecycle) setupGatewayInfrastructure(ctx context.Context, r *resources.Resources) error {
	log.Println("creating gateway infrastructure (RHCL mode)...")

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mcpSystemNamespace}}
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", mcpSystemNamespace, err)
	}

	gwNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gatewayNamespace}}
	if err := r.Create(ctx, gwNs); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", gatewayNamespace, err)
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
	if err := l.create(ctx, r, grant); err != nil {
		return fmt.Errorf("create ReferenceGrant: %w", err)
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
	if err := l.create(ctx, r, gw); err != nil {
		return fmt.Errorf("create Gateway %s/%s: %w", gatewayNamespace, gatewayName, err)
	}

	if err := kuadrantapi.AddToScheme(r.GetScheme()); err != nil {
		return fmt.Errorf("register Kuadrant types: %w", err)
	}

	ext := &kuadrantapi.MCPGatewayExtension{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mcp.kuadrant.io/v1",
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
	if err := l.create(ctx, r, ext); err != nil {
		return fmt.Errorf("create MCPGatewayExtension: %w", err)
	}

	return nil
}

func waitForDeploymentAvailable(ctx context.Context, r *resources.Resources, labelSelector string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			depList := &unstructured.UnstructuredList{}
			depList.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "apps", Version: "v1", Kind: "DeploymentList",
			})
			if err := r.List(ctx, depList, resources.WithLabelSelector(labelSelector)); err != nil {
				return false, nil
			}
			for i := range depList.Items {
				conditions, _, _ := unstructured.NestedSlice(depList.Items[i].Object, "status", "conditions")
				for _, c := range conditions {
					cond, ok := c.(map[string]any)
					if !ok {
						continue
					}
					if cond["type"] == "Available" && cond["status"] == "True" {
						return true, nil
					}
				}
			}
			return false, nil
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
