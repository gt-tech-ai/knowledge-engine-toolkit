package sqs

// Mockgen directive for the injectable SQS client seam (Config.API), so black-box
// tests can drive the publisher/subscriber through a generated mock instead of a
// hand-written fake. The destination is go/tests/mocks (three levels up).
//go:generate mockgen -destination=../../../tests/mocks/mock_sqs_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs API
