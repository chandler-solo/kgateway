package xdscheck

import (
	"context"
	"fmt"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"

	CodeDuplicateResourceName             = "duplicate_resource_name"
	CodeMissingRouteConfiguration         = "missing_route_configuration"
	CodeMissingCluster                    = "missing_cluster"
	CodeMissingClusterLoadAssignment      = "missing_cluster_load_assignment"
	CodeUnsupportedHCMTypedConfig         = "unsupported_hcm_typed_config"
	CodeUnsupportedHCMConfigType          = "unsupported_hcm_config_type"
	CodeUnsupportedHCMRouteSpecifier      = "unsupported_hcm_route_specifier"
	CodeUnsupportedRouteClusterHeader     = "unsupported_route_cluster_header"
	CodeUnsupportedClusterSpecifierPlugin = "unsupported_cluster_specifier_plugin"
	CodeUnsupportedWeightedClusterHeader  = "unsupported_weighted_cluster_header"
	CodeUnsupportedInlineClusterSpecifier = "unsupported_inline_cluster_specifier"
	CodeCanceled                          = "check_canceled"
)

// Snapshot is the Envoy xDS resource set checked by this package.
type Snapshot struct {
	Listeners []*envoylistenerv3.Listener
	Routes    []*envoyroutev3.RouteConfiguration
	Clusters  []*envoyclusterv3.Cluster
	Endpoints []*envoyendpointv3.ClusterLoadAssignment
	Secrets   []*envoytlsv3.Secret
}

// Finding describes a precise invariant result for a concrete xDS snapshot.
type Finding struct {
	Severity string
	Code     string
	Resource string
	Message  string
}

// CheckSnapshot checks concrete LDS/RDS/CDS/EDS dependency invariants without
// invoking Envoy or changing production behavior.
func CheckSnapshot(ctx context.Context, s Snapshot) []Finding {
	if ctx == nil {
		ctx = context.Background()
	}

	c := checker{}
	c.routes = indexByName(s.Routes, "RouteConfiguration", func(r *envoyroutev3.RouteConfiguration) string {
		return r.GetName()
	}, &c.findings)
	c.clusters = indexByName(s.Clusters, "Cluster", func(c *envoyclusterv3.Cluster) string {
		return c.GetName()
	}, &c.findings)
	c.endpoints = indexByName(s.Endpoints, "ClusterLoadAssignment", func(e *envoyendpointv3.ClusterLoadAssignment) string {
		return e.GetClusterName()
	}, &c.findings)
	indexByName(s.Listeners, "Listener", func(l *envoylistenerv3.Listener) string {
		return l.GetName()
	}, &c.findings)
	indexByName(s.Secrets, "Secret", func(s *envoytlsv3.Secret) string {
		return s.GetName()
	}, &c.findings)

	if c.isCanceled(ctx) {
		return c.findings
	}
	for _, listener := range s.Listeners {
		c.checkListener(ctx, listener)
		if c.isCanceled(ctx) {
			return c.findings
		}
	}
	for _, route := range s.Routes {
		c.checkRouteConfiguration(ctx, route, routeResource(route.GetName()))
		if c.isCanceled(ctx) {
			return c.findings
		}
	}
	for _, cluster := range s.Clusters {
		c.checkEDSCluster(cluster)
	}

	return c.findings
}

// ErrorFindings returns only error-severity findings.
func ErrorFindings(findings []Finding) []Finding {
	var out []Finding
	for _, finding := range findings {
		if finding.Severity == SeverityError {
			out = append(out, finding)
		}
	}
	return out
}

type checker struct {
	findings  []Finding
	routes    map[string]*envoyroutev3.RouteConfiguration
	clusters  map[string]*envoyclusterv3.Cluster
	endpoints map[string]*envoyendpointv3.ClusterLoadAssignment
}

func (c *checker) checkListener(ctx context.Context, listener *envoylistenerv3.Listener) {
	for _, filterChain := range listener.GetFilterChains() {
		filterChainName := filterChain.GetName()
		if filterChainName == "" {
			filterChainName = "<unnamed>"
		}
		for _, filter := range filterChain.GetFilters() {
			if ctx.Err() != nil {
				return
			}
			if filter.GetName() != envoywellknown.HTTPConnectionManager {
				continue
			}

			resource := fmt.Sprintf("%s FilterChain/%s Filter/%s", listenerResource(listener.GetName()), filterChainName, filter.GetName())
			hcm, ok := c.unpackHCM(filter, resource)
			if !ok {
				continue
			}
			c.checkHCMRouteSpecifier(ctx, listener.GetName(), filterChainName, hcm)
		}
	}
}

func (c *checker) unpackHCM(filter *envoylistenerv3.Filter, resource string) (*envoyhcmv3.HttpConnectionManager, bool) {
	typedConfig := filter.GetTypedConfig()
	if typedConfig == nil {
		c.add(SeverityWarning, CodeUnsupportedHCMConfigType, resource,
			"HCM filter does not use typed_config; route references were not validated")
		return nil, false
	}

	hcm := &envoyhcmv3.HttpConnectionManager{}
	if err := typedConfig.UnmarshalTo(hcm); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHCMTypedConfig, resource,
			fmt.Sprintf("cannot unpack HCM typed_config %q; route references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return nil, false
	}
	return hcm, true
}

func (c *checker) checkHCMRouteSpecifier(ctx context.Context, listenerName, filterChainName string, hcm *envoyhcmv3.HttpConnectionManager) {
	resource := fmt.Sprintf("%s FilterChain/%s", listenerResource(listenerName), filterChainName)
	switch routeSpecifier := hcm.GetRouteSpecifier().(type) {
	case *envoyhcmv3.HttpConnectionManager_Rds:
		routeName := routeSpecifier.Rds.GetRouteConfigName()
		if _, ok := c.routes[routeName]; !ok {
			c.add(SeverityError, CodeMissingRouteConfiguration, resource,
				fmt.Sprintf("listener %q filter chain %q references missing RDS route configuration %q", listenerName, filterChainName, routeName))
		}
	case *envoyhcmv3.HttpConnectionManager_RouteConfig:
		c.checkRouteConfiguration(ctx, routeSpecifier.RouteConfig, fmt.Sprintf("%s InlineRouteConfiguration", resource))
	case *envoyhcmv3.HttpConnectionManager_ScopedRoutes:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			"scoped_routes route specifier is not validated by xdscheck")
	case nil:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			"HCM route specifier is empty; route references were not validated")
	default:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			fmt.Sprintf("HCM route specifier %T is not validated by xdscheck", routeSpecifier))
	}
}

func (c *checker) checkRouteConfiguration(ctx context.Context, routeConfig *envoyroutev3.RouteConfiguration, resourcePrefix string) {
	if routeConfig == nil {
		return
	}
	for _, virtualHost := range routeConfig.GetVirtualHosts() {
		if ctx.Err() != nil {
			return
		}
		vhostResource := fmt.Sprintf("%s VirtualHost/%s", resourcePrefix, virtualHost.GetName())
		for _, route := range virtualHost.GetRoutes() {
			routeName := route.GetName()
			if routeName == "" {
				routeName = "<unnamed>"
			}
			c.checkRouteAction(route.GetRoute(), fmt.Sprintf("%s Route/%s", vhostResource, routeName), routeConfig.GetName(), virtualHost.GetName(), routeName)
		}
	}
}

func (c *checker) checkRouteAction(routeAction *envoyroutev3.RouteAction, resource, routeConfigName, virtualHostName, routeName string) {
	if routeAction == nil {
		return
	}

	switch clusterSpecifier := routeAction.GetClusterSpecifier().(type) {
	case *envoyroutev3.RouteAction_Cluster:
		c.requireCluster(clusterSpecifier.Cluster, resource, routeConfigName, virtualHostName, routeName)
	case *envoyroutev3.RouteAction_WeightedClusters:
		for i, clusterWeight := range clusterSpecifier.WeightedClusters.GetClusters() {
			clusterResource := fmt.Sprintf("%s WeightedCluster/%d", resource, i)
			if clusterWeight.GetClusterHeader() != "" {
				c.add(SeverityWarning, CodeUnsupportedWeightedClusterHeader, clusterResource,
					fmt.Sprintf("weighted cluster entry uses cluster_header %q; static cluster existence cannot be verified", clusterWeight.GetClusterHeader()))
				continue
			}
			c.requireCluster(clusterWeight.GetName(), clusterResource, routeConfigName, virtualHostName, routeName)
		}
	case *envoyroutev3.RouteAction_ClusterHeader:
		c.add(SeverityWarning, CodeUnsupportedRouteClusterHeader, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses cluster_header %q; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName, clusterSpecifier.ClusterHeader))
	case *envoyroutev3.RouteAction_ClusterSpecifierPlugin:
		c.add(SeverityWarning, CodeUnsupportedClusterSpecifierPlugin, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses cluster_specifier_plugin %q; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName, clusterSpecifier.ClusterSpecifierPlugin))
	case *envoyroutev3.RouteAction_InlineClusterSpecifierPlugin:
		c.add(SeverityWarning, CodeUnsupportedInlineClusterSpecifier, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses inline cluster specifier plugin; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName))
	case nil:
		return
	default:
		c.add(SeverityWarning, CodeUnsupportedClusterSpecifierPlugin, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses unsupported cluster specifier %T", routeConfigName, virtualHostName, routeName, clusterSpecifier))
	}
}

func (c *checker) requireCluster(name, resource, routeConfigName, virtualHostName, routeName string) {
	if name == "" {
		return
	}
	if _, ok := c.clusters[name]; ok {
		return
	}
	c.add(SeverityError, CodeMissingCluster, resource,
		fmt.Sprintf("route configuration %q virtual host %q route %q references missing cluster %q", routeConfigName, virtualHostName, routeName, name))
}

func (c *checker) checkEDSCluster(cluster *envoyclusterv3.Cluster) {
	if cluster.GetType() != envoyclusterv3.Cluster_EDS {
		return
	}

	expectedName := cluster.GetEdsClusterConfig().GetServiceName()
	if expectedName == "" {
		expectedName = cluster.GetName()
	}
	if _, ok := c.endpoints[expectedName]; ok {
		return
	}
	c.add(SeverityError, CodeMissingClusterLoadAssignment, clusterResource(cluster.GetName()),
		fmt.Sprintf("cluster %q uses EDS resource %q but no matching ClusterLoadAssignment was emitted", cluster.GetName(), expectedName))
}

func (c *checker) isCanceled(ctx context.Context) bool {
	err := ctx.Err()
	if err == nil {
		return false
	}
	c.add(SeverityError, CodeCanceled, "Snapshot", fmt.Sprintf("xDS snapshot check canceled: %v", err))
	return true
}

func (c *checker) add(severity, code, resource, message string) {
	c.findings = append(c.findings, Finding{
		Severity: severity,
		Code:     code,
		Resource: resource,
		Message:  message,
	})
}

func indexByName[T any](items []T, typeName string, nameOf func(T) string, findings *[]Finding) map[string]T {
	out := make(map[string]T, len(items))
	firstIndex := make(map[string]int, len(items))
	for i, item := range items {
		name := nameOf(item)
		if first, ok := firstIndex[name]; ok {
			*findings = append(*findings, Finding{
				Severity: SeverityError,
				Code:     CodeDuplicateResourceName,
				Resource: fmt.Sprintf("%s/%s", typeName, name),
				Message:  fmt.Sprintf("duplicate %s resource name %q at indexes %d and %d", typeName, name, first, i),
			})
			continue
		}
		firstIndex[name] = i
		out[name] = item
	}
	return out
}

func listenerResource(name string) string {
	return fmt.Sprintf("Listener/%s", name)
}

func routeResource(name string) string {
	return fmt.Sprintf("RouteConfiguration/%s", name)
}

func clusterResource(name string) string {
	return fmt.Sprintf("Cluster/%s", name)
}
