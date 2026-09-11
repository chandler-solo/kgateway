package xdscheck

import (
	"cmp"
	"context"
	"slices"
	"strings"
)

// DependencyGraph is the reference graph of a concrete xDS snapshot as seen by
// the checker's traversal: listener -> route configuration (RDS), listener or
// route -> cluster (route actions, filters, access logs, tracing), cluster ->
// ClusterLoadAssignment (EDS), and listener/cluster/filter -> secret (SDS).
// Static and bootstrap resources the checker exempts (the blackhole cluster
// and the system CA secret) are not nodes: they are shared by construction
// and would otherwise connect every component.
//
// The graph is the prerequisite for an isolated publication policy (RF-002,
// RF-021): a component is a unit whose resources can advance together while
// others are held. It is only as complete as the checker's traversal. Any
// typed config the checker could not unpack makes its origin resource
// opaque; an opaque node may reference anything, so no component containing
// one is safe to treat as independent.
type DependencyGraph struct {
	// Nodes are the emitted dynamic resources, as "Kind/name".
	Nodes map[string]struct{}
	// Edges maps a referencing node to the nodes it references. A target
	// that is not in Nodes is a dangling reference.
	Edges map[string]map[string]struct{}
	// Opaque maps a node to the reason its references are incomplete.
	Opaque map[string]string
}

// Component is a connected set of nodes in the undirected reference relation.
type Component struct {
	Nodes []string
	// Dangling lists referenced nodes absent from the snapshot.
	Dangling []string
	// Opaque is true when any member has references the checker could not
	// see; such a component must not be published independently.
	Opaque bool
}

func newDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		Nodes:  map[string]struct{}{},
		Edges:  map[string]map[string]struct{}{},
		Opaque: map[string]string{},
	}
}

func (g *DependencyGraph) addNode(id string) {
	g.Nodes[id] = struct{}{}
}

func (g *DependencyGraph) addEdge(from, to string) {
	if from == "" || to == "" {
		return
	}
	if g.Edges[from] == nil {
		g.Edges[from] = map[string]struct{}{}
	}
	g.Edges[from][to] = struct{}{}
}

// DependencyGraphOf runs the checker over the snapshot and returns the
// reference graph it traversed together with the findings. It changes no
// production behavior; the graph is a research and test instrument.
func DependencyGraphOf(ctx context.Context, s Snapshot) (*DependencyGraph, []Finding) {
	c := checker{graph: newDependencyGraph()}
	findings := c.run(ctx, s)
	return c.graph, findings
}

// Components partitions the graph into connected components of the
// undirected reference relation, each sorted by node id, ordered by their
// first node. Dangling targets participate in connectivity (two resources
// referencing the same missing cluster belong together) but are reported
// separately from emitted nodes.
func (g *DependencyGraph) Components() []Component {
	parent := map[string]string{}
	find := func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	ensure := func(x string) {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
	}
	for id := range g.Nodes {
		ensure(id)
	}
	for from, targets := range g.Edges {
		ensure(from)
		for to := range targets {
			ensure(to)
			union(from, to)
		}
	}
	groups := map[string]*Component{}
	for id := range parent {
		root := find(id)
		component := groups[root]
		if component == nil {
			component = &Component{}
			groups[root] = component
		}
		if _, emitted := g.Nodes[id]; emitted {
			component.Nodes = append(component.Nodes, id)
			if _, opaque := g.Opaque[id]; opaque {
				component.Opaque = true
			}
		} else {
			component.Dangling = append(component.Dangling, id)
		}
	}
	out := make([]Component, 0, len(groups))
	for _, component := range groups {
		slices.Sort(component.Nodes)
		slices.Sort(component.Dangling)
		out = append(out, *component)
	}
	slices.SortFunc(out, func(a, b Component) int {
		return cmp.Compare(firstOf(a), firstOf(b))
	})
	return out
}

func firstOf(c Component) string {
	if len(c.Nodes) > 0 {
		return c.Nodes[0]
	}
	if len(c.Dangling) > 0 {
		return c.Dangling[0]
	}
	return ""
}

// isOpaqueCode reports whether a finding code marks references the checker
// could not follow.
func isOpaqueCode(code string) bool {
	return strings.HasPrefix(code, "unsupported_")
}
