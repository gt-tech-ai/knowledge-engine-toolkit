package storage

// Mockgen directive for the injectable S3 client seam (Config.API), so black-box
// tests can drive the StorageClient through a generated mock instead of a
// hand-written fake — mirroring the SQS client. The destination is go/tests/mocks
// (two levels up).
//go:generate mockgen -destination=../../tests/mocks/mock_s3_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3 S3API

// Mock for the stdlib io.ReadCloser returned by StorageClient.Download, so the
// storage-decorator stream tests can drive a context-aware download body through a
// generated mock instead of a hand-written reader.
//go:generate mockgen -destination=../../tests/mocks/mock_reader.go -package=mocks io ReadCloser
