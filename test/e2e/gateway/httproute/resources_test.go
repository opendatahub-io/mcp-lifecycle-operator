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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mcpv1alpha1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1alpha1"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/scope"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
)

func TestHTTPRouteProviderResources(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-httproute-config"

	feature := features.New("HTTPRoute provider: resources").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.HTTPRoute).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			configData := prov.CopyConfigData()
			configData["section-name"] = listenerName
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "httproute-resources", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			bindingName := server.Name + "-gateway-binding"
			binding := &mcpv1alpha1.MCPGatewayBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bindingName,
					Namespace: server.Namespace,
				},
			}
			f.WaitForBindingRegistered(ctx, t, r, binding, metav1.ConditionTrue)
			return ctx
		}).
		Assess("HTTPRoute is created with correct spec", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			route := &gatewayv1.HTTPRoute{}
			if err := r.Get(ctx, bindingName, server.Namespace, route); err != nil {
				t.Fatalf("HTTPRoute not found: %v", err)
			}

			if len(route.Spec.ParentRefs) != 1 {
				t.Fatalf("expected 1 parentRef, got %d", len(route.Spec.ParentRefs))
			}
			gwName := prov.ConfigData["gateway-name"]
			gwNamespace := prov.ConfigData["gateway-namespace"]
			if string(route.Spec.ParentRefs[0].Name) != gwName {
				t.Fatalf("expected parentRef name %s, got %s", gwName, route.Spec.ParentRefs[0].Name)
			}
			if route.Spec.ParentRefs[0].Namespace == nil || string(*route.Spec.ParentRefs[0].Namespace) != gwNamespace {
				t.Fatalf("expected parentRef namespace %s", gwNamespace)
			}

			if len(route.Spec.Rules) != 1 || len(route.Spec.Rules[0].BackendRefs) != 1 {
				t.Fatal("expected 1 rule with 1 backendRef")
			}
			if string(route.Spec.Rules[0].BackendRefs[0].Name) != server.Name {
				t.Fatalf("expected backendRef name %s, got %s", server.Name, route.Spec.Rules[0].BackendRefs[0].Name)
			}

			hostname := prov.ConfigData["route-hostname"]
			if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != hostname {
				t.Fatalf("expected hostname %s, got %v", hostname, route.Spec.Hostnames)
			}

			ownerRef := metav1.GetControllerOf(route)
			if ownerRef == nil || ownerRef.Kind != "MCPGatewayBinding" {
				t.Fatal("HTTPRoute should be owned by MCPGatewayBinding")
			}
			t.Logf("HTTPRoute %s verified", bindingName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestHTTPRouteFallbackToRouteHostname(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-fallback-route-config"

	feature := features.New("HTTPRoute: fallback to route-hostname").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.HTTPRoute).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			configData := map[string]string{
				"gateway-name":      prov.ConfigData["gateway-name"],
				"gateway-namespace": prov.ConfigData["gateway-namespace"],
				"section-name":      listenerName,
				"route-hostname":    "route.mcp.local",
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "fallback-route", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL uses route-hostname as fallback", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "route.mcp.local", "/mcp")
			t.Logf("status.address.url correctly falls back to route-hostname: %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestHTTPRouteFallbackToGatewayAddress(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-fallback-addr-config"

	var gwAddr string

	feature := features.New("HTTPRoute: fallback to gateway address").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.HTTPRoute).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			gwAddr = f.WaitForGatewayAddress(ctx, t, cfg.Client().Resources(), prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"])

			configData := map[string]string{
				"gateway-name":      prov.ConfigData["gateway-name"],
				"gateway-namespace": prov.ConfigData["gateway-namespace"],
				"section-name":      listenerName,
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "fallback-addr", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL contains gateway LoadBalancer address", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			statusURL := server.Status.Address.URL
			if !strings.Contains(statusURL, gwAddr) {
				t.Fatalf("expected status.address.url to contain gateway address %s, got %s", gwAddr, statusURL)
			}
			t.Logf("status.address.url uses gateway LoadBalancer address: %s", statusURL)
			return ctx
		}).
		Assess("MCP server is reachable via gateway address", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.AssertMCPReachable(ctx, t, gwAddr, gwAddr, "/mcp")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}
