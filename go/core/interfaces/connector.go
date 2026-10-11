package interfaces

import "context"

// ConnectorSource is the connector-family read seam any connector type satisfies — an
// object store now (S3), a record system (Salesforce) or a file SaaS (GDrive/SharePoint)
// later. It is deliberately transport-agnostic and AWS-free so it lives in
// dependency-free core; the concrete S3/OAuth clients live in
// go/clients/connector/<type>. It exposes the two reads a connector needs: a bounded
// reachability probe (TestConnection, e.g. to validate newly entered credentials) and the
// resumable, memory-bounded page-by-page listing a sync process crawls (ListPage). It
// does NOT fetch or normalize object BODIES — parsing, embedding and ingesting them is
// the caller's pipeline.
type ConnectorSource interface {
	// TestConnection probes the source with the connector's credentials and returns a
	// bounded count of reachable items (e.g. objects under an S3 bucket/prefix), or a
	// coded error when the source is unreachable or the credentials are rejected.
	TestConnection(ctx context.Context) (int, error)

	// ListPage streams one page of the source's objects starting AFTER continuationToken
	// (empty = the start of the listing) and returns the page plus the token to resume
	// from ("" when the listing is exhausted). It is the resumable, memory-bounded
	// primitive a sync process crawls a large source with — holding one page at a time
	// (never the full key list) and checkpointing the token so a crashed sync resumes
	// from the last committed page rather than restarting a million-object crawl. A
	// non-positive limit lets the backend apply its default page size. A failure is a
	// coded error (transient CodeUnavailable vs terminal CodeInvalidInput) so the caller
	// can classify retry-vs-fail.
	ListPage(
		ctx context.Context,
		continuationToken string,
		limit int,
	) (objects []StorageObject, nextToken string, err error)
}

// ConnectorKind selects a connector backend (the family discriminator), mirroring the
// storage tier's Kind pattern. S3 now; gdrive/salesforce/… are added as new kinds — a
// config change, not an edit to the builder's callers or a sync engine.
type ConnectorKind string

// ConnectorKindS3 is the S3-compatible object-store connector (the first — and, for now,
// only — kind).
const ConnectorKindS3 ConnectorKind = "s3"

// SourceConfig is the primitive, domain-free, AWS-free description a SourceBuilder turns
// into a ConnectorSource. The consumer maps its own connector record + decrypted
// credentials onto this at its composition root, so this package never imports consumer
// types. Its credential fields are already-decrypted plaintext (the consumer owns
// encryption-at-rest); never log them.
type SourceConfig struct {
	// Kind selects the connector backend.
	Kind ConnectorKind
	// Bucket names the source object-store bucket (S3 kind).
	Bucket string
	// Prefix restricts the listing to objects under this key prefix (S3 kind).
	Prefix string
	// Region is the connector's bucket region (S3 kind); empty falls back to the client
	// default. The object-store ENDPOINT is deliberately NOT here — it is app-infra (dev
	// MinIO), captured by the composition root's client builder, not a per-connector
	// value.
	Region string
	// AuthMethod selects the credential strategy: "iam_role" (STS AssumeRole) or
	// "access_key" (static).
	AuthMethod string
	// IAMRoleARN is the role assumed for auth_method=iam_role.
	IAMRoleARN string
	// ExternalID is the STS ExternalId sent on every AssumeRole for auth_method=iam_role
	// (required there). The role's trust policy requires it, which ties the role to the
	// tenant that owns it: without it, a tenant who registers another tenant's role ARN
	// could have the platform assume it for them (the confused-deputy problem). The
	// consumer's platform generates it per tenant and stores it with the connector; it is
	// never taken from tenant input, or a tenant could present another tenant's role ARN
	// together with that tenant's id. Format (STS's rule): 2–1224 characters of
	// [\w+=,.@:/-]; the S3 builder rejects anything else with CodeInvalidInput.
	ExternalID string
	// AccessKeyID is the access key id for auth_method=access_key.
	AccessKeyID string
	// SecretAccessKey is the secret for auth_method=access_key (already-decrypted
	// plaintext; never log it).
	SecretAccessKey string
}

// SourceBuilder builds a ConnectorSource from a SourceConfig, failing loudly on an
// unknown kind (the foundation/logger NewFromConfig pattern). It is the seam the
// connector service depends on and the composition root injects; the concrete builder
// lives in go/clients/connector.
type SourceBuilder interface {
	// Build constructs the ConnectorSource for cfg.Kind, or returns a coded error on an
	// unknown kind or a construction failure.
	Build(ctx context.Context, cfg SourceConfig) (ConnectorSource, error)
}
