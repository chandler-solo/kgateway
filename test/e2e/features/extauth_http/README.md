# HTTP ExtAuth E2E Test Suite

This test suite validates HTTP-based external authorization functionality in kgateway.

## Overview

The HTTP ExtAuth test suite demonstrates and validates the HTTP protocol for external authorization, which provides a simpler alternative to gRPC-based authorization services. This is particularly useful for:

- OAuth2 Proxy and similar HTTP-based authorization services
- Custom webhook-style authorization endpoints
- Services that don't require gRPC infrastructure
- Simpler debugging and testing with standard HTTP tools

## Test Architecture

```
┌──────────┐      ┌─────────┐      ┌────────────────┐      ┌─────────┐
│  Client  │─────▶│ Gateway │─────▶│ HTTP Auth Svr  │      │ httpbin │
│  (curl)  │      │ (envoy) │      │ (Python)       │      │ (backend│
└──────────┘      └─────────┘      └────────────────┘      └─────────┘
                       │                   │
                       │   POST /verify    │
                       │ + x-auth-token    │
                       │──────────────────▶│
                       │                   │
                       │  200 OK (allow)   │
                       │  or 403 (deny)    │
                       │◀──────────────────│
                       │                   │
                       │ Forward request   │
                       │──────────────────────────────────▶│
```

## Test Components

### 1. HTTP Authorization Server (`http-auth-server.yaml`)

A simple Python-based HTTP authorization service that:
- Listens on port 8080
- Accepts POST requests at `/verify`
- Returns HTTP 200 OK if the `x-auth-token: allow` header is present
- Returns HTTP 403 Forbidden otherwise
- Adds headers (`x-auth-user`, `x-auth-roles`) to successful responses that can be forwarded to upstream

### 2. GatewayExtension (`http-extauth-extension.yaml`)

Configures HTTP external authorization with:
- Backend reference to the HTTP auth server
- Path prefix: `/verify`
- Request timeout: 500ms
- Upstream header forwarding: `x-auth-user`, `x-auth-roles`
- Client error headers: `x-auth-result`
- Fail-closed behavior (deny on error)
- Custom error status: 403

### 3. Traffic Policies

- **Gateway-level policy** (`gateway-policy.yaml`): Applies HTTP ExtAuth to all requests through the gateway
- **Route-level disable policy** (`route-disable-policy.yaml`): Demonstrates how to disable ExtAuth for specific routes

## Test Scenarios

### TestHttpExtAuthBasic

Validates basic HTTP ExtAuth functionality:
1. **Allowed requests**: Requests with `x-auth-token: allow` header succeed
2. **Denied requests**: Requests without the auth header are rejected with 403
3. **Invalid auth**: Requests with wrong auth token are rejected with 403

### TestHttpExtAuthWithUpstreamHeaders

Validates header forwarding from auth response to upstream:
1. Sends authorized request to `/headers` endpoint
2. Verifies that `x-auth-user: test-user` is forwarded to httpbin
3. Verifies that `x-auth-roles: admin,user` is forwarded to httpbin

### TestHttpExtAuthRouteDisable

Validates route-level policy override:
1. Applies gateway-level ExtAuth (deny by default)
2. Applies route-level disable policy
3. Verifies requests succeed without auth header (route policy takes precedence)

## Running the Tests

### Run all HTTP ExtAuth tests:
```bash
make test-e2e E2E_TEST_FILTER='ExtAuthHTTP'
```

### Run a specific test:
```bash
make test-e2e E2E_TEST_FILTER='ExtAuthHTTP/TestHttpExtAuthBasic'
```

## Comparison with gRPC ExtAuth

| Aspect | HTTP ExtAuth | gRPC ExtAuth |
|--------|--------------|--------------|
| **Protocol** | HTTP POST | gRPC streaming |
| **Setup complexity** | Simple HTTP server | Requires gRPC proto implementation |
| **Debugging** | Easy with curl/HTTP tools | Requires gRPC tooling |
| **Auth decision** | HTTP status codes (200=allow, 403=deny) | gRPC response with CheckResponse |
| **Header forwarding** | HTTP headers | gRPC metadata |
| **Use cases** | OAuth2 Proxy, webhooks, simple auth | Custom gRPC auth services |

## Key Configuration Options

### ExtHttpService

```yaml
httpService:
  backendRef:
    name: http-auth-server
    port: 8080
  pathPrefix: /verify              # Path where auth server expects requests
  requestTimeout: 500ms             # Timeout for auth requests
  authorizationRequest:
    allowedHeaders:                 # Additional request headers to forward
      - x-tenant-id
    headersToAdd:                   # Static headers to add
      x-environment: production
  authorizationResponse:
    allowedUpstreamHeaders:         # Headers to forward to upstream
      - x-auth-user
      - x-auth-roles
    allowedClientHeaders:           # Headers to include in error responses
      - www-authenticate
    dynamicMetadataFromHeaders:     # Headers to emit as metadata
      - x-user-id
```

## Real-World Integration Examples

### OAuth2 Proxy

For production use with OAuth2 Proxy:

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: oauth2-ext-auth
spec:
  type: ExtAuth
  extAuth:
    httpService:
      backendRef:
        name: oauth2-proxy
        port: 4180
      pathPrefix: /oauth2/auth
      authorizationResponse:
        allowedUpstreamHeaders:
          - x-auth-request-user
          - x-auth-request-email
        allowedClientHeaders:
          - www-authenticate
          - set-cookie
```

### Custom Webhook

For a custom authorization webhook:

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: custom-webhook-auth
spec:
  type: ExtAuth
  extAuth:
    httpService:
      backendRef:
        name: auth-webhook
        port: 443
      pathPrefix: /api/v1/authorize
      requestTimeout: 1s
      authorizationRequest:
        headersToAdd:
          x-api-key: "your-api-key"
```

## Troubleshooting

### Auth requests timing out
- Increase `requestTimeout` in the ExtHttpService configuration
- Check that the auth server is responding within the timeout period

### Headers not being forwarded
- Ensure headers are listed in `allowedUpstreamHeaders` or `allowedClientHeaders`
- Verify the auth server is actually sending the headers in its response

### All requests being denied
- Check the auth server logs to see what it's receiving
- Verify the `pathPrefix` matches where your auth server expects requests
- Test the auth server directly with curl to verify it's working

## Development Notes

- The test HTTP auth server is intentionally simple for easy testing
- For production, use proper authorization services (OAuth2 Proxy, Ory Oathkeeper, etc.)
- The test suite focuses on the HTTP protocol aspects, not complex auth logic
- Header forwarding is a key feature to test as it enables passing user context to upstreams
