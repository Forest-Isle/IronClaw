package config

import "time"

type StoreConfig struct {
	Path string `yaml:"path"`
}

type MemoryConfig struct {
	Enabled               bool                 `yaml:"enabled"`
	StorageType           string               `yaml:"storage_type"` // "file" or "sqlite" (default: "file")
	StorageDir            string               `yaml:"storage_dir"`  // directory for file-based storage (default: ~/.daimon/memory)
	EmbeddingModel        string               `yaml:"embedding_model"`
	EmbeddingBaseURL      string               `yaml:"embedding_base_url"` // base URL for embedding API (default: https://api.openai.com/v1/embeddings)
	OpenAIAPIKey          string               `yaml:"openai_api_key"`
	LocalEmbedding        LocalEmbeddingConfig `yaml:"local_embedding"`        // OpenAI-key-less fallback via a local OpenAI-compatible engine (Ollama)
	SemanticRetrieval     bool                 `yaml:"semantic_retrieval"`     // opt-in world semantic retrieval; embeds each query (API cost) and brute-force scans journal embeddings, suitable for medium scale
	FactExtraction        bool                 `yaml:"fact_extraction"`        // legacy key: enable lifecycle decisions for explicit memory saves
	SimilarityThreshold   float64              `yaml:"similarity_threshold"`   // dedup threshold (default 0.85)
	ConsolidationInterval time.Duration        `yaml:"consolidation_interval"` // session->user promotion interval
	BM25Weight            float64              `yaml:"bm25_weight"`            // BM25 weight in RRF (default 0.4)
	VectorWeight          float64              `yaml:"vector_weight"`          // vector weight in RRF (default 0.6)
	EnableVSS             bool                 `yaml:"enable_vss"`             // enable HNSW indexing via sqlite-vss
	VectorDimension       int                  `yaml:"vector_dimension"`       // embedding dimension (default: 1536)
	EnableSearchCache     bool                 `yaml:"enable_search_cache"`    // enable search result caching
	SearchCacheSize       int                  `yaml:"search_cache_size"`      // max cached queries (default: 500)
	SearchCacheTTL        time.Duration        `yaml:"search_cache_ttl"`       // cache TTL (default: 5min)
	FileStorage           FileStorageConfig    `yaml:"file_storage"`           // file storage specific settings
	RetentionEpisodic     time.Duration        `yaml:"retention_episodic"`     // e.g., "720h" for 30 days
	RetentionSemantic     time.Duration        `yaml:"retention_semantic"`     // e.g., "8760h" for 365 days
	RetentionProcedural   time.Duration        `yaml:"retention_procedural"`   // 0 = never
}

// LocalEmbeddingConfig configures a local OpenAI-compatible embedding engine
// (Ollama) used as fallback when no valid OpenAI API key is set. Opt-in: when
// Enabled and no key is present, the engine is probed (and the model pulled if
// AutoPull) in the background; until ready, embedding degrades to no-op.
type LocalEmbeddingConfig struct {
	Enabled  bool   `yaml:"enabled"`   // default false
	Engine   string `yaml:"engine"`    // "ollama" (default; only engine supported)
	Model    string `yaml:"model"`     // default "nomic-embed-text" (768-dim)
	Host     string `yaml:"host"`      // default "http://localhost:11434"
	AutoPull bool   `yaml:"auto_pull"` // pull model via the running daemon if missing
}

// FileStorageConfig holds file-based storage specific settings.
type FileStorageConfig struct {
	FlushInterval  time.Duration `yaml:"flush_interval"`  // transaction log flush interval (default: 5s)
	ChunkThreshold int           `yaml:"chunk_threshold"` // facts per file before chunking (default: 200)
	Compression    bool          `yaml:"compression"`     // enable gzip compression for large files
}

type ServerConfig struct {
	Addr    string `yaml:"addr"`
	Token   string `yaml:"token"`
	Enabled bool   `yaml:"enabled"`
}

// HealthConfig configures the health check HTTP endpoint.
type HealthConfig struct {
	Port int `yaml:"port"` // default: 9090
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type TelemetryConfig struct {
	Enabled       bool   `yaml:"enabled"`
	TracePath     string `yaml:"trace_path"`
	ReplayEnabled bool   `yaml:"replay_enabled"`
	ReplayDir     string `yaml:"replay_dir"`
}
