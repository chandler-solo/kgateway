package main

import (
	"context"
	"strconv"

	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	sotw "github.com/envoyproxy/go-control-plane/pkg/server/sotw/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
)

func versionFor(phase int, typ string) string {
	if typ == resource.EndpointType {
		if phase == 3 || phase == 4 {
			return "2"
		}
		return strconv.Itoa(phase)
	}
	if typ == resource.ClusterType && phase >= 3 {
		return "rewarm"
	}
	return "base"
}

// Wire the same fixture through the actual dependency. A repeated SetSnapshot
// at unchanged EDS version does NOT force an EDS response (RF-017).
func installCacheServer(ctx context.Context, g *grpc.Server, p *probeServer, ordered, publishInitial bool) (func(int) error, error) {
	c := cache.NewSnapshotCache(true, cache.IDHash{}, nil)
	publish := func(phase int) error {
		rs := map[resource.Type][]cachetypes.Resource{}
		for typ, items := range p.resourcesFor(phase) {
			for _, item := range items {
				msg, err := item.UnmarshalNew()
				if err != nil {
					return err
				}
				rs[typ] = append(rs[typ], msg)
			}
		}
		snap, err := cache.NewSnapshot("base", rs)
		if err != nil {
			return err
		}
		for typ := range rs {
			snap.Resources[cache.GetResponseType(typ)].Version = p.versionFor(phase, typ)
		}
		err = c.SetSnapshot(ctx, "formal-probe", snap)
		var failure any
		if err != nil {
			failure = err.Error()
		}
		p.record(map[string]any{"event": "cache-set-result", "phase": phase, "error": failure})
		return err
	}
	if publishInitial {
		if err := publish(0); err != nil {
			return nil, err
		}
	}
	callbacks := server.CallbackFuncs{
		StreamRequestFunc: func(_ int64, r *discovery.DiscoveryRequest) error {
			p.record(map[string]any{"event": "request", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.ResponseNonce, "names": r.ResourceNames, "error": r.ErrorDetail})
			p.noteRequest(r.TypeUrl, r.VersionInfo, r.ResponseNonce, r.ResourceNames)
			if r.ErrorDetail != nil {
				return p.observeNack(r.TypeUrl)
			}
			return nil
		},
		StreamResponseFunc: func(_ context.Context, _ int64, _ *discovery.DiscoveryRequest, r *discovery.DiscoveryResponse) {
			p.responseCount.Add(1)
			p.record(map[string]any{"event": "response", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.Nonce})
		},
	}
	if ordered {
		discovery.RegisterAggregatedDiscoveryServiceServer(g, server.NewServer(ctx, c, callbacks, sotw.WithOrderedADS()))
	} else {
		discovery.RegisterAggregatedDiscoveryServiceServer(g, server.NewServer(ctx, c, callbacks))
	}
	return publish, nil
}
