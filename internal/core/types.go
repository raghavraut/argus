// Package core defines the shared domain types and module contracts for Rarefy.
//
// The interfaces here are the extension points for the graph exporter
// (Neo4j/DOT/Mermaid, deferred to a later iteration): keep EvidenceGraph
// small, snapshot-friendly, and free of exporter concerns.
package core

import "context"

// ExecutionProfile controls stealth vs. depth trade-offs.
type ExecutionProfile string

const (
	ProfileStealth    ExecutionProfile = "stealth"
	ProfileStandard   ExecutionProfile = "standard"
	ProfileAggressive ExecutionProfile = "aggressive"
)

// NodeType classifies graph nodes.
type NodeType string

const (
	NodeAsset           NodeType = "asset"
	NodeInfrastructure  NodeType = "infrastructure"
	NodeIdentity        NodeType = "identity"
	NodeSurface         NodeType = "surface"
)

// EdgeType classifies graph relationships.
type EdgeType string

const (
	EdgeResolvesTo EdgeType = "RESOLVES_TO"
	EdgeSharesCert EdgeType = "SHARES_CERT"
	EdgeRedirectsTo EdgeType = "REDIRECTS_TO"
	EdgeImportsJS  EdgeType = "IMPORTS_JS"
	EdgeLinkedFrom EdgeType = "LINKED_FROM"
)

// Node is a single vertex in the evidence graph.
// Bodies are never stored here — only hashes and metadata (see memory fix).
type Node struct {
	ID    string            `json:"id"`
	Type  NodeType          `json:"type"`
	Score float64           `json:"score"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Edge is a directed relationship between two nodes.
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Type EdgeType `json:"type"`
}

// Asset is the recon target under triage.
type Asset struct {
	Name string `json:"name"`
}

// HTTPResponse is a memory-bounded probe result.
//
// Body is capped by the prober (see probe package). TokenCounts carries the
// pre-tokenized signal counts so TF-IDF never needs the raw body.
type HTTPResponse struct {
	Asset        string         `json:"asset"`
	StatusCode   int            `json:"status"`
	Title        string         `json:"title"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyPreview  string         `json:"body_preview,omitempty"`
	BodyMD5      string         `json:"body_md5"`
	SimHash      uint64         `json:"simhash"`
	FaviconHash  string         `json:"favicon_hash,omitempty"`
	TokenCounts  map[string]int `json:"-"`
	TotalTokens  int            `json:"-"`
	CertSANs     []string       `json:"cert_sans,omitempty"`
	IPs          []string       `json:"ips,omitempty"`
	CDN          string         `json:"cdn,omitempty"`
	Tech         []string       `json:"tech,omitempty"`
}

// TriageResult is the final output structure (JSONL to stdout).
type TriageResult struct {
	Asset          string   `json:"asset"`
	FinalScore     float64  `json:"score"`
	Confidence     float64  `json:"confidence"`
	RarityIndex    float64  `json:"rarity_index"`
	Evidence       []string `json:"evidence"`
	ClusterID      string   `json:"cluster_id"`
	ClusterSize    int      `json:"cluster_size,omitempty"`
	Final          bool     `json:"final"`
	Recommendation string   `json:"recommendation"`
	Status         int      `json:"status,omitempty"`
	Title          string   `json:"title,omitempty"`
	Tech           []string `json:"tech,omitempty"`
}

// LLMClassification is the Ollama verdict for ambiguous responses.
type LLMClassification struct {
	Classification string  `json:"classification"`
	Confidence     float64 `json:"confidence"`
	Reason         string  `json:"reason"`
	Degraded       bool    `json:"degraded,omitempty"`
}

// Task is a unit of DAG work. Stage+Asset uniquely identify resumable work.
type Task struct {
	Stage   string `json:"stage"`
	Asset   string `json:"asset"`
	Profile ExecutionProfile `json:"-"`
}

// EvidenceGraph defines the contract for the relational data model.
// Implementations must be safe for concurrent use and must support
// snapshot reads so a future exporter can traverse without holding locks.
type EvidenceGraph interface {
	AddNode(ctx context.Context, node Node) error
	AddEdge(ctx context.Context, edge Edge) error
	GetNeighbors(ctx context.Context, nodeID string, edgeType EdgeType) ([]Node, error)
	// PropagateScore flows interest through the graph (e.g. shared certs).
	// Implementations must terminate on cyclic graphs (visited set + depth cap).
	PropagateScore(ctx context.Context, nodeID string, score float64, decayFactor float64) error
	// Snapshot returns a point-in-time copy for exporters/scorers.
	Snapshot(ctx context.Context) ([]Node, []Edge, error)
}

// TriageEngine defines the scoring and rarity calculation contract.
type TriageEngine interface {
	// CalculateTFIDF analyzes the campaign corpus to assign rarity weights.
	// It must operate on token sketches, never on full bodies.
	CalculateTFIDF(ctx context.Context, corpus []HTTPResponse) (map[string]float64, error)
	// Score evaluates a single asset using rarity + confidence multipliers.
	Score(ctx context.Context, asset Asset, graph EvidenceGraph) (TriageResult, error)
}

// SemanticAnalyzer defines the local LLM integration contract.
type SemanticAnalyzer interface {
	ClassifyAmbiguous(ctx context.Context, resp HTTPResponse) (LLMClassification, error)
}

// DAGExecutor manages the asynchronous task queue and state resumability.
type DAGExecutor interface {
	Submit(ctx context.Context, task Task) error
	ResumeFromState(ctx context.Context, dbPath string) error
	SetProfile(profile ExecutionProfile)
}
