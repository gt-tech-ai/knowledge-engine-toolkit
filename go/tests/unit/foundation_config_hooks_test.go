package unit_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfgviper "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/viper"
)

// TestStringToStringMapHookFunc tests the flat-string → map[string]string decode
// hook that lets a config map field be populated from a single scalar env var.
//
// Why this test is important:
//   - A Kubernetes secretKeyRef can only inject a scalar, so the deploy path
//     delivers auth.service_tokens as one flat "caller=token;caller=token" string.
//     The hook is the sole bridge from that scalar to the map the validator reads;
//     its entry/kv-splitting, rotation-pair preservation, and skip rules are the
//     contract the deploy format is built on.
//
// What it tests:
//   - well-formed entries decode; a value's embedded "," (rotation pair) survives;
//     whitespace is trimmed; empty and malformed (no "=") entries are skipped; an
//     empty string yields an empty non-nil map; a non-map target passes through.
func TestStringToStringMapHookFunc(t *testing.T) {
	strType := reflect.TypeFor[string]()
	mapType := reflect.TypeFor[map[string]string]()
	hook := cfgviper.StringToStringMapHookFunc()

	tests := []struct {
		from reflect.Type
		to   reflect.Type
		in   any
		want any
		name string
	}{
		{
			name: "well-formed entries with a rotation pair preserved",
			from: strType, to: mapType,
			in:   "reports=rcur,rprev;orders=otok",
			want: map[string]string{"reports": "rcur,rprev", "orders": "otok"},
		},
		{
			name: "whitespace around entries and keys/values is trimmed",
			from: strType, to: mapType,
			in:   " orders = otok ; reports = rtok ",
			want: map[string]string{"orders": "otok", "reports": "rtok"},
		},
		{
			name: "empty and malformed (no '=') entries are skipped",
			from: strType, to: mapType,
			in:   "orders=otok;;garbage;=noKey;noValue=",
			want: map[string]string{"orders": "otok", "": "noKey", "noValue": ""},
		},
		{
			name: "empty string yields an empty non-nil map",
			from: strType, to: mapType,
			in:   "",
			want: map[string]string{},
		},
		{
			name: "non-map target passes through untouched",
			from: strType, to: reflect.TypeFor[[]string](),
			in:   "a=1;b=2",
			want: "a=1;b=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hook(tt.from, tt.to, tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
