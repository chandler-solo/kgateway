package main

import (
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

func timeoutFixture(t *testing.T) {
	t.Helper()
	previous := resourceFetchTimeout
	resourceFetchTimeout = -time.Second
	t.Cleanup(func() { resourceFetchTimeout = previous })
}

func TestTimeoutRewarmingDoesNotInventEDSChange(t *testing.T) {
	timeoutFixture(t)
	before, after := timeoutProfileResources("cached-rewarm", 0), timeoutProfileResources("cached-rewarm", 1)
	if timeoutProfileVersion(before[resource.EndpointType]) != timeoutProfileVersion(after[resource.EndpointType]) {
		t.Fatal("unchanged EDS must keep its version; an artificial replay would hide fallback")
	}
	if timeoutProfileVersion(before[resource.ClusterType]) == timeoutProfileVersion(after[resource.ClusterType]) {
		t.Fatal("the changed CDS must trigger rewarming")
	}
}

func TestTimeoutFixtureLeavesGeneratedSourcesUnset(t *testing.T) {
	timeoutFixture(t)
	rs := timeoutProfileResources("missing-rds", 1)
	c := &envoyclusterv3.Cluster{}
	if err := rs[resource.ClusterType][0].UnmarshalTo(c); err != nil {
		t.Fatal(err)
	}
	if c.EdsClusterConfig.EdsConfig.InitialFetchTimeout != nil {
		t.Fatal("EDS must retain the unset default")
	}
	l := &envoylistenerv3.Listener{}
	if err := rs[resource.ListenerType][0].UnmarshalTo(l); err != nil {
		t.Fatal(err)
	}
	manager := &hcm.HttpConnectionManager{}
	if err := l.FilterChains[0].Filters[0].GetTypedConfig().UnmarshalTo(manager); err != nil {
		t.Fatal(err)
	}
	if manager.GetRds().ConfigSource.InitialFetchTimeout != nil {
		t.Fatal("RDS must retain the unset default")
	}
}

func TestTimeoutLocalCycleReleaseDoesNotDeliverDynamicEndpoints(t *testing.T) {
	timeoutFixture(t)
	rs := timeoutProfileResources("local-cycle", 1)
	if len(rs) != 1 || len(rs[resource.EndpointType]) != 1 {
		t.Fatal("cycle release must contain only local EDS")
	}
	assignment := &envoyendpointv3.ClusterLoadAssignment{}
	if err := rs[resource.EndpointType][0].UnmarshalTo(assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.ClusterName != timeoutLocalCluster {
		t.Fatal("wrong assignment identity")
	}
}

func TestTimeoutDisabledEDSControlDoesNotDisableRDS(t *testing.T) {
	timeoutFixture(t)
	rs := timeoutProfileResources("disabled-eds", 1)
	c := &envoyclusterv3.Cluster{}
	if err := rs[resource.ClusterType][0].UnmarshalTo(c); err != nil {
		t.Fatal(err)
	}
	if c.EdsClusterConfig.EdsConfig.InitialFetchTimeout == nil || c.EdsClusterConfig.EdsConfig.InitialFetchTimeout.AsDuration() != 0 {
		t.Fatal("EDS zero must be explicit")
	}
	l := &envoylistenerv3.Listener{}
	if err := rs[resource.ListenerType][0].UnmarshalTo(l); err != nil {
		t.Fatal(err)
	}
	manager := &hcm.HttpConnectionManager{}
	if err := l.FilterChains[0].Filters[0].GetTypedConfig().UnmarshalTo(manager); err != nil {
		t.Fatal(err)
	}
	if manager.GetRds().ConfigSource.InitialFetchTimeout != nil {
		t.Fatal("the control must not change RDS")
	}
}
