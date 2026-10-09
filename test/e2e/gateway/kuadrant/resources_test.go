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
	"crypto/sha256"
	"encoding/hex"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mcpv1alpha1 "github.com/kubernetes-sigs/mcp-lifecycle-operator/api/v1alpha1"
	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/scope"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
)

func TestKuadrantProviderResources(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-kuadrant-config"

	feature := features.New("Kuadrant provider: resources").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.WaitForExtensionReady(ctx, t, cfg, prov.ConfigData["extension-name"], prov.ConfigData["extension-namespace"])

			configData := f.BuildControllerConfigData(prov, listenerName)
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "kuadrant-resources", false,
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
		Assess("MCPServerRegistration is created", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			reg := &kuadrantapi.MCPServerRegistration{}
			if err := r.Get(ctx, bindingName, server.Namespace, reg); err != nil {
				t.Fatalf("MCPServerRegistration not found: %v", err)
			}

			if reg.Spec.TargetRef.Group != "gateway.networking.k8s.io" {
				t.Fatalf("expected targetRef group gateway.networking.k8s.io, got %s", reg.Spec.TargetRef.Group)
			}
			if reg.Spec.TargetRef.Kind != "HTTPRoute" {
				t.Fatalf("expected targetRef kind HTTPRoute, got %s", reg.Spec.TargetRef.Kind)
			}
			if reg.Spec.TargetRef.Name != bindingName {
				t.Fatalf("expected targetRef name %s, got %s", bindingName, reg.Spec.TargetRef.Name)
			}
			if reg.Spec.Path != "/mcp" {
				t.Fatalf("expected path /mcp, got %s", reg.Spec.Path)
			}
			if reg.Spec.State != "Enabled" {
				t.Fatalf("expected state Enabled, got %s", reg.Spec.State)
			}

			ownerRef := metav1.GetControllerOf(reg)
			if ownerRef == nil || ownerRef.Kind != "MCPGatewayBinding" {
				t.Fatal("MCPServerRegistration should be owned by MCPGatewayBinding")
			}
			t.Logf("MCPServerRegistration %s verified", bindingName)
			return ctx
		}).
		Assess("HTTPRoute is created with correct owner", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			route := &gatewayv1.HTTPRoute{}
			if err := r.Get(ctx, bindingName, server.Namespace, route); err != nil {
				t.Fatalf("HTTPRoute not found: %v", err)
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

func TestKuadrantAutoConstructedHostname(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-auto-hostname-config"

	feature := features.New("Kuadrant: auto-constructed hostname from wildcard listener").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			wildcard := gatewayv1.Hostname("*.mcp.local")
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-wildcard-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcps", Port: 80, Protocol: gatewayv1.HTTPProtocolType, Hostname: &wildcard},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"auto-host-ext", ns,
				"kuadrant-wildcard-gw", prov.ConfigData["gateway-namespace"],
				f.WithSectionName("mcps"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "auto-host-ext", ns)

			configData := map[string]string{
				"extension-name":      "auto-host-ext",
				"extension-namespace": ns,
				"section-name":        "mcps",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "auto-host", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			return ctx
		}).
		Assess("HTTPRoute hostname is auto-constructed from wildcard", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			binding := &mcpv1alpha1.MCPGatewayBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: server.Namespace},
			}
			// Registered=False is expected: without an MCPGatewayExtension the
			// MCPServerRegistration won't become ready, but the HTTPRoute (which
			// we verify below) is created before the registration gate.
			f.WaitForBindingRegistered(ctx, t, r, binding, metav1.ConditionFalse)

			route := &gatewayv1.HTTPRoute{}
			if err := r.Get(ctx, bindingName, server.Namespace, route); err != nil {
				t.Fatalf("HTTPRoute not found: %v", err)
			}

			expectedHostname := server.Name + "." + server.Namespace + ".mcp.local"
			if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != expectedHostname {
				t.Fatalf("expected HTTPRoute hostname %s, got %v", expectedHostname, route.Spec.Hostnames)
			}
			t.Logf("HTTPRoute hostname auto-constructed: %s", expectedHostname)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantDefaultSectionName(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-default-section-config"

	feature := features.New("Kuadrant: default sectionName mcps").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			wildcard := gatewayv1.Hostname("*.mcp.local")
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-default-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcps", Port: 80, Protocol: gatewayv1.HTTPProtocolType, Hostname: &wildcard},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"default-ext", ns,
				"kuadrant-default-gw", prov.ConfigData["gateway-namespace"],
				f.WithPublicHost("default-sec.public.example.com"),
				f.WithSectionName("mcps"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "default-ext", ns)

			configData := map[string]string{
				"extension-name":      "default-ext",
				"extension-namespace": ns,
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "default-sec", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionTrue)
			return ctx
		}).
		Assess("GatewayRegistered=True with default mcps sectionName", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			cond := f.GetMCPServerCondition(server, "GatewayRegistered")
			if cond == nil || cond.Status != metav1.ConditionTrue {
				t.Fatal("expected GatewayRegistered=True with default sectionName mcps")
			}
			t.Log("GatewayRegistered=True with default sectionName mcps (no section-name in ConfigMap)")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantPublicHostnamePriority(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-priority-config"

	feature := features.New("Kuadrant: extension publicHost appears in status URL").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-priority-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"priority-ext", ns,
				"kuadrant-priority-gw", prov.ConfigData["gateway-namespace"],
				f.WithPublicHost("extension.example.com"),
				f.WithSectionName("http"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "priority-ext", ns)

			configData := map[string]string{
				"extension-name":      "priority-ext",
				"extension-namespace": ns,
				"section-name":        "http",
				"route-hostname":      "route.mcp.local",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "priority", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL uses extension publicHost", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "extension.example.com", "/mcp")
			t.Logf("extension publicHost used in status URL: %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantExtensionFallback(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-ext-fallback-config"

	feature := features.New("Kuadrant: direct extension reference with publicHost").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			wildcard := gatewayv1.Hostname("*.mcp.local")
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-ext-fallback-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcp", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
					{Name: "mcps", Port: 80, Protocol: gatewayv1.HTTPProtocolType, Hostname: &wildcard},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"fallback-ext", ns,
				"kuadrant-ext-fallback-gw", prov.ConfigData["gateway-namespace"],
				f.WithPublicHost("public.example.com"),
				f.WithSectionName("mcp"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "fallback-ext", ns)

			configData := map[string]string{
				"extension-name":      "fallback-ext",
				"extension-namespace": ns,
				"section-name":        "mcps",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "ext-fallback", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL uses extension publicHost", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "public.example.com", "/mcp")
			t.Logf("extension publicHost used via direct reference: %s", server.Status.Address.URL)
			return ctx
		}).
		Assess("HTTPRoute uses ConfigMap section-name override", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			route := &gatewayv1.HTTPRoute{}
			if err := r.Get(ctx, bindingName, server.Namespace, route); err != nil {
				t.Fatalf("HTTPRoute not found: %v", err)
			}

			if len(route.Spec.ParentRefs) == 0 {
				t.Fatal("HTTPRoute has no parentRefs")
			}
			parentRef := route.Spec.ParentRefs[0]
			if parentRef.SectionName == nil || string(*parentRef.SectionName) != "mcps" {
				actual := "<nil>"
				if parentRef.SectionName != nil {
					actual = string(*parentRef.SectionName)
				}
				t.Fatalf("expected HTTPRoute parentRef.sectionName 'mcps' (from ConfigMap), got %s", actual)
			}
			t.Log("HTTPRoute parentRef.sectionName = mcps (ConfigMap override, not extension's 'mcp')")

			expectedHostname := server.Name + "." + server.Namespace + ".mcp.local"
			if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != expectedHostname {
				t.Fatalf("expected HTTPRoute hostname %s (auto-constructed from wildcard), got %v", expectedHostname, route.Spec.Hostnames)
			}
			t.Logf("HTTPRoute hostname auto-constructed from wildcard listener: %s", expectedHostname)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantExtensionNotFound(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-ext-notfound-config"

	feature := features.New("Kuadrant: GatewayRegistered=False when extension not found").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			configData := map[string]string{
				"extension-name":      "nonexistent-extension",
				"extension-namespace": prov.ConfigData["gateway-namespace"],
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "ext-notfound", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			return ctx
		}).
		Assess("GatewayRegistered=False due to missing extension", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionFalse)
			t.Log("GatewayRegistered=False as expected with nonexistent extension")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantExtensionNotReady(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-ext-notready-config"

	feature := features.New("Kuadrant: GatewayRegistered=False when extension not ready").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"notready-ext", ns,
				"nonexistent-gateway", prov.ConfigData["gateway-namespace"],
				f.WithPublicHost("notready.example.com"),
				f.WithSectionName("mcp"),
			)

			configData := map[string]string{
				"extension-name":      "notready-ext",
				"extension-namespace": ns,
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "ext-notready", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			return ctx
		}).
		Assess("GatewayRegistered=False due to extension not ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerCondition(ctx, t, r, server, "GatewayRegistered", metav1.ConditionFalse)
			t.Log("GatewayRegistered=False as expected with extension not ready")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantCrossNamespaceExtension(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-cross-ns-config"

	feature := features.New("Kuadrant: cross-namespace extension reference").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-cross-ns-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcp", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"cross-ns-ext", ns,
				"kuadrant-cross-ns-gw", prov.ConfigData["gateway-namespace"],
				f.WithPublicHost("cross-ns.example.com"),
				f.WithSectionName("mcp"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "cross-ns-ext", ns)

			configData := map[string]string{
				"extension-name":      "cross-ns-ext",
				"extension-namespace": ns,
				"section-name":        "mcp",
				"route-hostname":      "cross-ns.mcp.local",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "cross-ns", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("extension in test namespace is discovered", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "cross-ns.example.com", "/mcp")
			t.Logf("cross-namespace extension discovered: %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantPublicAddressPending(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-pubaddr-pending-config"

	feature := features.New("Kuadrant: PublicAddressPending when wildcard listener and no publicHost").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			wildcard := gatewayv1.Hostname("*.mcp.local")
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-pubaddr-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcps", Port: 80, Protocol: gatewayv1.HTTPProtocolType, Hostname: &wildcard},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"pubaddr-ext", ns,
				"kuadrant-pubaddr-gw", prov.ConfigData["gateway-namespace"],
				f.WithSectionName("mcps"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "pubaddr-ext", ns)

			configData := map[string]string{
				"extension-name":      "pubaddr-ext",
				"extension-namespace": ns,
				"section-name":        "mcps",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "pubaddr-pend", false,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)
			return ctx
		}).
		Assess("GatewayRegistered=False with PublicAddressPending message", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			f.WaitForMCPServerConditionMessageContains(ctx, t, r, server,
				"GatewayRegistered", metav1.ConditionFalse, "GatewayNotRegistered", "Waiting for public address")
			t.Log("GatewayRegistered=False with PublicAddressPending message (wildcard listener, no publicHost)")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantListenerHostnameFallback(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-listener-fallback-config"

	feature := features.New("Kuadrant: fallback to listener hostname when extension has no publicHost").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			listenerHost := gatewayv1.Hostname("listener.mcp.local")
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"kuadrant-listener-fallback-gw", prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"],
				[]f.ListenerSpec{
					{Name: "mcp", Port: 80, Protocol: gatewayv1.HTTPProtocolType, Hostname: &listenerHost},
				},
			)

			f.CreateMCPGatewayExtension(ctx, t, cfg,
				"listener-ext", ns,
				"kuadrant-listener-fallback-gw", prov.ConfigData["gateway-namespace"],
				f.WithSectionName("mcp"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, "listener-ext", ns)

			configData := map[string]string{
				"extension-name":      "listener-ext",
				"extension-namespace": ns,
				"section-name":        "mcp",
				"route-hostname":      "listener.mcp.local",
				"prefix":              prov.ConfigData["prefix"],
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "listener-fb", true,
				f.WithGateway(prov.Name, configMapName),
				f.WithPath("/mcp"),
			)

			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()
			f.WaitForMCPServerGatewayAddress(ctx, t, r, server)
			return ctx
		}).
		Assess("status URL uses listener hostname as fallback", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			if err := r.Get(ctx, server.Name, server.Namespace, server); err != nil {
				t.Fatalf("failed to get MCPServer: %v", err)
			}

			f.AssertGatewayAddressURL(t, server, "listener.mcp.local", "/mcp")
			t.Logf("listener hostname used as fallback (no publicHost on extension): %s", server.Status.Address.URL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantAutoGeneratedPrefix(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-auto-prefix-config"

	feature := features.New("Kuadrant: auto-generated prefix from MCPServer name and namespace").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.WaitForExtensionReady(ctx, t, cfg, prov.ConfigData["extension-name"], prov.ConfigData["extension-namespace"])

			configData := f.BuildControllerConfigData(prov, listenerName)
			delete(configData, "prefix")
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "auto-prefix", false,
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
		Assess("MCPServerRegistration has auto-generated prefix", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			reg := &kuadrantapi.MCPServerRegistration{}
			if err := r.Get(ctx, bindingName, server.Namespace, reg); err != nil {
				t.Fatalf("MCPServerRegistration not found: %v", err)
			}

			h := sha256.Sum256([]byte(server.Name + "/" + server.Namespace))
			expectedPrefix := "mcp_" + hex.EncodeToString(h[:4]) + "_"
			if reg.Spec.Prefix != expectedPrefix {
				t.Fatalf("expected auto-generated prefix %q, got %q", expectedPrefix, reg.Spec.Prefix)
			}
			t.Logf("MCPServerRegistration prefix auto-generated: %s", reg.Spec.Prefix)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}

func TestKuadrantExplicitPrefixOverride(t *testing.T) {
	prov := f.ActiveProvider(t)
	const configMapName = "gw-explicit-prefix-config"

	feature := features.New("Kuadrant: explicit prefix overrides auto-generation").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Moderate).
		WithLabel(scope.Label, scope.Kuadrant).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			listenerName := f.EnsureGateway(ctx, t, cfg, prov.ConfigData["gateway-name"], prov.ConfigData["gateway-namespace"], prov.ConfigData["gateway-class"])
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.ConfigData["gateway-namespace"])

			f.WaitForExtensionReady(ctx, t, cfg, prov.ConfigData["extension-name"], prov.ConfigData["extension-namespace"])

			configData := f.BuildControllerConfigData(prov, listenerName)
			configData["prefix"] = "custom_explicit_"
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "explicit-pfx", false,
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
		Assess("MCPServerRegistration uses explicit prefix from ConfigMap", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			server := f.ServerFromContext(ctx)
			r := cfg.Client().Resources()

			bindingName := server.Name + "-gateway-binding"
			reg := &kuadrantapi.MCPServerRegistration{}
			if err := r.Get(ctx, bindingName, server.Namespace, reg); err != nil {
				t.Fatalf("MCPServerRegistration not found: %v", err)
			}

			if reg.Spec.Prefix != "custom_explicit_" {
				t.Fatalf("expected explicit prefix %q, got %q", "custom_explicit_", reg.Spec.Prefix)
			}
			t.Logf("MCPServerRegistration uses explicit prefix: %s", reg.Spec.Prefix)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return f.TeardownMCPServer(ctx, t, cfg)
		}).
		Feature()

	testenv.Test(t, feature)
}
