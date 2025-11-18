# EP-XXXXX: HTTP ExtAuth Service Support


DLC XXXXX is?

DLC oauth2-proxy for OIDC

* Issue: [#XXXXX](https://github.com/kgateway-dev/kgateway/issues/XXXXX)

## Background

kgateway currently supports external authorization (extauth) for HTTP traffic through the `GatewayExtension` API with `ExtAuth` type. However, the current implementation only supports communication with the authorization server via gRPC protocol, as defined by the `ExtAuthProvider.GrpcService` field in `api/v1alpha1/ext_auth_types.go`.

This limitation means that authorization services must implement the [Envoy ext_authz gRPC protocol](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/auth/v3/external_auth.proto), even though Envoy's ext_authz filter also supports a simpler HTTP-based protocol for communication with authorization services.

The HTTP protocol for ext_authz is widely used in the industry and offers several advantages:
- Simpler to implement - authorization services can be standard HTTP servers
- Lower operational complexity - no need for gRPC infrastructure
- Broader ecosystem compatibility - many existing auth services expose HTTP endpoints
- Easier debugging and testing - standard HTTP tools can be used

Popular authorization services that support or prefer HTTP-based protocols include:
- OAuth2 Proxy
- Ory Oathkeeper
- Custom webhook-style authorization services
- Many cloud provider authorization endpoints

This enhancement proposes adding HTTP service support alongside the existing gRPC support, giving users flexibility in choosing their authorization backend protocol.

## Motivation

### Goals

- Enable users to configure HTTP-based external authorization services in addition to gRPC services
- Support the key Envoy ext_authz HTTP configuration options:
  - Path prefix for authorization endpoint
  - Timeout configuration
  - Authorization request/response header manipulation
  - Allowed client/upstream headers
- Maintain backward compatibility with existing gRPC-based configurations
- Provide sensible defaults for HTTP service configuration to minimize configuration complexity
- Follow the same attachment and policy patterns as existing gRPC extauth

### Non-Goals

- Deprecating or removing gRPC extauth support
- Supporting both HTTP and gRPC services simultaneously in a single `GatewayExtension` resource
- Implementing automatic protocol detection or fallback between HTTP and gRPC
- Supporting network-level (TCP) ext_authz filters
- Providing a reference implementation of an HTTP authorization server
- Supporting advanced HTTP features like request body passthrough in the initial implementation (can be added later if needed)

## Implementation Details

### API Changes

Add a new `ExtHttpService` type to support HTTP-based authorization services:

```go
// ExtHttpService defines the HTTP service that will handle the processing.
type ExtHttpService struct {
	// BackendRef references the backend HTTP service.
	// +required
	BackendRef gwv1.BackendRef `json:"backendRef"`

	// PathPrefix specifies a prefix to the value of the authorization request's path header.
	// This allows customizing the path at which the authorization server expects to receive requests.
	// For example, if the authorization server expects requests at "/verify", set this to "/verify".
	// If not specified, defaults to "/".
	// +optional
	// +kubebuilder:default="/"
	PathPrefix *string `json:"pathPrefix,omitempty"`

	// RequestTimeout is the timeout for the HTTP request. This is the timeout for a specific request.
	// If not specified, defaults to 200ms.
	// +optional
	// +kubebuilder:validation:XValidation:rule="matches(self, '^([0-9]{1,5}(h|m|s|ms)){1,4}$')",message="invalid timeout value"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1ms')",message="timeout must be at least 1ms."
	// +kubebuilder:default="200ms"
	RequestTimeout *metav1.Duration `json:"requestTimeout,omitempty"`

	// AuthorizationRequest configures the authorization request sent to the HTTP service.
	// +optional
	AuthorizationRequest *AuthorizationRequest `json:"authorizationRequest,omitempty"`

	// AuthorizationResponse configures how to handle the authorization response from the HTTP service.
	// +optional
	AuthorizationResponse *AuthorizationResponse `json:"authorizationResponse,omitempty"`
}

// AuthorizationRequest configures the HTTP authorization request.
type AuthorizationRequest struct {
	// AllowedHeaders specifies which client request headers should be sent to the authorization server.
	// By default, the following headers are sent: Host, Method, Path, Content-Length, and Authorization.
	// Use this to add additional headers that the authorization server needs for decision-making.
	// If not set, only the default headers are sent.
	// Note: pseudo-headers like :method, :path, :authority are automatically included and don't need to be specified.
	// +optional
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`

	// HeadersToAdd specifies additional headers to add to the authorization request.
	// These headers will be added to every request sent to the authorization server.
	// Useful for adding static metadata like API keys or version information.
	// +optional
	// +listType=map
	// +listMapKey=name
	HeadersToAdd []HeaderValue `json:"headersToAdd,omitempty"`
}

// AuthorizationResponse configures how to handle the HTTP authorization response.
type AuthorizationResponse struct {
	// AllowedUpstreamHeaders specifies which authorization response headers should be added to the
	// upstream request when authorization succeeds. This allows the authorization service to pass
	// additional context (like user ID, roles, etc.) to the upstream service.
	// If not set, no authorization response headers are added to the upstream request.
	// +optional
	AllowedUpstreamHeaders []string `json:"allowedUpstreamHeaders,omitempty"`

	// AllowedClientHeaders specifies which authorization response headers should be added to the
	// client response when authorization fails. This allows the authorization service to provide
	// additional context in error responses (like WWW-Authenticate challenges).
	// If not set, no authorization response headers are added to the client response.
	// +optional
	AllowedClientHeaders []string `json:"allowedClientHeaders,omitempty"`

	// DynamicMetadataFromHeaders specifies headers from the authorization response that should be
	// emitted as dynamic metadata to be consumed by the next filter. The header names will be used
	// as the metadata keys. The metadata lives in the "envoy.filters.http.ext_authz" namespace.
	// +optional
	DynamicMetadataFromHeaders []string `json:"dynamicMetadataFromHeaders,omitempty"`
}

// HeaderValue represents a header name and value pair.
type HeaderValue struct {
	// Name is the header name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Value is the header value.
	// +required
	Value string `json:"value"`
}
```

Update `ExtAuthProvider` to support either HTTP or gRPC services:

```go
// ExtAuthProvider defines the configuration for an ExtAuth provider.
// +kubebuilder:validation:XValidation:message="Either grpcService or httpService must be set, but not both",rule="(has(self.grpcService) && !has(self.httpService)) || (!has(self.grpcService) && has(self.httpService))"
type ExtAuthProvider struct {
	// GrpcService is the GRPC service that will handle the auth.
	// Mutually exclusive with HttpService.
	// +optional
	GrpcService *ExtGrpcService `json:"grpcService,omitempty"`

	// HttpService is the HTTP service that will handle the auth.
	// Mutually exclusive with GrpcService.
	// +optional
	HttpService *ExtHttpService `json:"httpService,omitempty"`

	// FailOpen determines if requests are allowed when the ext auth service is unavailable.
	// Defaults to false, meaning requests will be denied if the ext auth service is unavailable.
	// +optional
	// +kubebuilder:default=false
	FailOpen bool `json:"failOpen,omitempty"`

	// ClearRouteCache determines if the route cache should be cleared to allow the
	// external authentication service to correctly affect routing decisions.
	// +optional
	// +kubebuilder:default=false
	ClearRouteCache bool `json:"clearRouteCache,omitempty"`

	// WithRequestBody allows the request body to be buffered and sent to the auth service.
	// Warning: buffering has implications for streaming and therefore performance.
	// Note: This is primarily useful for HTTP services. For gRPC services, body handling
	// is typically done through the gRPC protocol.
	// +optional
	WithRequestBody *ExtAuthBufferSettings `json:"withRequestBody,omitempty"`

	// StatusOnError sets the HTTP status response code that is returned to the client when the
	// auth server returns an error or cannot be reached. Must be in the range of 100-511 inclusive.
	// The default matches the deny response code of 403 Forbidden.
	// +optional
	// +kubebuilder:default=403
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=511
	StatusOnError int32 `json:"statusOnError,omitempty"`

	// StatPrefix is an optional prefix to include when emitting stats from the extauthz filter,
	// enabling different instances of the filter to have unique stats.
	// +optional
	// +kubebuilder:validation:MinLength=1
	StatPrefix *string `json:"statPrefix,omitempty"`
}
```

### Configuration Examples

#### Basic HTTP ExtAuth Configuration

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: http-extauth
  namespace: default
spec:
  type: ExtAuth
  extAuth:
    httpService:
      backendRef:
        name: auth-service
        port: 8080
      pathPrefix: /verify
```

#### HTTP ExtAuth with Custom Headers

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: http-extauth-custom
  namespace: default
spec:
  type: ExtAuth
  extAuth:
    httpService:
      backendRef:
        name: auth-service
        port: 8080
      pathPrefix: /api/v1/authorize
      requestTimeout: 500ms
      authorizationRequest:
        # Allow the auth service to see these request headers
        allowedHeaders:
          - x-tenant-id
          - x-api-version
        # Add static headers to every auth request
        headersToAdd:
          x-auth-version: "1.0"
          x-environment: production
      authorizationResponse:
        # Forward these headers from auth response to upstream
        allowedUpstreamHeaders:
          - x-user-id
          - x-user-roles
          - x-tenant-id
        # Include these headers in client error responses
        allowedClientHeaders:
          - www-authenticate
          - x-error-code
        # Emit these headers as dynamic metadata for other filters
        # The header names will be used as the metadata keys
        dynamicMetadataFromHeaders:
          - x-user-id
          - x-user-roles
    failOpen: false
    statusOnError: 403
```

#### Applying HTTP ExtAuth to a Gateway

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: TrafficPolicy
metadata:
  name: gateway-auth
  namespace: default
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: my-gateway
  extAuth:
    extensionRef:
      name: http-extauth
```

#### Mixed Environment (HTTP and gRPC ExtAuth)

Users can have different `GatewayExtension` resources using different protocols:

```yaml
---
# HTTP-based auth for legacy services
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: legacy-http-auth
spec:
  type: ExtAuth
  extAuth:
    httpService:
      backendRef:
        name: legacy-auth-service
        port: 80
---
# gRPC-based auth for modern services
apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayExtension
metadata:
  name: modern-grpc-auth
spec:
  type: ExtAuth
  extAuth:
    grpcService:
      backendRef:
        name: modern-auth-service
        port: 50051
---
# Apply different auth to different routes
apiVersion: gateway.kgateway.dev/v1alpha1
kind: TrafficPolicy
metadata:
  name: legacy-route-auth
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: legacy-api
  extAuth:
    extensionRef:
      name: legacy-http-auth
---
apiVersion: gateway.kgateway.dev/v1alpha1
kind: TrafficPolicy
metadata:
  name: modern-route-auth
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: modern-api
  extAuth:
    extensionRef:
      name: modern-grpc-auth
```

### Plugin Implementation

The extauth plugin (in `internal/kgateway/extensions2/plugins/trafficpolicy/extauth_policy.go`) will need to be updated to:

1. Detect whether `grpcService` or `httpService` is configured in the `ExtAuthProvider`
2. Generate the appropriate Envoy xDS configuration based on the service type:
   - For gRPC: Generate `envoy.extensions.filters.http.ext_authz.v3.ExtAuthz` with `grpc_service` configuration
   - For HTTP: Generate `envoy.extensions.filters.http.ext_authz.v3.ExtAuthz` with `http_service` configuration
3. Apply the service-specific configuration options:
   - HTTP: path prefix, allowed headers, headers to add/remove
   - gRPC: authority, retry policy
4. Handle common configuration options that apply to both (failOpen, statusOnError, etc.)

### Translation Logic

The translator will construct the Envoy ext_authz filter configuration as follows:

**For HTTP Services:**
```go
&extauthv3.ExtAuthz{
    Services: &extauthv3.ExtAuthz_HttpService{
        HttpService: &extauthv3.HttpService{
            ServerUri: &corev3.HttpUri{
                Uri: fmt.Sprintf("http://%s:%d", serviceName, port),
                HttpUpstreamType: &corev3.HttpUri_Cluster{
                    Cluster: clusterName,
                },
                Timeout: durationpb.New(timeout),
            },
            PathPrefix: pathPrefix,
            AuthorizationRequest: &extauthv3.AuthorizationRequest{
                AllowedHeaders: &matcherv3.ListStringMatcher{
                    Patterns: allowedHeaders,
                },
                HeadersToAdd: headersToAdd,
            },
            AuthorizationResponse: &extauthv3.AuthorizationResponse{
                AllowedUpstreamHeaders: &matcherv3.ListStringMatcher{
                    Patterns: allowedUpstreamHeaders,
                },
                AllowedClientHeaders: &matcherv3.ListStringMatcher{
                    Patterns: allowedClientHeaders,
                },
                DynamicMetadataFromHeaders: dynamicMetadataFromHeaders,
            },
        },
    },
    FailureModeAllow: failOpen,
    StatusOnError: &typev3.HttpStatus{
        Code: statusOnError,
    },
    // ... other common fields
}
```

### Validation

The following validations will be enforced via CEL rules and plugin logic:

1. **Mutual exclusivity**: Either `grpcService` or `httpService` must be set, but not both
2. **Path prefix validation**: Must start with "/" if specified
3. **Timeout validation**: Must be >= 1ms
4. **Status code validation**: Must be in range 100-511
5. **Header name validation**: Must be valid HTTP header names (lowercase, alphanumeric, hyphens)
6. **Backend reference validation**: Referenced service must exist

### Reporting

Status reporting will follow the existing pattern for `GatewayExtension` resources:

- `Accepted=True` when the configuration is valid and the backend reference resolves
- `Accepted=False` with appropriate reason when:
  - Both or neither grpcService/httpService are specified
  - Backend reference is invalid or not found
  - Configuration validation fails

### Observability

The existing Envoy ext_authz metrics will work for both HTTP and gRPC services:

- `ext_authz.ok` - Successful authorization checks
- `ext_authz.denied` - Denied authorization checks
- `ext_authz.error` - Authorization service errors
- `ext_authz.disabled` - Checks skipped when disabled
- `ext_authz.failure_mode_allowed` - Requests allowed due to failOpen

Additional metrics can be distinguished by the configured `statPrefix` field.

### Test Plan

#### Unit Tests

- Test ExtHttpService API validation (CEL rules)
- Test mutual exclusivity of grpcService and httpService
- Test default value application (pathPrefix, timeout, statusOnError)
- Test header list validation
- Test translation logic for HTTP service configuration
- Test that common fields work with both HTTP and gRPC services

#### Integration Tests

- Test that HTTP service configuration generates correct Envoy xDS
- Test that existing gRPC service configuration remains unchanged
- Test mixed configurations (some GatewayExtensions using HTTP, others using gRPC)
- Test header manipulation (allowed headers, headers to add, dynamic metadata)

#### E2E Tests

Extend the existing extauth e2e test suite (`test/e2e/features/extauth/` and `test/e2e/features/agentgateway/extauth/`) to include:

1. **Basic HTTP extauth test**:
   - Deploy a simple HTTP authorization service (e.g., using a test container that responds 200 with specific headers or 403 without)
   - Configure HTTP extauth with basic settings
   - Verify authorized requests succeed
   - Verify unauthorized requests fail with correct status code

2. **HTTP header manipulation test**:
   - Configure allowedHeaders, headersToAdd
   - Verify headers are correctly passed to auth service
   - Configure allowedUpstreamHeaders
   - Verify headers from auth response reach upstream service

3. **HTTP failOpen test**:
   - Configure HTTP extauth with failOpen=true
   - Stop the auth service
   - Verify requests are allowed through
   - Configure failOpen=false
   - Verify requests are denied

4. **HTTP and gRPC coexistence test**:
   - Configure one route with HTTP extauth
   - Configure another route with gRPC extauth
   - Verify both work independently

5. **HTTP path prefix test**:
   - Configure different pathPrefix values
   - Verify auth service receives requests at correct paths

6. **HTTP timeout test**:
   - Configure short timeout
   - Use auth service with artificial delay
   - Verify timeout behavior and statusOnError

## Alternatives

### Alternative 1: Single Service Type with Protocol Field

Instead of having separate `grpcService` and `httpService` fields, use a single service definition with a protocol discriminator:

```yaml
extAuth:
  service:
    protocol: http  # or grpc
    backendRef:
      name: auth-service
      port: 8080
    # protocol-specific fields
```

**Pros:**
- Simpler top-level structure
- Easier to understand that only one service can be configured

**Cons:**
- Harder to enforce protocol-specific required fields via CEL
- More complex validation logic needed
- Less clear API - some fields only valid for certain protocols
- Doesn't follow the established pattern in kgateway

**Decision:** Use separate service types for type safety and clarity.

### Alternative 2: Separate GatewayExtension Types

Create a new `GatewayExtensionType` for HTTP-based extauth:

```yaml
spec:
  type: ExtAuthHttp  # new type
  extAuthHttp:
    httpService:
      # ...
```

**Pros:**
- Complete separation of HTTP and gRPC extauth
- Very explicit about protocol choice

**Cons:**
- Creates unnecessary proliferation of extension types
- Violates single responsibility - both are still extauth
- More complex for users to understand the relationship
- Would require duplicate configuration for all common fields

**Decision:** Keep single ExtAuth type with protocol choice via service field.

### Alternative 3: Automatic Protocol Detection

Automatically detect protocol based on backend service annotations or port conventions:

**Pros:**
- Less configuration required
- Potentially simpler user experience

**Cons:**
- Magic behavior that's hard to debug
- Fragile - relies on external conventions
- Doesn't work well with services that support both protocols
- Can't configure protocol-specific options explicitly
- Users lose control over the protocol choice

**Decision:** Require explicit protocol selection for clarity and control.

## Design Considerations

1. **Backward Compatibility**: The change is fully backward compatible. Existing `GatewayExtension` resources with `grpcService` continue to work unchanged.

2. **Future Extensibility**: The design allows for adding more service types in the future if needed (though HTTP and gRPC should cover the vast majority of use cases).

3. **Consistency with Gateway API**: The design follows Kubernetes Gateway API patterns of using discriminated unions with CEL validation.

4. **Envoy Alignment**: The API closely mirrors Envoy's ext_authz HTTP service configuration, making it easier for users familiar with Envoy to adopt.

5. **Progressive Disclosure**: Basic use cases require minimal configuration (just backendRef), while advanced use cases can leverage detailed header manipulation options.

## Open Questions

- [ ] Should we support request body buffering for HTTP services in the initial implementation, or add it in a follow-up? (Recommendation: Add in follow-up if there's demand)

- [ ] Should we provide helm chart examples or reference implementations of simple HTTP authorization services for testing/demo purposes?

- [ ] Should the default timeout for HTTP services be different from gRPC services? Current proposal uses 200ms for both, but HTTP might benefit from a slightly higher default due to connection overhead.

- [ ] Should we support custom HTTP methods for authorization requests (currently Envoy sends POST by default)? This could be added via a `method` field in `AuthorizationRequest` if needed.

- [ ] Do we need to support TLS configuration for HTTP services, or rely on service mesh / backend configuration? (Recommendation: Defer to backend/mesh configuration for simplicity)
