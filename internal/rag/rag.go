// Package rag provides the Retrieval-Augmented Generation subsystem.
//
// Architecture:
//   - Vector store: SQLite + sqlite-vec extension (replaces sqlite-vss)
//   - Embeddings: bge-m3 via local inference endpoint
//   - LLM: Qwen2.5-Coder-32B-Instruct (local) — ONLY for translating/prioritizing
//     findings already validated by deterministic checkers. NEVER for detection.
//
// Source restrictions (enforced by metadata filter on every query):
//   - eks:       https://docs.aws.amazon.com/eks/
//   - upstream:  https://kubernetes.io/docs/reference/using-api/deprecation-guide/
//   - kubespray: https://kubespray.io/ + official CNI/CSI release notes
//
// PROHIBITED sources: blogs, Medium, Reddit, Stack Overflow, open GitHub issues.
package rag

import "context"

// Chunk is a unit of indexed documentation with mandatory provider metadata.
// Every chunk MUST have Provider, VersionRange, and SourceURL set.
type Chunk struct {
	ID           string    `json:"id"`
	Content      string    `json:"content"`
	Provider     string    `json:"provider"`      // eks | upstream | kubespray
	VersionRange string    `json:"version_range"` // e.g. "1.27-1.30"
	SourceURL    string    `json:"source_url"`    // must be an approved official URL
	Embedding    []float32 `json:"-"`
}

// QueryRequest is the input to the RAG query pipeline.
type QueryRequest struct {
	// Query is the natural language question derived from a validated Finding.
	Query string `json:"query"`
	// Provider filters retrieval to only chunks matching this provider.
	// Required — prevents cross-provider contamination.
	Provider string `json:"provider"`
	// VersionRange narrows retrieval to the relevant version window.
	VersionRange string `json:"version_range,omitempty"`
	// MaxChunks limits the number of retrieved chunks fed to the LLM.
	MaxChunks int `json:"max_chunks,omitempty"`
}

// QueryResponse is the output of the RAG pipeline.
type QueryResponse struct {
	// Explanation is the LLM-generated translation of the finding into operator language.
	// It MUST be grounded in retrieved chunks — no hallucinated remediation steps.
	Explanation string   `json:"explanation"`
	Sources     []string `json:"sources"` // source URLs of retrieved chunks
}

// RAG is the interface the API layer uses. Implementations must be concurrency-safe.
type RAG interface {
	// Query retrieves relevant chunks for the given provider+version, then asks
	// the LLM to translate/prioritize the finding in operator language.
	Query(ctx context.Context, req *QueryRequest) (*QueryResponse, error)

	// IndexChunk adds a documentation chunk to the vector store.
	// Chunks without Provider, VersionRange, or SourceURL are rejected.
	IndexChunk(ctx context.Context, chunk *Chunk) error
}

// NoopRAG is a no-op implementation returned until the LLM backend is configured.
// It never errors and never hallucinates — it returns empty explanations.
type NoopRAG struct{}

var _ RAG = (*NoopRAG)(nil)

func (n *NoopRAG) Query(_ context.Context, _ *QueryRequest) (*QueryResponse, error) {
	return &QueryResponse{Explanation: "(RAG not configured)"}, nil
}

func (n *NoopRAG) IndexChunk(_ context.Context, _ *Chunk) error {
	return nil
}

// TODO: implement SQLiteRAG using:
//   - github.com/mattn/go-sqlite3 with asg017/sqlite-vec extension loaded via sqlite3_auto_extension
//   - bge-m3 embedding endpoint: POST http://localhost:11434/api/embed (Ollama) or custom
//   - Qwen2.5-Coder-32B LLM endpoint: POST http://localhost:11434/api/generate
//
// Key invariant: retrieval ALWAYS filters WHERE provider = ? before cosine similarity search.
// This prevents EKS-specific chunks from contaminating Kubespray queries.
