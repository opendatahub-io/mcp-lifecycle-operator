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

package framework

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	kuadrantapi "github.com/kubernetes-sigs/mcp-lifecycle-operator/internal/controller/providers/kuadrant/api"
)

const (
	configKeyGatewayName      = "gateway-name"
	configKeyGatewayNamespace = "gateway-namespace"
	configKeyGatewayClass     = "gateway-class"
	configKeyRouteHostname    = "route-hostname"
	configKeyPublicHostname   = "public-hostname"
	configKeyExtensionName    = "extension-name"
	configKeyExtensionNS      = "extension-namespace"
	configKeyPrefix           = "prefix"

	providerKuadrant = "kuadrant"

	defaultRouteHostname = "mcp.e2e.test"
)

// ProviderConfig describes a gateway provider for conformance testing.
type ProviderConfig struct {
	Name       string
	ConfigData map[string]string
}

// CopyConfigData returns a shallow copy of the provider's ConfigData map,
// safe to mutate without affecting the global registry.
func (p ProviderConfig) CopyConfigData() map[string]string {
	cp := make(map[string]string, len(p.ConfigData))
	maps.Copy(cp, p.ConfigData)
	return cp
}

var providers = map[string]ProviderConfig{
	"httproute": {
		Name: "httproute",
		ConfigData: map[string]string{
			configKeyGatewayName:      "e2e-gateway",
			configKeyGatewayNamespace: "gateway-system",
			configKeyGatewayClass:     "eg",
			configKeyRouteHostname:    defaultRouteHostname,
			configKeyPublicHostname:   defaultRouteHostname,
		},
	},
	providerKuadrant: {
		Name: providerKuadrant,
		ConfigData: map[string]string{
			configKeyGatewayName:      "mcp-gateway",
			configKeyGatewayNamespace: "gateway-system",
			configKeyGatewayClass:     "istio",
			configKeyRouteHostname:    defaultRouteHostname,
			configKeyExtensionName:    "mcp-gateway-extension",
			configKeyExtensionNS:      "mcp-system",
			configKeyPrefix:           "e2e_",
		},
	},
}

// ActiveProvider returns the ProviderConfig selected by the GATEWAY_PROVIDER
// environment variable. It fatals if the variable is unset or unknown.
func ActiveProvider(t *testing.T) ProviderConfig {
	t.Helper()
	name := os.Getenv("GATEWAY_PROVIDER")
	if name == "" {
		t.Fatal("GATEWAY_PROVIDER environment variable is not set")
	}
	prov, ok := providers[name]
	if !ok {
		t.Fatalf("unknown gateway provider %q, available: %v", name, providerNames())
	}
	return prov
}

func providerNames() []string {
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	return names
}

type hostOverrideTransport struct {
	base http.RoundTripper
	host string
}

func (t *hostOverrideTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = t.host
	return t.base.RoundTrip(req)
}

// WithHostOverride wraps the given http.Client's transport so that every
// outgoing request sets the Host header to the given value. This is needed
// when the MCP SDK client creates its own requests internally and the
// gateway performs host-based routing.
func WithHostOverride(c *http.Client, host string) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport: &hostOverrideTransport{base: base, host: host},
	}
}

// MCPGatewayExtensionOption configures an MCPGatewayExtension for testing.
type MCPGatewayExtensionOption func(*kuadrantapi.MCPGatewayExtension)

// WithPublicHost sets the publicHost on the MCPGatewayExtension.
func WithPublicHost(host string) MCPGatewayExtensionOption {
	return func(ext *kuadrantapi.MCPGatewayExtension) {
		ext.Spec.PublicHost = host
	}
}

// WithSectionName sets the sectionName on the MCPGatewayExtension's targetRef.
func WithSectionName(name string) MCPGatewayExtensionOption {
	return func(ext *kuadrantapi.MCPGatewayExtension) {
		ext.Spec.TargetRef.SectionName = name
	}
}

// CreateMCPGatewayExtension creates an MCPGatewayExtension that targets the
// given Gateway. The caller must ensure the kuadrant scheme is registered.
func CreateMCPGatewayExtension(ctx context.Context, t *testing.T, cfg *envconf.Config,
	name, namespace, gatewayName, gatewayNamespace string, opts ...MCPGatewayExtensionOption) *kuadrantapi.MCPGatewayExtension {
	t.Helper()

	ext := &kuadrantapi.MCPGatewayExtension{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mcp.kuadrant.io/v1alpha1",
			Kind:       "MCPGatewayExtension",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kuadrantapi.MCPGatewayExtensionSpec{
			TargetRef: kuadrantapi.TargetReference{
				Group:     gatewayv1.GroupName,
				Kind:      "Gateway",
				Name:      gatewayName,
				Namespace: gatewayNamespace,
			},
		},
	}
	for _, opt := range opts {
		opt(ext)
	}

	if err := cfg.Client().Resources().Create(ctx, ext); err != nil {
		t.Fatalf("failed to create MCPGatewayExtension %s/%s: %v", namespace, name, err)
	}
	t.Logf("created MCPGatewayExtension %s/%s (gateway=%s/%s)", namespace, name, gatewayNamespace, gatewayName)

	t.Cleanup(func() {
		cleanupExt := &kuadrantapi.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}
		r := cfg.Client().Resources()
		if err := r.Get(context.Background(), name, namespace, cleanupExt); err != nil {
			return
		}
		if len(cleanupExt.Finalizers) > 0 {
			cleanupExt.Finalizers = nil
			_ = r.Update(context.Background(), cleanupExt)
		}
		_ = r.Delete(context.Background(), cleanupExt)
	})

	return ext
}

// WaitForExtensionReady polls until the MCPGatewayExtension has Ready=True,
// which is set by the mcp-gateway controller.
func WaitForExtensionReady(ctx context.Context, t *testing.T, cfg *envconf.Config, name, namespace string) {
	t.Helper()
	r := cfg.Client().Resources()
	deadline := time.Now().Add(120 * time.Second)
	for {
		ext := &kuadrantapi.MCPGatewayExtension{}
		if err := r.Get(ctx, name, namespace, ext); err != nil {
			t.Fatalf("failed to get MCPGatewayExtension %s/%s: %v", namespace, name, err)
		}
		for _, c := range ext.Status.Conditions {
			if c.Type == "Ready" && c.Status == metav1.ConditionTrue {
				t.Logf("MCPGatewayExtension %s/%s is ready", namespace, name)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for MCPGatewayExtension %s/%s to become ready", namespace, name)
		}
		time.Sleep(2 * time.Second)
	}
}

// BuildControllerConfigData builds the ConfigMap data that the controller reads
// based on the provider type. Kuadrant uses extension-based keys while httproute
// uses gateway-name/gateway-namespace.
func BuildControllerConfigData(prov ProviderConfig, sectionName string) map[string]string {
	switch prov.Name {
	case providerKuadrant:
		data := map[string]string{
			configKeyExtensionName: prov.ConfigData[configKeyExtensionName],
			configKeyExtensionNS:   prov.ConfigData[configKeyExtensionNS],
			configKeyPrefix:        prov.ConfigData[configKeyPrefix],
		}
		if sectionName != "" {
			data["section-name"] = sectionName
		}
		if rh, ok := prov.ConfigData[configKeyRouteHostname]; ok {
			data[configKeyRouteHostname] = rh
		}
		return data
	default:
		data := map[string]string{
			configKeyGatewayName:      prov.ConfigData[configKeyGatewayName],
			configKeyGatewayNamespace: prov.ConfigData[configKeyGatewayNamespace],
		}
		if sectionName != "" {
			data["section-name"] = sectionName
		}
		if rh, ok := prov.ConfigData[configKeyRouteHostname]; ok {
			data[configKeyRouteHostname] = rh
		}
		if ph, ok := prov.ConfigData[configKeyPublicHostname]; ok {
			data[configKeyPublicHostname] = ph
		}
		return data
	}
}

// ListenerSpec describes a Gateway listener for EnsureMultiListenerGateway.
type ListenerSpec struct {
	Name     string
	Port     int32
	Protocol gatewayv1.ProtocolType
	Hostname *gatewayv1.Hostname
}

// EnsureMultiListenerGateway creates a Gateway with multiple listeners if it
// doesn't already exist. Returns the gateway's LoadBalancer address.
func EnsureMultiListenerGateway(ctx context.Context, t *testing.T, cfg *envconf.Config,
	name, namespace, gatewayClassName string, listeners []ListenerSpec) string {
	t.Helper()
	r := cfg.Client().Resources()

	fromAll := gatewayv1.NamespacesFromAll
	gwListeners := make([]gatewayv1.Listener, 0, len(listeners))
	for _, l := range listeners {
		listener := gatewayv1.Listener{
			Name:     gatewayv1.SectionName(l.Name),
			Protocol: l.Protocol,
			Port:     l.Port,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: &fromAll,
				},
			},
		}
		if l.Hostname != nil {
			listener.Hostname = l.Hostname
		}
		gwListeners = append(gwListeners, listener)
	}

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
			Listeners:        gwListeners,
		},
	}
	if err := r.Create(ctx, gw); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			t.Fatalf("failed to create Gateway %s/%s: %v", namespace, name, err)
		}
		if err := r.Update(ctx, gw); err != nil {
			t.Fatalf("failed to update existing Gateway %s/%s: %v", namespace, name, err)
		}
	}

	t.Cleanup(func() {
		_ = r.Delete(context.Background(), gw)
	})

	gatewayAddress := WaitForGatewayAddress(ctx, t, r, name, namespace)

	t.Logf("ensured multi-listener Gateway %s/%s (class=%s, listeners=%d, address=%s)",
		namespace, name, gatewayClassName, len(listeners), gatewayAddress)
	return gatewayAddress
}

// AssertMCPReachable performs a full MCP handshake through the gateway,
// proving the server is reachable via the data plane. It connects to the
// gateway's LoadBalancer address with the route hostname as the Host header.
func AssertMCPReachable(ctx context.Context, t *testing.T, gatewayAddress, routeHostname, path string) {
	t.Helper()

	httpClient := WithHostOverride(&http.Client{}, routeHostname)

	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    "e2e-hostname-test-client",
			Version: "v0.0.1",
		},
		nil,
	)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   fmt.Sprintf("http://%s%s", gatewayAddress, path),
		HTTPClient: httpClient,
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	session, err := mcpClient.Connect(connectCtx, transport, nil)
	if err != nil {
		t.Fatalf("MCP handshake through gateway failed (address=%s, host=%s, path=%s): %v",
			gatewayAddress, routeHostname, path, err)
	}
	defer func() { _ = session.Close() }()

	initResult := session.InitializeResult()
	if initResult == nil {
		t.Fatal("InitializeResult is nil")
	}
	t.Logf("MCP handshake succeeded: server=%s version=%s (host=%s)",
		initResult.ServerInfo.Name, initResult.ServerInfo.Version, routeHostname)
}

// EnsureReferenceGrant creates a ReferenceGrant in gatewayNamespace that allows
// MCPGatewayExtensions (and HTTPRoutes) from fromNamespace to reference Gateway
// resources. The mcp-gateway controller requires this for cross-namespace
// extension references. A t.Cleanup is registered to delete the grant.
func EnsureReferenceGrant(ctx context.Context, t *testing.T, cfg *envconf.Config,
	fromNamespace, gatewayNamespace string) {
	t.Helper()
	r := cfg.Client().Resources()

	name := "allow-" + fromNamespace
	grant := &gatewayv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gatewayNamespace,
		},
		Spec: gatewayv1.ReferenceGrantSpec{
			From: []gatewayv1.ReferenceGrantFrom{
				{
					Group:     gatewayv1.GroupName,
					Kind:      "HTTPRoute",
					Namespace: gatewayv1.Namespace(fromNamespace),
				},
				{
					Group:     "mcp.kuadrant.io",
					Kind:      "MCPGatewayExtension",
					Namespace: gatewayv1.Namespace(fromNamespace),
				},
			},
			To: []gatewayv1.ReferenceGrantTo{
				{
					Group: "",
					Kind:  "Service",
				},
				{
					Group: "gateway.networking.k8s.io",
					Kind:  "Gateway",
				},
			},
		},
	}
	if err := r.Create(ctx, grant); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create ReferenceGrant %s/%s: %v", gatewayNamespace, name, err)
	}
	t.Logf("created ReferenceGrant %s/%s (from=%s)", gatewayNamespace, name, fromNamespace)

	t.Cleanup(func() {
		_ = r.Delete(context.Background(), grant)
	})
}

func init() {
	for name, p := range providers {
		if p.Name != name {
			panic(fmt.Sprintf("provider config key %q does not match Name %q", name, p.Name))
		}
	}
}
