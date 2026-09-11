package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Scenario "secrets" (RF-004, RF-020, RF-021): SDS delivery, rotation, and
// removal semantics on the pinned Envoy, driven over ADS like kgateway's
// main server delivers Secret resources.
//
//	phase 0  TLS listener on 10004 whose certificate is SDS secret "cert"
//	         (subject CN cert-1); plain listener on 10000; cluster a ready
//	phase 1  the secret's content rotates in place (CN cert-2), same name
//	phase 2  the secret is removed from the SDS response
//	phase 3  a second TLS listener on 10006 references secret "never", which
//	         the server does not carry
//
// Private keys are generated per run inside this process and appear only in
// the SDS response; the wire log records types, versions, and nonces, and
// Envoy redacts key material in its config dump.
type probeCertificate struct {
	certPEM, keyPEM []byte
	commonName      string
}

func newProbeCertificate(commonName string) (probeCertificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return probeCertificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return probeCertificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"probe.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return probeCertificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return probeCertificate{}, err
	}
	return probeCertificate{
		certPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		commonName: commonName,
	}, nil
}

// secretSchedule holds the per-run certificates for the secrets scenario.
type secretSchedule struct {
	first, second, third probeCertificate
}

// mismatched pairs one certificate with another's private key, which Envoy
// rejects while loading (KEY_VALUES_MISMATCH).
func (s secretSchedule) mismatched() probeCertificate {
	return probeCertificate{certPEM: s.first.certPEM, keyPEM: s.second.keyPEM, commonName: s.first.commonName}
}

func (s secretSchedule) resources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	cla := &envoyendpointv3.ClusterLoadAssignment{ClusterName: "a", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	httpListener := func(name string, port uint32, secretName string, sdsSource *envoycorev3.ConfigSource) *envoylistenerv3.Listener {
		hm := &hcm.HttpConnectionManager{StatPrefix: name, RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
		chain := &envoylistenerv3.FilterChain{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}
		if secretName != "" {
			chain.TransportSocket = &envoycorev3.TransportSocket{Name: "envoy.transport_sockets.tls", ConfigType: &envoycorev3.TransportSocket_TypedConfig{TypedConfig: packed(&envoytlsv3.DownstreamTlsContext{
				CommonTlsContext: &envoytlsv3.CommonTlsContext{TlsCertificateSdsSecretConfigs: []*envoytlsv3.SdsSecretConfig{{Name: secretName, SdsConfig: sdsSource}}},
			})}}
		}
		return &envoylistenerv3.Listener{Name: name, Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: port}}}}, FilterChains: []*envoylistenerv3.FilterChain{chain}}
	}
	secret := func(name string, c probeCertificate) *envoytlsv3.Secret {
		return &envoytlsv3.Secret{Name: name, Type: &envoytlsv3.Secret_TlsCertificate{TlsCertificate: &envoytlsv3.TlsCertificate{
			CertificateChain: &envoycorev3.DataSource{Specifier: &envoycorev3.DataSource_InlineBytes{InlineBytes: c.certPEM}},
			PrivateKey:       &envoycorev3.DataSource{Specifier: &envoycorev3.DataSource_InlineBytes{InlineBytes: c.keyPEM}},
		}}}
	}

	// Phase 5 changes only the bytes of the tls listener's SDS ConfigSource
	// (Envoy #47309): a new secret provider for an already-subscribed name.
	tlsSource := ads()
	if phase >= 5 {
		tlsSource = adsWithTimeout(5 * time.Second)
	}
	listeners := []*anypb.Any{packed(httpListener("front", 10000, "", nil)), packed(httpListener("tls", 10004, "cert", tlsSource))}
	switch {
	case phase == 3:
		listeners = append(listeners, packed(httpListener("orphan-tls", 10006, "never", ads())))
	case phase >= 4:
		// The second TLS listener now references cert2, delivered below.
		listeners = append(listeners, packed(httpListener("orphan-tls", 10006, "cert2", ads())))
	}
	secrets := []*anypb.Any{}
	switch {
	case phase == 0:
		secrets = append(secrets, packed(secret("cert", s.first)))
	case phase == 1 || phase == 3:
		secrets = append(secrets, packed(secret("cert", s.second)))
	case phase == 2:
		// Removed: the SDS response for the subscribed name carries nothing.
	case phase == 4:
		// A valid rotation of cert beside an invalid cert2 in one response.
		secrets = append(secrets, packed(secret("cert", s.third)), packed(secret("cert2", s.mismatched())))
	case phase >= 5:
		secrets = append(secrets, packed(secret("cert", s.third)), packed(secret("cert2", s.second)))
	}
	return map[string][]*anypb.Any{
		resource.ClusterType:  {packed(edsCluster("a"))},
		resource.EndpointType: {packed(cla)},
		resource.RouteType:    {packed(rc)},
		resource.ListenerType: listeners,
		resource.SecretType:   secrets,
	}
}

// servedCommonName performs a TLS handshake against addr and returns the
// server certificate's common name.
func servedCommonName(addr string) (string, error) {
	dialer := &net.Dialer{Timeout: time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: "probe.local"}) //nolint:gosec // G402: the probe inspects a self-signed test certificate
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no peer certificate")
	}
	return certs[0].Subject.CommonName, nil
}

// secretNames reads the dynamic active secret names from a config dump.
func secretNames(body string) []string {
	var dump struct {
		Configs []struct {
			Active []struct {
				Name string `json:"name"`
			} `json:"dynamic_active_secrets"`
			Warming []struct {
				Name string `json:"name"`
			} `json:"dynamic_warming_secrets"`
		} `json:"configs"`
	}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return nil
	}
	var names []string
	for _, config := range dump.Configs {
		for _, s := range config.Active {
			names = append(names, "active:"+s.Name)
		}
		for _, s := range config.Warming {
			names = append(names, "warming:"+s.Name)
		}
	}
	return names
}

func runSecrets(ctx context.Context, dir, admin, front, tlsAddr, orphanAddr string, p *probeServer, advance func(int) error, schedule secretSchedule) error {
	awaitNack := func(typ string) error {
		for {
			select {
			case got := <-p.nacks:
				if got == typ {
					return nil
				}
			case <-ctx.Done():
				return fmt.Errorf("no NACK of %s observed: %w", typ, ctx.Err())
			}
		}
	}
	save := func(name string) (string, error) {
		_, body, err := get(admin + "/config_dump")
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	quiet := func(window time.Duration) error {
		for len(p.nacks) > 0 {
			<-p.nacks
		}
		before := p.nackCount.Load()
		select {
		case <-time.After(window):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delta := p.nackCount.Load() - before; delta != 0 {
			return fmt.Errorf("unexpected %d NACK(s)", delta)
		}
		return nil
	}
	awaitCommonName := func(want string) error {
		return await(ctx, func() bool { got, err := servedCommonName(tlsAddr); return err == nil && got == want })
	}

	// Phase 0: the SDS-backed listener serves the first certificate.
	if err := await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		return fmt.Errorf("readiness with an SDS listener: %w", err)
	}
	if err := awaitCommonName(schedule.first.commonName); err != nil {
		return fmt.Errorf("SDS certificate was not served: %w", err)
	}
	if err := await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("plain listener did not serve: %w", err)
	}
	body, err := save("sds-initial-config.json")
	if err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "sds-initial", "ready": 200, "served_cn": schedule.first.commonName, "secrets": secretNames(body)})

	// Phase 1: rotation in place.
	if err = advance(1); err != nil {
		return err
	}
	if err = awaitCommonName(schedule.second.commonName); err != nil {
		return fmt.Errorf("rotated SDS certificate was not served: %w", err)
	}
	if err = quiet(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 1: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "sds-rotated", "served_cn": schedule.second.commonName, "nack": false})

	// Phase 2: the secret disappears from the SDS response. Observe over a
	// stable window whether the listener keeps the last certificate.
	if err = advance(2); err != nil {
		return err
	}
	retained := 0
	for range 8 {
		got, err := servedCommonName(tlsAddr)
		if err == nil && got == schedule.second.commonName {
			retained++
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	body, err = save("sds-removed-config.json")
	if err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "sds-removed", "handshakes_with_last_cert": retained, "of": 8, "secrets": secretNames(body), "nack_count": p.nackCount.Load()})
	// RF-020: on the pinned Envoy an SDS response that no longer carries the
	// subscribed secret does not revoke the installed certificate; the
	// listener keeps serving the last one. A change here means removal
	// semantics moved.
	if retained != 8 {
		return fmt.Errorf("listener stopped serving the last certificate after SDS removal (%d/8 handshakes); reassess RF-020", retained)
	}

	// Phase 3: a listener whose secret never arrives.
	if err = advance(3); err != nil {
		return err
	}
	if err = await(ctx, func() bool {
		_, d, _ := get(admin + "/config_dump")
		return listenerStates(d)["orphan-tls"].err == "" && strings.Contains(d, `"orphan-tls"`)
	}); err != nil {
		return fmt.Errorf("orphan listener never appeared in the dump: %w", err)
	}
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	body, err = save("sds-missing-config.json")
	if err != nil {
		return err
	}
	states := listenerStates(body)
	_, orphanErr := servedCommonName(orphanAddr)
	code, _, _ := get(admin + "/ready")
	frontCode, _, _ := get(front + "/")
	p.record(map[string]any{"event": "observation", "phase": "sds-missing", "orphan_active": states["orphan-tls"].active, "orphan_handshake_error": orphanErr != nil, "ready": code, "front": frontCode, "secrets": secretNames(body)})
	if states["orphan-tls"].active {
		return errors.New("listener with a missing SDS secret became active")
	}
	if orphanErr == nil {
		return errors.New("listener with a missing SDS secret completed a handshake")
	}
	if code != 200 || frontCode != 200 {
		return fmt.Errorf("a warming SDS listener changed readiness (%d) or the plain listener (%d)", code, frontCode)
	}
	if err = quiet(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 3: %w", err)
	}
	// Phase 4 (RF-026, Envoy Gateway #9463): one SDS response carries a valid
	// rotation of cert (CN cert-3) and an invalid cert2 whose key does not
	// match its certificate. Does the valid sibling apply?
	if err = advance(4); err != nil {
		return err
	}
	if err = awaitNack(resource.SecretType); err != nil {
		return err
	}
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	servedCN, _ := servedCommonName(tlsAddr)
	body, err = save("sds-partial-rejection-config.json")
	if err != nil {
		return err
	}
	sdsSiblingApplied := servedCN == schedule.third.commonName
	p.record(map[string]any{"event": "observation", "phase": "sds-partial-rejection", "served_cn": servedCN, "valid_sibling_applied": sdsSiblingApplied, "secrets": secretNames(body)})
	if sdsSiblingApplied {
		return fmt.Errorf("valid secret in the rejected SDS response was applied (serving %s); reassess RF-026 for SDS", servedCN)
	}

	// Phase 5 (Envoy #47309): the tls listener's SDS ConfigSource bytes change
	// (an initial_fetch_timeout is added) while the secret name, content, and
	// SDS version stay the same, and cert2 becomes valid.
	if err = advance(5); err != nil {
		return err
	}
	if err = await(ctx, func() bool {
		got, err := servedCommonName(orphanAddr)
		return err == nil && got == schedule.second.commonName
	}); err != nil {
		return fmt.Errorf("corrected cert2 was not served: %w", err)
	}
	if err = await(ctx, func() bool {
		got, err := servedCommonName(tlsAddr)
		return err == nil && got == schedule.third.commonName
	}); err != nil {
		return fmt.Errorf("rotated cert was not served after the corrected response: %w", err)
	}
	// Watch the tls listener across the new provider's 5 s initial fetch
	// timeout: a provider that never receives the secret would time out and
	// replace the serving listener with one that has no certificate.
	handshakes, failures := 0, 0
	for range 16 {
		if got, err := servedCommonName(tlsAddr); err == nil && got == schedule.third.commonName {
			handshakes++
		} else {
			failures++
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	body, err = save("sds-provider-key-change-config.json")
	if err != nil {
		return err
	}
	states = listenerStates(body)
	p.record(map[string]any{"event": "observation", "phase": "sds-provider-key-change", "handshakes_ok": handshakes, "handshakes_failed": failures, "tls_active": states["tls"].active, "secrets": secretNames(body), "nacks": p.nackCount.Load()})
	if failures != 0 {
		return fmt.Errorf("tls listener failed %d of %d handshakes after its SDS ConfigSource changed (Envoy #47309 shape); reassess RF-027", failures, handshakes+failures)
	}
	fmt.Println("PASS Envoy secrets characterization: SDS rotates in place, removal retains the last certificate, a missing secret holds only its listener, an invalid sibling rejects the whole SDS response, a provider key change keeps serving")
	return nil
}
