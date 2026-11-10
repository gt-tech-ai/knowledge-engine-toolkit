package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// SpyLogger records log calls made through an embedded generated MockLogger, for
// tests that assert which messages were logged — directly and through WithContext
// child loggers. It replaces the former hand-rolled mocks.SpyLogger.
type SpyLogger struct {
	// MockLogger is the embedded generated mock whose expectations back the spy.
	*mocks.MockLogger

	// ChildDebugCalls records Debug messages logged on the WithContext child loggers,
	// so a test can assert the child actually logged (e.g. an observability decorator
	// that binds WithContext(ctx) for trace correlation and logs at Debug).
	ChildDebugCalls *[]string
	// ChildInfoCalls records Info messages logged on the WithContext child loggers.
	ChildInfoCalls *[]string
	// ChildWarnCalls records Warn messages logged on the WithContext child loggers.
	ChildWarnCalls *[]string
	// ChildErrorCalls records Error messages logged on the WithContext child loggers.
	ChildErrorCalls *[]string
	// parentDebugRef is non-nil on a child logger; the child's Debug calls also append
	// to the parent's ChildDebugCalls slice through it.
	parentDebugRef *[]string
	// parentInfoRef is non-nil on a child logger; the child's Info calls also append
	// to the parent's ChildInfoCalls slice through it.
	parentInfoRef *[]string
	// parentWarnRef is non-nil on a child logger; the child's Warn calls also append
	// to the parent's ChildWarnCalls slice through it.
	parentWarnRef *[]string
	// parentErrorRef is non-nil on a child logger; the child's Error calls also append
	// to the parent's ChildErrorCalls slice through it.
	parentErrorRef *[]string
	// DebugCalls records Debug messages logged directly on this logger.
	DebugCalls []string
	// InfoCalls records Info messages logged directly on this logger.
	InfoCalls []string
	// WarnCalls records Warn messages logged directly on this logger.
	WarnCalls []string
	// ErrorCalls records Error messages logged directly on this logger.
	ErrorCalls []string
	// ContextUsed is true when this logger was created by a WithContext call.
	ContextUsed bool
}
