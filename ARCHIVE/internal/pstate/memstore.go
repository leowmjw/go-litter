package pstate

// Refactor out common code for each store type ..
// Different PStore has different behaviors that map to diff store:
// All persisted to S3-like storage; some might be sqlite or redis etc
// For slow store; maybe even can be direct parquet or iceberg via DuckDB?
// Or vene graph if needed?

// $$profiles, $$friends, $$posts (subindexed inner maps), $$profileViews

// MemStore implementation of Store of each PState
