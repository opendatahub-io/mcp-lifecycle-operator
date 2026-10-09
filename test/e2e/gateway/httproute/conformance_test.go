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
	"net/url"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mcpv1alpha1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1alpha1"
	mcpv1beta1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1beta1"
	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/scope"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
)

var registerConformanceKuadrantOnce sync.Once

func ensureConformanceKuadrantScheme(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	registerConformanceKuadrantOnce.Do(func() {
		if err := kuadrantapi.AddToScheme(scheme); err != nil {
			t.Fatalf("failed to register Kuadrant types: %v", err)
		}
	})
}

func setupKuadrantExtension(ctx context.Context, t *testing.T, cfg *envconf.Config, prov f.ProviderConfig, ns string) {
	t.Helper()
	if prov.Name != "kuadrant" {
		return
	}
	ensureConformanceKuadrantScheme(t, cfg.Client().Resources().GetScheme())
	f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])
	f.WaitForExtensionReady(ctx, t, cfg, prov.ConfigData["extension-name"], prov.ConfigData["extension-namespace"])
}

func TestGatewayConformanceBindingLifecycle(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-conformance-config"

	feature := features.New("Gateway conformance: binding lifecycle").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			setupKuadrantExtension(ctx, t, cfg, prov, ns)
			configData := f.BuildControllerConfigData(prov, listenerName)
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			return f.SetupMCPServer(ctx, t, cfg, "conformance-lifecycle", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
		}).
		Assess("MCPGatewayBinding is created and Registered", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
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

			if err := r.Get(ctx, bindingName, server.Namespace, binding); err != nil {
				t.Fatalf("failed to get MCPGatewayBinding: %v", err)
			}
			if binding.Spec.Provider != prov.Name {
				t.Fatalf("expected provider %s, got %s", prov.Name, binding.Spec.Provider)
			}
			if binding.Spec.MCPServerRef != server.Name {
				t.Fatalf("expected mcpServerRef %s, got %s", server.Name, binding.Spec.MCPServerRef)
			}
			t.Logf("MCPGatewayBinding %s is Registered", bindingName)
			return ctx
		}).
		Assess("MCPServer reflects gateway status", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}
			if server.Status.Address == nil || server.Status.Address.URL == "" {
				t.Fatal("expected status.address.url to be set")
			}
			t.Logf("MCPServer gateway status verified: address=%s", server.Status.Address.URL)
			return ctx
		}).
		Assess("MCPServer is fully ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerReconciledAndReady(ctx, t, r, server)
			t.Log("MCPServer is Available and Verified")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestGatewayConformanceRemoval(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-removal-config"

	feature := features.New("Gateway conformance: removal").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			setupKuadrantExtension(ctx, t, cfg, prov, ns)
			configData := f.BuildControllerConfigData(prov, listenerName)
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "conformance-removal", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionTrue)
			return ctx
		}).
		Assess("remove gateway from spec", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.UpdateWithRetry(ctx, t, r, server, func(s *mcpv1beta1.MCPServer) {
				s.Spec.Gateway = nil
			})
			t.Log("removed spec.gateway from MCPServer")
			return ctx
		}).
		Assess("binding is deleted", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			bindingName := server.Name + "-gateway-binding"
			binding := &mcpv1alpha1.MCPGatewayBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bindingName,
					Namespace: server.Namespace,
				},
			}
			f.WaitForBindingDeleted(ctx, t, r, binding)
			t.Logf("MCPGatewayBinding %s deleted", bindingName)
			return ctx
		}).
		Assess("MCPServer address reverts to service URL", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			f.WaitForMCPServerReconciledAndReady(ctx, t, r, server)

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertAddressURL(t, server, 8080)
			t.Logf("MCPServer address reverted to service URL: %s", server.Status.Address.URL)

			cond := f.GetMCPServerCondition(server, "GatewayRegistered")
			if cond != nil {
				t.Fatalf("expected no GatewayRegistered condition after removal, but found one: %s", cond.Status)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestGatewayConformanceHTTPReachability(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-reachability-config"

	var gwAddr string

	feature := features.New("Gateway conformance: HTTP reachability").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			gwAddr = f.WaitForGatewayAddress(ctx, t, cfg.Client().Resources(), prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"])

			setupKuadrantExtension(ctx, t, cfg, prov, ns)
			configData := f.BuildControllerConfigData(prov, listenerName)
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "conformance-http", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerReconciledAndReady(ctx, t, r, server)
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionTrue)
			return ctx
		}).
		Assess("MCP handshake through gateway", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}
			if server.Status.Address == nil || server.Status.Address.URL == "" {
				t.Fatal("status.address.url is not set")
			}

			parsed, err := url.Parse(server.Status.Address.URL)
			if err != nil {
				t.Fatalf("failed to parse status.address.url %q: %v", server.Status.Address.URL, err)
			}

			f.AssertMCPReachable(ctx, t, gwAddr, parsed.Hostname(), parsed.Path)

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestGatewayConformanceHostnameSeparation(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-hostname-sep-config"

	var gwAddr string

	feature := features.New("Gateway conformance: hostname separation").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			gwAddr = f.WaitForGatewayAddress(ctx, t, cfg.Client().Resources(), prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"])

			setupKuadrantExtension(ctx, t, cfg, prov, ns)
			configData := f.BuildControllerConfigData(prov, listenerName)
			configData["route-hostname"] = "internal.mcp.local"
			configData["public-hostname"] = "public.example.com"
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "hostname-sep", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL does not use route-hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			parsed, err := url.Parse(server.Status.Address.URL)
			if err != nil {
				t.Fatalf("failed to parse status URL %q: %v", server.Status.Address.URL, err)
			}
			if parsed.Hostname() == "internal.mcp.local" {
				t.Fatalf("status URL hostname must differ from route-hostname, got %s", server.Status.Address.URL)
			}
			t.Logf("status URL hostname (%s) differs from route-hostname (internal.mcp.local)", parsed.Hostname())
			return ctx
		}).
		Assess("HTTPRoute uses route-hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			route := &gatewayv1.HTTPRoute{}
			if err := r.Get(ctx, bindingName, server.Namespace, route); err != nil {
				t.Fatalf("HTTPRoute not found: %v", err)
			}

			if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != "internal.mcp.local" {
				t.Fatalf("expected HTTPRoute hostname internal.mcp.local, got %v", route.Spec.Hostnames)
			}
			t.Log("HTTPRoute correctly uses route-hostname: internal.mcp.local")
			return ctx
		}).
		Assess("MCP server is reachable via route hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.AssertMCPReachable(ctx, t, gwAddr, "internal.mcp.local", "/mcp")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestGatewayConformanceRecoverOnConfigMapUpdate(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-recover-config"

	feature := features.New("Gateway conformance: recover on ConfigMap update").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			var configData map[string]string
			switch prov.Name {
			case "kuadrant":
				ensureConformanceKuadrantScheme(t, cfg.Client().Resources().GetScheme())
				configData = map[string]string{
					"extension-name":      "nonexistent-extension",
					"extension-namespace": prov.ConfigData["gateway-namespace"],
					"section-name":        listenerName,
					"route-hostname":      "recover.mcp.local",
					"prefix":              prov.ConfigData["prefix"],
				}
			default:
				configData = map[string]string{
					"gateway-name":      "nonexistent-gateway",
					"gateway-namespace": prov.ConfigData["gateway-namespace"],
					"section-name":      listenerName,
					"route-hostname":    "recover.mcp.local",
					"public-hostname":   "recover.mcp.local",
				}
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "recover-cfg", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			return ctx
		}).
		Assess("GatewayRegistered=False with invalid gateway", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionFalse)
			t.Log("GatewayRegistered=False as expected with invalid gateway reference")
			return ctx
		}).
		Assess("update ConfigMap to valid gateway", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)

			var configData map[string]string
			switch prov.Name {
			case "kuadrant":
				setupKuadrantExtension(ctx, t, cfg, prov, ns)
				configData = f.BuildControllerConfigData(prov, "")
			default:
				listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
				configData = map[string]string{
					"gateway-name":      prov.ConfigData["gateway-name"],
					"gateway-namespace": prov.ConfigData["gateway-namespace"],
					"section-name":      listenerName,
					"route-hostname":    "recover.mcp.local",
					"public-hostname":   "recover.mcp.local",
				}
			}
			f.UpdateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			t.Log("updated ConfigMap to valid gateway reference")
			return ctx
		}).
		Assess("GatewayRegistered recovers to True", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}
			if prov.Name != "kuadrant" {
				f.AssertGatewayAddressURL(t, server, "recover.mcp.local", "/mcp")
			}
			t.Logf("GatewayRegistered recovered to True with address: %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestGatewayConformanceConfigMapUpdateTriggersStatusUpdate(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-cm-update-config"

	var sectionName string
	var extName, extNs string

	feature := features.New("Gateway conformance: ConfigMap update triggers status update").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)

			var configData map[string]string
			switch prov.Name {
			case "kuadrant":
				ensureConformanceKuadrantScheme(t, cfg.Client().Resources().GetScheme())
				f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])
				f.EnsureMultiListenerGateway(ctx, t, cfg,
					"cfg-update-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
					[]f.ListenerSpec{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
				)
				sectionName = "http"
				extName = "cfg-update-ext"
				extNs = ns
				f.CreateMCPGatewayExtension(ctx, t, cfg,
					extName, extNs,
					"cfg-update-gw", prov.ConfigData["gateway-namespace"],
					f.WithPublicHost("first.mcp.local"),
					f.WithSectionName("http"),
				)
				f.WaitForExtensionReady(ctx, t, cfg, extName, extNs)
				configData = map[string]string{
					"extension-name":      extName,
					"extension-namespace": extNs,
					"section-name":        "http",
					"route-hostname":      "first.mcp.local",
					"prefix":              prov.ConfigData["prefix"],
				}
			default:
				listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
				sectionName = listenerName
				configData = map[string]string{
					"gateway-name":      prov.ConfigData["gateway-name"],
					"gateway-namespace": prov.ConfigData["gateway-namespace"],
					"section-name":      listenerName,
					"route-hostname":    "first.mcp.local",
					"public-hostname":   "first.mcp.local",
				}
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "cm-update", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("initial status uses first hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "first.mcp.local", "/mcp")
			t.Logf("initial status uses first hostname: %s", server.Status.Address.URL)
			return ctx
		}).
		Assess("update ConfigMap to second hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			var configData map[string]string
			switch prov.Name {
			case "kuadrant":
				ext := &kuadrantapi.MCPGatewayExtension{
					ObjectMeta: metav1.ObjectMeta{Name: extName, Namespace: extNs},
				}
				f.UpdateWithRetry(ctx, t, cfg.Client().Resources(), ext, func(e *kuadrantapi.MCPGatewayExtension) {
					e.Spec.PublicHost = "second.mcp.local"
				})
				configData = map[string]string{
					"extension-name":      extName,
					"extension-namespace": extNs,
					"section-name":        sectionName,
					"route-hostname":      "second.mcp.local",
					"prefix":              prov.ConfigData["prefix"],
				}
			default:
				configData = map[string]string{
					"gateway-name":      prov.ConfigData["gateway-name"],
					"gateway-namespace": prov.ConfigData["gateway-namespace"],
					"section-name":      sectionName,
					"route-hostname":    "second.mcp.local",
					"public-hostname":   "second.mcp.local",
				}
			}
			f.UpdateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			t.Log("updated ConfigMap to second.mcp.local")
			return ctx
		}).
		Assess("status URL updates to second hostname", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			f.WaitForMCPServerAddressContains(ctx, t, r, server, "second.mcp.local")

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "second.mcp.local", "/mcp")
			t.Logf("status URL updated to second hostname: %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}
