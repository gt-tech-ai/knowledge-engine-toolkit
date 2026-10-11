// Package integration holds the Go integration tests: each file exercises a
// clients-tier backend against the real service it wraps, started in a throwaway
// testcontainers container by the fixtures under go/tests/fixtures/dbtest —
// Redis (cache, lock, messaging, replay buffer), MinIO (S3 storage and the S3
// connector), ElasticMQ (SQS messaging), and PostgreSQL (River's runtime and
// migrations). The package is flat; files are named after the tier they cover.
//
// Integration tests for a consumer's own schema or ORM layer belong with that
// consumer.
//
// All tests require the build tag "integration" and a Docker daemon.
// Run with: INTEGRATION=1 go test -tags=integration ./tests/integration/...
package integration
