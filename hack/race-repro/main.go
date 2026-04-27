// race-repro drives sustained HTTP traffic at a kgateway data-plane while
// repeatedly hard-deleting the kgateway control-plane pod(s). It reports any
// non-2xx responses or transport errors observed during the restart cycles.
//
// The bug it exercises is the xDS race in
// pkg/kgateway/proxy_syncer/perclient.go::snapshotPerClient: when the
// controller restarts, Envoy reconnects under a new Uniquely Connected Client
// (UCC) and the per-client cluster transformation can race with the snapshot
// transformation, producing a partial CDS that omits clusters referenced by
// the listener/route. Affected requests get response flag NC ("no cluster")
// and HTTP 500/503.
//
// This tool is standalone (depends only on stdlib + kubectl on PATH) so it
// can be used to confirm the race independent of the e2e test framework.
//
// Usage:
//
//	go run ./hack/race-repro \
//	    --url=http://127.0.0.1:8080 \
//	    --host=example.com \
//	    --controller-namespace=zero-downtime \
//	    --concurrency=50 \
//	    --duration=5m \
//	    --restart-interval=4s
//
// See hack/race-repro/README.md for setup instructions (port-forward etc.).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	targetURL       = flag.String("url", "http://127.0.0.1:8080", "Target gateway URL (typically a kubectl port-forward address)")
	hostHeader      = flag.String("host", "example.com", "Host header to send")
	hostTemplate    = flag.String("host-template", "", "Optional fmt template for generated Host headers, e.g. repro-%d.example.com")
	hostCount       = flag.Int("host-count", 0, "Number of generated Host headers to rotate through with --host-template")
	hostsCSV        = flag.String("hosts", "", "Comma-separated Host headers to rotate through")
	concurrency     = flag.Int("concurrency", 50, "Number of concurrent HTTP workers")
	qps             = flag.Int("qps", 0, "Optional total request rate cap across all workers; 0 means unlimited")
	duration        = flag.Duration("duration", 5*time.Minute, "Total test duration")
	restartInterval = flag.Duration("restart-interval", 4*time.Second, "Time between controller pod hard-deletes")
	controllerNS    = flag.String("controller-namespace", "zero-downtime", "kgateway controller namespace")
	controllerLabel = flag.String("controller-label", "kgateway=kgateway", "Label selector for controller pods")
	requestTimeout  = flag.Duration("request-timeout", 3*time.Second, "Per-request timeout")
	statsEvery      = flag.Duration("stats-every", 2*time.Second, "Periodic stats interval")
	keepAlive       = flag.Bool("keepalive", false, "Use HTTP keepalive (default off — disabled to maximize race window)")
	stopOnError     = flag.Bool("stop-on-error", false, "Stop the run as soon as any non-2xx is observed")
	kubectlPath     = flag.String("kubectl", "kubectl", "Path to kubectl binary")
	skipDelete      = flag.Bool("skip-delete", false, "Skip controller pod deletes (sanity-check baseline traffic)")
)

type errorSample struct {
	at         time.Time
	host       string
	statusCode int // 0 if transport-level error
	body       string
	transport  error
}

type stats struct {
	total     atomic.Uint64
	succeeded atomic.Uint64
	transport atomic.Uint64 // network-level errors (connection refused, etc.)
	timeout   atomic.Uint64 // request timeouts
	by5xxMu   sync.Mutex
	by5xx     map[int]uint64

	samplesMu  sync.Mutex
	samples    []errorSample
	maxSamples int
}

func newStats() *stats {
	return &stats{
		by5xx:      map[int]uint64{},
		maxSamples: 32,
	}
}

func (s *stats) recordStatus(host string, code int, body string) {
	s.total.Add(1)
	switch {
	case code >= 200 && code < 300:
		s.succeeded.Add(1)
	case code >= 500:
		s.by5xxMu.Lock()
		s.by5xx[code]++
		s.by5xxMu.Unlock()
		s.recordSample(errorSample{at: time.Now(), host: host, statusCode: code, body: body})
	default:
		s.recordSample(errorSample{at: time.Now(), host: host, statusCode: code, body: body})
	}
}

func (s *stats) recordTransport(host string, err error, isTimeout bool) {
	s.total.Add(1)
	if isTimeout {
		s.timeout.Add(1)
	} else {
		s.transport.Add(1)
	}
	s.recordSample(errorSample{at: time.Now(), host: host, transport: err})
}

func (s *stats) recordSample(e errorSample) {
	s.samplesMu.Lock()
	defer s.samplesMu.Unlock()
	if len(s.samples) >= s.maxSamples {
		return
	}
	s.samples = append(s.samples, e)
}

func (s *stats) anyError() bool {
	if s.transport.Load() > 0 || s.timeout.Load() > 0 {
		return true
	}
	s.by5xxMu.Lock()
	defer s.by5xxMu.Unlock()
	for _, n := range s.by5xx {
		if n > 0 {
			return true
		}
	}
	return false
}

func (s *stats) snapshot() string {
	total := s.total.Load()
	ok := s.succeeded.Load()
	tr := s.transport.Load()
	to := s.timeout.Load()

	s.by5xxMu.Lock()
	codes := make([]int, 0, len(s.by5xx))
	for c := range s.by5xx {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	codeStrs := ""
	for _, c := range codes {
		codeStrs += fmt.Sprintf(" %d=%d", c, s.by5xx[c])
	}
	s.by5xxMu.Unlock()

	return fmt.Sprintf("requests=%d ok=%d transport=%d timeouts=%d%s",
		total, ok, tr, to, codeStrs)
}

func (s *stats) dumpSamples(w io.Writer) {
	s.samplesMu.Lock()
	defer s.samplesMu.Unlock()
	if len(s.samples) == 0 {
		return
	}
	fmt.Fprintf(w, "\nfirst %d error samples:\n", len(s.samples))
	for i, e := range s.samples {
		switch {
		case e.transport != nil:
			fmt.Fprintf(w, "  %2d %s host=%s transport: %v\n", i, e.at.Format("15:04:05.000"), e.host, e.transport)
		default:
			body := e.body
			if len(body) > 200 {
				body = body[:200] + "...(truncated)"
			}
			fmt.Fprintf(w, "  %2d %s host=%s status=%d body=%q\n", i, e.at.Format("15:04:05.000"), e.host, e.statusCode, body)
		}
	}
}

func main() {
	flag.Parse()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Honor Ctrl-C so partial runs still report.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nsignal received, draining...")
		rootCancel()
	}()

	runCtx, runCancel := context.WithTimeout(rootCtx, *duration)
	defer runCancel()

	hosts, err := buildHosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(hosts) == 0 {
		fmt.Fprintln(os.Stderr, "no hosts configured")
		os.Exit(2)
	}
	perWorkerDelay := rateDelay(*qps, *concurrency)
	if *qps > 0 {
		fmt.Printf("rate cap: %d qps across %d workers (%v per worker)\n", *qps, *concurrency, perWorkerDelay)
	}
	if len(hosts) > 1 {
		fmt.Printf("rotating %d hosts: first=%s last=%s\n", len(hosts), hosts[0], hosts[len(hosts)-1])
	}

	s := newStats()

	if *stopOnError {
		go func() {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					if s.anyError() {
						runCancel()
						return
					}
				}
			}
		}()
	}

	tr := &http.Transport{
		DisableKeepAlives:   !*keepAlive,
		DialContext:         (&net.Dialer{Timeout: 1 * time.Second}).DialContext,
		MaxIdleConnsPerHost: *concurrency,
	}
	client := &http.Client{Transport: tr, Timeout: *requestTimeout}

	var wg sync.WaitGroup

	// Worker goroutines pumping requests
	var nextHost atomic.Uint64
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runWorker(runCtx, client, s, hosts, &nextHost, perWorkerDelay)
		}()
	}

	// Restart goroutine
	if !*skipDelete {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRestarter(runCtx, s)
		}()
	}

	// Stats reporter
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(*statsEvery)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				fmt.Println(s.snapshot())
			}
		}
	}()

	wg.Wait()

	fmt.Println("\n=== final ===")
	fmt.Println(s.snapshot())
	s.dumpSamples(os.Stdout)

	if s.anyError() {
		os.Exit(1)
	}
}

func buildHosts() ([]string, error) {
	switch {
	case *hostsCSV != "":
		parts := strings.Split(*hostsCSV, ",")
		hosts := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				hosts = append(hosts, part)
			}
		}
		return hosts, nil
	case *hostTemplate != "" || *hostCount > 0:
		if *hostTemplate == "" || *hostCount <= 0 {
			return nil, fmt.Errorf("--host-template and --host-count must be set together")
		}
		hosts := make([]string, 0, *hostCount)
		for i := 1; i <= *hostCount; i++ {
			hosts = append(hosts, fmt.Sprintf(*hostTemplate, i))
		}
		return hosts, nil
	default:
		return []string{*hostHeader}, nil
	}
}

func rateDelay(totalQPS, workers int) time.Duration {
	if totalQPS <= 0 || workers <= 0 {
		return 0
	}
	perWorkerQPS := float64(totalQPS) / float64(workers)
	if perWorkerQPS <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / perWorkerQPS)
}

func runWorker(ctx context.Context, client *http.Client, s *stats, hosts []string, nextHost *atomic.Uint64, delay time.Duration) {
	var ticker *time.Ticker
	if delay > 0 {
		ticker = time.NewTicker(delay)
		defer ticker.Stop()
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if ticker != nil {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
		host := hosts[int(nextHost.Add(1)-1)%len(hosts)]
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, *targetURL, nil)
		if err != nil {
			return
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.recordTransport(host, err, isTimeout(err))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		s.recordStatus(host, resp.StatusCode, string(body))
	}
}

func runRestarter(ctx context.Context, s *stats) {
	ticker := time.NewTicker(*restartInterval)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			i++
			start := time.Now()
			cmd := exec.CommandContext(ctx, *kubectlPath, "delete", "pod",
				"-n", *controllerNS,
				"-l", *controllerLabel,
				"--grace-period=0", "--force", "--wait=false",
			)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fmt.Printf("[restart %d] kubectl error after %v: %v\n%s\n", i, time.Since(start), err, out)
				continue
			}
			fmt.Printf("[restart %d] %s (%v elapsed; cumulative: %s)\n",
				i, time.Now().Format("15:04:05.000"), time.Since(start), s.snapshot())
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}
