// Package graph provides a sharded in-memory EvidenceGraph.
//
// Fix for the single-RWMutex bottleneck: the node/edge space is sharded by
// FNV-1a(nodeID) % numShards, so concurrent AddNode/AddEdge calls on
// disjoint assets rarely contend. PropagateScore uses an iterative BFS with
// a visited set, depth cap, and score-delta cutoff so cyclic REDIRECTS_TO
// chains always terminate.
package graph

import (
	"context"
	"hash/fnv"
	"sync"

	"github.com/raghavraut/argus/internal/core"
)

const (
	numShards      = 32
	maxPropDepth   = 3
	minScoreDelta  = 0.01
	defaultDecay   = 0.5
)

type nodeEntry struct {
	mu   sync.Mutex
	node core.Node
}

type shard struct {
	mu    sync.RWMutex
	nodes map[string]*nodeEntry
	// adjacency: fromID -> edgeType -> toIDs
	edges map[string]map[core.EdgeType]map[string]struct{}
	// edgeList kept for Snapshot/exporters (append-only under shard lock).
	edgeList []core.Edge
}

// MemoryGraph is a sharded in-memory EvidenceGraph.
type MemoryGraph struct {
	shards [numShards]*shard
}

var _ core.EvidenceGraph = (*MemoryGraph)(nil)

// NewMemoryGraph creates an empty sharded graph.
func NewMemoryGraph() *MemoryGraph {
	g := &MemoryGraph{}
	for i := range g.shards {
		g.shards[i] = &shard{
			nodes: make(map[string]*nodeEntry),
			edges: make(map[string]map[core.EdgeType]map[string]struct{}),
		}
	}
	return g
}

func shardFor(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % numShards)
}

func (g *MemoryGraph) AddNode(_ context.Context, n core.Node) error {
	s := g.shards[shardFor(n.ID)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.nodes[n.ID]; ok {
		e.mu.Lock()
		if n.Score > e.node.Score {
			e.node.Score = n.Score
		}
		if e.node.Attrs == nil && n.Attrs != nil {
			e.node.Attrs = map[string]string{}
		}
		for k, v := range n.Attrs {
			if _, exists := e.node.Attrs[k]; !exists {
				e.node.Attrs[k] = v
			}
		}
		e.mu.Unlock()
		return nil
	}
	if n.Attrs == nil {
		n.Attrs = map[string]string{}
	}
	s.nodes[n.ID] = &nodeEntry{node: n}
	return nil
}

func (g *MemoryGraph) AddEdge(_ context.Context, e core.Edge) error {
	s := g.shards[shardFor(e.From)]
	s.mu.Lock()
	byType, ok := s.edges[e.From]
	if !ok {
		byType = make(map[core.EdgeType]map[string]struct{})
		s.edges[e.From] = byType
	}
	set, ok := byType[e.Type]
	if !ok {
		set = make(map[string]struct{})
		byType[e.Type] = set
	}
	if _, dup := set[e.To]; dup {
		s.mu.Unlock()
		return nil
	}
	set[e.To] = struct{}{}
	s.edgeList = append(s.edgeList, e)
	// Fast path: target placeholder already present in the same shard.
	_, sameShardHas := s.nodes[e.To]
	sameShard := shardFor(e.From) == shardFor(e.To)
	s.mu.Unlock()

	// Ensure target node exists as a placeholder so traversals don't drop
	// edges. Done WITHOUT holding the FROM shard lock (RWMutex is not
	// reentrant — locking it twice deadlocks).
	if sameShard {
		if sameShardHas {
			return nil
		}
		s.mu.Lock()
		if _, ok := s.nodes[e.To]; !ok {
			s.nodes[e.To] = &nodeEntry{node: core.Node{ID: e.To, Attrs: map[string]string{}}}
		}
		s.mu.Unlock()
		return nil
	}
	if _, ok := g.findEntry(e.To); ok {
		return nil
	}
	ts := g.shards[shardFor(e.To)]
	ts.mu.Lock()
	if _, ok := ts.nodes[e.To]; !ok {
		ts.nodes[e.To] = &nodeEntry{node: core.Node{ID: e.To, Attrs: map[string]string{}}}
	}
	ts.mu.Unlock()
	return nil
}

// findEntry locates a node entry; caller must not hold shard locks that
// would deadlock (uses RLock only).
func (g *MemoryGraph) findEntry(id string) (*nodeEntry, bool) {
	s := g.shards[shardFor(id)]
	s.mu.RLock()
	e, ok := s.nodes[id]
	s.mu.RUnlock()
	return e, ok
}

func (g *MemoryGraph) GetNeighbors(_ context.Context, nodeID string, edgeType core.EdgeType) ([]core.Node, error) {
	s := g.shards[shardFor(nodeID)]
	s.mu.RLock()
	byType, ok := s.edges[nodeID]
	var targets []string
	if ok {
		for to := range byType[edgeType] {
			targets = append(targets, to)
		}
	}
	s.mu.RUnlock()

	out := make([]core.Node, 0, len(targets))
	for _, id := range targets {
		if e, ok := g.findEntry(id); ok {
			e.mu.Lock()
			cp := e.node
			e.mu.Unlock()
			out = append(out, cp)
		}
	}
	return out, nil
}

// PropagateScore flows score outward via BFS with decay.
//
// Terminates on cycles via visited set; stops beyond maxPropDepth or when
// the decayed delta falls below minScoreDelta. Locks are never held across
// levels: neighbor IDs are snapshotted, then scores applied per-node.
func (g *MemoryGraph) PropagateScore(ctx context.Context, nodeID string, score float64, decayFactor float64) error {
	if decayFactor <= 0 || decayFactor >= 1 {
		decayFactor = defaultDecay
	}
	type item struct {
		id    string
		score float64
		depth int
	}
	visited := map[string]bool{nodeID: true}
	queue := []item{{id: nodeID, score: score, depth: 0}}

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cur := queue[0]
		queue = queue[1:]

		// Snapshot all outgoing neighbor IDs without holding locks across shards.
		var neighborIDs []string
		for i := range g.shards {
			sh := g.shards[i]
			sh.mu.RLock()
			for _, set := range sh.edges[cur.id] {
				// edges are stored under the FROM shard, so only one shard hits;
				// loop is cheap because map lookup misses fast.
				for to := range set {
					neighborIDs = append(neighborIDs, to)
				}
			}
			sh.mu.RUnlock()
			if len(neighborIDs) > 0 {
				break
			}
		}

		for _, nid := range neighborIDs {
			if visited[nid] {
				continue
			}
			visited[nid] = true
			decayed := cur.score * decayFactor
			if cur.depth+1 > maxPropDepth || decayed < minScoreDelta {
				continue
			}
			if e, ok := g.findEntry(nid); ok {
				e.mu.Lock()
				if decayed > e.node.Score {
					e.node.Score = decayed
				}
				e.mu.Unlock()
			}
			queue = append(queue, item{id: nid, score: decayed, depth: cur.depth + 1})
		}
	}
	return nil
}

// Snapshot returns a point-in-time copy for scorers and future exporters.
func (g *MemoryGraph) Snapshot(_ context.Context) ([]core.Node, []core.Edge, error) {
	var nodes []core.Node
	var edges []core.Edge
	for _, s := range g.shards {
		s.mu.RLock()
		for _, e := range s.nodes {
			e.mu.Lock()
			nodes = append(nodes, e.node)
			e.mu.Unlock()
		}
		edges = append(edges, s.edgeList...)
		s.mu.RUnlock()
	}
	return nodes, edges, nil
}
