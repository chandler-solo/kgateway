package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

const timeoutLocalCluster = "formal.formal"

func validTimeoutCase(name string) bool {
	switch name {
	case "missing-cds", "missing-lds", "missing-eds", "missing-rds", "explicit-empty", "cached-rewarm", "disabled-eds", "local-cycle":
		return true
	default:
		return false
	}
}

func timeoutProfileResources(name string, phase int) map[string][]*anypb.Any {
	rs := resources(2, false)
	local := packed(&envoyendpointv3.ClusterLoadAssignment{ClusterName: timeoutLocalCluster})
	rs[resource.EndpointType] = append(rs[resource.EndpointType], local)
	if name == "local-cycle" && phase == 1 {
		return map[string][]*anypb.Any{resource.EndpointType: {local}}
	}
	if phase == 0 {
		switch name {
		case "missing-eds", "disabled-eds":
			delete(rs, resource.EndpointType)
		case "missing-rds":
			delete(rs, resource.RouteType)
		case "missing-lds":
			delete(rs, resource.ListenerType)
			delete(rs, resource.RouteType)
		case "explicit-empty":
			rs[resource.EndpointType] = []*anypb.Any{packed(&envoyendpointv3.ClusterLoadAssignment{ClusterName: "a"}), local}
		}
	}
	if name == "disabled-eds" {
		c := &envoyclusterv3.Cluster{}
		if err := rs[resource.ClusterType][0].UnmarshalTo(c); err != nil {
			panic(err)
		}
		c.EdsClusterConfig.EdsConfig.InitialFetchTimeout = durationpb.New(0)
		rs[resource.ClusterType][0] = packed(c)
	}
	if name == "cached-rewarm" && phase >= 1 {
		c := &envoyclusterv3.Cluster{}
		if err := rs[resource.ClusterType][0].UnmarshalTo(c); err != nil {
			panic(err)
		}
		c.ConnectTimeout = durationpb.New(3 * time.Second)
		rs[resource.ClusterType][0] = packed(c)
	}
	return rs
}

// Only this direct timeout fixture uses proto bytes as versions. This is not
// the production zero-marshal EDS algorithm; stable bytes prevent artificial replay.
func timeoutProfileVersion(items []*anypb.Any) string {
	values := []string{}
	for _, item := range items {
		values = append(values, string(item.Value))
	}
	slices.Sort(values)
	raw, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

type timeoutADS struct {
	discovery.UnimplementedAggregatedDiscoveryServiceServer
	inner discovery.AggregatedDiscoveryServiceServer
	probe *probeServer
}

type timeoutStream struct {
	discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer
	probe *probeServer
}

func (s timeoutStream) Send(r *discovery.DiscoveryResponse) error {
	values := []json.RawMessage{}
	for _, item := range r.Resources {
		raw, err := protojson.Marshal(item)
		if err != nil {
			return err
		}
		values = append(values, raw)
	}
	s.probe.record(map[string]any{"event": "send-attempt", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.Nonce, "resources": values})
	err := s.AggregatedDiscoveryService_StreamAggregatedResourcesServer.Send(r)
	status := "sent"
	if err != nil {
		status = err.Error()
	}
	s.probe.record(map[string]any{"event": "send-result", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.Nonce, "status": status})
	return err
}

func (s timeoutADS) StreamAggregatedResources(st discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer) error {
	return s.inner.StreamAggregatedResources(timeoutStream{st, s.probe})
}

func (p *probeServer) timeoutObservation(dir, admin, front, phase string) error {
	code, raw, err := get(admin + "/config_dump?include_eds")
	if err != nil || code != 200 {
		return fmt.Errorf("admin observation: %d %w", code, err)
	}
	configName := phase + "-config.json"
	if err = os.WriteFile(filepath.Join(dir, configName), []byte(raw), 0o600); err != nil {
		return err
	}
	_, stats, err := get(admin + "/stats?format=json&filter=init_fetch_timeout%7Cassignment_use_cached")
	if err != nil {
		return err
	}
	statsName := phase + "-stats.json"
	if err = os.WriteFile(filepath.Join(dir, statsName), []byte(stats), 0o600); err != nil {
		return err
	}
	ready, _, err := get(admin + "/ready")
	if err != nil {
		return err
	}
	status, body, requestErr := get(front + "/")
	if requestErr != nil {
		status, body = 0, ""
	}
	p.record(map[string]any{
		"event":        "observation",
		"phase":        phase,
		"config":       configName,
		"sha256":       fmt.Sprintf("%x", sha256.Sum256([]byte(raw))),
		"stats":        statsName,
		"stats_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(stats))),
		"ready":        ready,
		"status":       status,
		"body":         body,
	})
	return nil
}

func waitTimeoutWindow(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func timeoutCluster(admin string, warming bool, connectTimeout string) bool {
	_, body, err := get(admin + "/config_dump")
	if err != nil {
		return false
	}
	var dump struct {
		Configs []struct {
			Active []struct {
				Cluster struct {
					Name           string
					ConnectTimeout string `json:"connect_timeout"`
				}
			} `json:"dynamic_active_clusters"`
			Warming []struct {
				Cluster struct {
					Name           string
					ConnectTimeout string `json:"connect_timeout"`
				}
			} `json:"dynamic_warming_clusters"`
		}
	}
	if json.Unmarshal([]byte(body), &dump) != nil {
		return false
	}
	for _, config := range dump.Configs {
		items := config.Active
		if warming {
			items = config.Warming
		}
		for _, item := range items {
			if item.Cluster.Name == "a" && (connectTimeout == "" || item.Cluster.ConnectTimeout == connectTimeout) {
				return true
			}
		}
	}
	return false
}

func runTimeoutProfile(ctx context.Context, dir, admin, front string, p *probeServer, advance func(int) error) error {
	name := p.timeoutCase
	p.record(map[string]any{"event": "case", "name": name, "fetch_timeout_ns": resourceFetchTimeout.Nanoseconds(), "eds_cache": edsCacheEnabled})
	// Validate the actual executed bootstrap, including declared zero-timeout controls.
	raw, err := os.ReadFile(filepath.Join(dir, "bootstrap.json"))
	if err != nil {
		return err
	}
	var boot envoybootstrapv3.Bootstrap
	if err = protojson.Unmarshal(raw, &boot); err != nil {
		return err
	}
	if boot.DynamicResources.CdsConfig.InitialFetchTimeout == nil || boot.DynamicResources.CdsConfig.InitialFetchTimeout.AsDuration() != 0 ||
		boot.DynamicResources.LdsConfig.InitialFetchTimeout == nil || boot.DynamicResources.LdsConfig.InitialFetchTimeout.AsDuration() != 0 {
		return errors.New("CDS/LDS must explicitly disable timeout")
	}
	requestSeen := func(typ string) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, r := range p.requests {
			if r.typeURL == typ {
				return true
			}
		}
		return false
	}
	restore := func(phase int) error {
		if e := advance(phase); e != nil {
			return e
		}
		if e := await(ctx, func() bool { code, body, _ := get(front + "/"); return code == 200 && body == "probe-upstream" }); e != nil {
			return e
		}
		return p.timeoutObservation(dir, admin, front, "recovered")
	}
	switch name {
	case "missing-cds", "missing-lds", "disabled-eds", "local-cycle":
		typ := resource.ClusterType
		if name == "missing-lds" {
			typ = resource.ListenerType
		}
		if name == "disabled-eds" || name == "local-cycle" {
			typ = resource.EndpointType
		}
		if err = await(ctx, func() bool { return requestSeen(typ) }); err != nil {
			return err
		}
		if err = p.timeoutObservation(dir, admin, front, "waiting"); err != nil {
			return err
		}
		if err = waitTimeoutWindow(ctx, 16*time.Second); err != nil {
			return err
		}
		if err = p.timeoutObservation(dir, admin, front, "still-waiting"); err != nil {
			return err
		}
		ready, _, _ := get(admin + "/ready")
		if ready == 200 {
			return errors.New("disabled-timeout history unexpectedly ready")
		}
		if name == "local-cycle" {
			if requestSeen(resource.ClusterType) {
				return errors.New("local cycle did not block CDS")
			}
			// The initial named watch was declined before CDS could name a.
			// With no local timeout, no request or watch advances this cycle.
			return nil
		}
		return restore(1)
	case "cached-rewarm":
		if err = await(ctx, func() bool { code, body, _ := get(front + "/"); return code == 200 && body == "probe-upstream" }); err != nil {
			return err
		}
		if err = p.timeoutObservation(dir, admin, front, "baseline"); err != nil {
			return err
		}
		if err = advance(1); err != nil {
			return err
		}
		if err = await(ctx, func() bool { return timeoutCluster(admin, true, "3s") }); err != nil {
			return err
		}
		if err = p.timeoutObservation(dir, admin, front, "rewarming"); err != nil {
			return err
		}
		if err = await(ctx, func() bool { return timeoutCluster(admin, false, "3s") }); err != nil {
			return err
		}
		return p.timeoutObservation(dir, admin, front, "cached-active")
	default:
		typ := resource.EndpointType
		if name == "missing-rds" {
			typ = resource.RouteType
		}
		if err = await(ctx, func() bool { return requestSeen(typ) }); err != nil {
			return err
		}
		if name != "explicit-empty" {
			if err = p.timeoutObservation(dir, admin, front, "waiting"); err != nil {
				return err
			}
		}
		if err = await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
			return err
		}
		if err = p.timeoutObservation(dir, admin, front, "degraded-active"); err != nil {
			return err
		}
		code, _, _ := get(front + "/")
		want := 503
		if name == "missing-rds" {
			want = 404
		}
		if code != want {
			return fmt.Errorf("degraded status %d, want %d", code, want)
		}
		return restore(1)
	}
}
