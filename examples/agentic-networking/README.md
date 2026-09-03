# Agentic Networking Example

This example demonstrates deploying an MCP server with the MCP Lifecycle
Operator and exposing it through
[kube-agentic-networking](https://github.com/kubernetes-sigs/kube-agentic-networking),
a Kubernetes SIG project that provides Gateway API-based routing for agent/MCP
traffic.

## What this demonstrates

- **MCP Lifecycle Operator** owns the lifecycle of the MCP server: it reconciles
  an `MCPServer` resource into a Deployment and a Service (`everything-mcp-server`,
  exposing port 3001). The MCP application behind that Service serves MCP
  traffic at the `/mcp` path.
- **kube-agentic-networking** routes directly to that Service: an `HTTPRoute`
  backendRef points at the Service by name/port, the `HTTPRoute` attaches to a
  `Gateway`, and the kube-agentic-networking controller programs an Envoy
  proxy accordingly.

```text
MCPServer --(operator creates)--> Service (everything-mcp-server:3001/mcp)
                                        ^
                                        | backendRefs
                                    HTTPRoute (everything-mcp-route)
                                        ^
                                        | parentRefs
                                    Gateway (everything-mcp-gateway)
```

This example intentionally stops at routing, using a direct Service
`backendRef` rather than kube-agentic-networking's `XBackend` resource — see
[Optional: tool-level authorization](#optional-tool-level-authorization) below
for why that matters if you want to add policy later.

## Prerequisites

- A Kubernetes cluster.
- **MCP Lifecycle Operator** CRDs installed and the controller running (`make install`,
  `make run` — see the [main README](../../README.md)).
- **kube-agentic-networking** installed according to its current official
  quickstart / installation instructions:
  <https://github.com/kubernetes-sigs/kube-agentic-networking>
  (hosted quickstart: <https://kube-agentic-networking.sigs.k8s.io/guides/quickstart/>).
  Installing it provisions the `kube-agentic-networking` `GatewayClass` used by
  this example, along with the CRDs and controller it depends on. Follow the
  upstream instructions in full rather than installing pieces individually —
  the setup does more than install CRDs and a Deployment.

## Deployment

1. Deploy the MCP server:

   ```bash
   kubectl apply -f examples/agentic-networking/mcpserver.yaml
   ```

2. Wait for it to become ready:

   ```bash
   kubectl wait --for=condition=Ready mcpserver/everything-mcp-server -n default --timeout=2m
   kubectl get mcpserver everything-mcp-server -n default
   ```

3. Deploy the Gateway API / kube-agentic-networking resources:

   ```bash
   kubectl apply -f examples/agentic-networking/networking.yaml
   ```

## Verification

### MCP Lifecycle Operator side

```bash
# MCPServer should show Ready=True and Accepted=True
kubectl get mcpserver everything-mcp-server -n default

# The operator-managed Service (created automatically for the MCPServer)
kubectl get svc everything-mcp-server -n default
```

### kube-agentic-networking side

This example routes directly to the Service, so there is no intermediate
backend resource to inspect here — use the Gateway and HTTPRoute status
conditions below to verify routing reconciliation.

```bash
kubectl get gateway everything-mcp-gateway -n default -o yaml
# or, just the conditions and assigned address:
kubectl get gateway everything-mcp-gateway -n default \
  -o jsonpath='{.status.conditions}{"\n"}{.status.addresses}{"\n"}'
```

Look for `Programmed=True` and `Accepted=True` in `status.conditions`, and at
least one entry under `status.addresses`.

```bash
kubectl get httproute everything-mcp-route -n default -o yaml
```

Look at `status.parents[].conditions` for `Accepted=True` and
`ResolvedRefs=True`.

## Traffic verification through the Gateway

This example does not include a copy/paste `curl` command for sending an actual
MCP request through the Gateway. The current kube-agentic-networking quickstart
puts the Gateway listener behind kube-agentic-networking's built-in SPIFFE-based
mTLS identity system, which requires client certificates issued through
Kubernetes certificate APIs (`PodCertificateRequest`, `ClusterTrustBundle`)
that are Beta and disabled by default — not something a generic cluster has
enabled out of the box. The exact feature gates and `--runtime-config` flags
needed can shift between Kubernetes releases, so rather than duplicate them
here, follow kube-agentic-networking's own versioned quickstart below, which
sets up a cluster with the current requirements already applied.

To exercise real traffic through the Gateway (including issuing an identity for
your test client), follow kube-agentic-networking's own quickstart:
<https://kube-agentic-networking.sigs.k8s.io/guides/quickstart/>. Its "Bring
Your Own Agent" section documents how to point an existing workload at a
Gateway once that identity infrastructure is set up.

### Troubleshooting the MCP backend directly (bypasses the Gateway)

This checks that the MCP server itself is healthy — it does **not** exercise
kube-agentic-networking's routing or authorization path:

```bash
kubectl port-forward svc/everything-mcp-server -n default 3001:3001
```

Then connect with an MCP client at `http://localhost:3001/mcp`.

## Optional: tool-level authorization

kube-agentic-networking also supports fine-grained, MCP method-level
authorization through the `XAccessPolicy` resource — for example, allowing a
given `ServiceAccount` to call only specific tools (`tools/call` with specific
tool-name params). This currently requires routing through an `XBackend`
rather than a direct Service `backendRef`: `XAccessPolicy` can only target an
`XBackend` or a `Gateway`, not a plain Service. This example does not include
one. See:

- The `XAccessPolicy` API:
  <https://github.com/kubernetes-sigs/kube-agentic-networking/blob/main/api/v1alpha1/accesspolicy_types.go>
- A worked example targeting an `XBackend`:
  <https://github.com/kubernetes-sigs/kube-agentic-networking/blob/main/site-src/guides/quickstart/policy/e2e.yaml>

## Cleanup

```bash
kubectl delete -f examples/agentic-networking/networking.yaml
kubectl delete -f examples/agentic-networking/mcpserver.yaml
```
