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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
	f "github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/scope"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/gateway/conformance"
)

func TestGatewayConformance(t *testing.T) {
	conformance.RunGatewayConformanceSuite(t, testenv, newKuadrantProvider())
}

func TestGatewayConformanceConfigMapUpdateTriggersStatusUpdate(t *testing.T) {
	prov := newKuadrantProvider()
	const configMapName = "gw-cm-update-config"

	var extName, extNs string

	feature := features.New("Gateway conformance: ConfigMap update triggers status update").
		WithLabel(category.Label, category.Networking).
		WithLabel(speed.Label, speed.Slow).
		WithLabel(scope.Label, scope.GatewayConformance).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			ns := ctx.Value(f.NsKey).(string)
			ensureScheme(t, cfg.Client().Resources().GetScheme())
			f.EnsureReferenceGrant(ctx, t, cfg, ns, prov.gatewayNamespace)
			_, gwClass := f.DiscoverGateway(ctx, t, cfg, prov.gatewayName, prov.gatewayNamespace)
			f.EnsureMultiListenerGateway(ctx, t, cfg,
				"cfg-update-gw", prov.gatewayNamespace, gwClass,
				[]f.ListenerSpec{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
			)
			extName = "cfg-update-ext"
			extNs = ns
			f.CreateMCPGatewayExtension(ctx, t, cfg,
				extName, extNs,
				"cfg-update-gw", prov.gatewayNamespace,
				f.WithPublicHost("first.mcp.local"),
				f.WithSectionName("http"),
			)
			f.WaitForExtensionReady(ctx, t, cfg, extName, extNs)
			configData := map[string]string{
				"extension-name":      extName,
				"extension-namespace": extNs,
				"section-name":        "http",
				"route-hostname":      "first.mcp.local",
				"prefix":              prov.prefix,
			}
			f.CreateGatewayConfigMap(ctx, t, cfg, configMapName, ns, configData)
			ctx = f.SetupMCPServer(ctx, t, cfg, "cm-update", true,
				f.WithGateway(prov.Name(), configMapName),
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
			ext := &kuadrantapi.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: extName, Namespace: extNs},
			}
			f.UpdateWithRetry(ctx, t, cfg.Client().Resources(), ext, func(e *kuadrantapi.MCPGatewayExtension) {
				e.Spec.PublicHost = "second.mcp.local"
			})
			configData := map[string]string{
				"extension-name":      extName,
				"extension-namespace": extNs,
				"section-name":        "http",
				"route-hostname":      "second.mcp.local",
				"prefix":              prov.prefix,
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
