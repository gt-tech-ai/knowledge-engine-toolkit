package awssm

// Mock generation for the Secrets Manager SDK-adapter seam. The API seam is EXPORTED so
// its mock lives in the external go/tests/mocks tree — never in-package — and a black-box
// test (go/tests/unit) drives Get's JSON-field extraction / not-found behavior through
// the exported New constructor without network access.

//go:generate mockgen -destination=../../../tests/mocks/mock_secretsmanager_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/awssm SecretsManagerAPI
