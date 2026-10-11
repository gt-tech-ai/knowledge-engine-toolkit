package validator

import (
	"reflect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Ensure runs Validate() on each DTO in order and returns the first coded violation, or
// nil when all pass. It is the hand-written-service chokepoint: call it at a service or
// workflow entry to enforce a DTO's self-validation — the generated Validate() composed
// with its optional domainRules() hook — below the transport edge, so non-edge writers
// (seed, connector sync, internal RPC) and server-injected fields are validated as
// CodeInvalidInput just like edge requests.
//
// A nil entry — an untyped nil OR a typed nil pointer (e.g. a nil *SomeDTO passed as an
// optional Validatable) — is skipped, so an unset optional DTO never panics in
// Validate(). Callers that must aggregate every violation (e.g. a bulk request reporting
// all offenders in one response) run each DTO's Validate() themselves rather than
// short-circuiting here.
func Ensure(dtos ...interfaces.Validatable) error {
	for _, dto := range dtos {
		if isNil(dto) {
			continue
		}
		if err := dto.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// isNil reports whether a Validatable is nil — either the untyped nil interface or a
// typed nil pointer/map/chan/func/slice, whose Validate() would dereference nil and
// panic.
func isNil(v interfaces.Validatable) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
