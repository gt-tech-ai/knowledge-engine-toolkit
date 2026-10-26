package unit_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/procedure"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// response is an HTTP response with status code and body.
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
}

// TestServicesProcedure_ProbeKinds tests the probe step's three kinds over a mixed list.
//
// Why this test is important:
//   - A health check over a stack is a list of mixed checks (an HTTP endpoint, a port, a
//     query run in a container); one probe row over the list must run each item as its
//     own kind and judge it by its own expectations, or a smoke run passes a dead
//     service.
//
// What it tests:
//   - One concurrent Each row probes an HTTP item (status and body checked), a TCP item
//     and an Exec item (stdout checked), one row per item in the items' order.
//   - An HTTP item with no status accepts a 2xx and fails a 503; an expected 401
//     passes; a body without Contains, an Exec stdout without Contains, and a refused
//     TCP connection fail with the hint appended.
func TestServicesProcedure_ProbeKinds(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	doer := mocks.NewMockHTTPDoer(ctrl)
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/ok":
				return response(http.StatusOK, "status: ok"), nil
			case "/auth":
				return response(http.StatusUnauthorized, ""), nil
			case "/body":
				return response(http.StatusOK, "nope"), nil
			default:
				return response(http.StatusServiceUnavailable, ""), nil
			}
		}).
		AnyTimes()
	dialer := mocks.NewMockDialer(ctrl)
	dialer.EXPECT().DialContext(gomock.Any(), "tcp", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr == "db:5432" {
				client, server := net.Pipe()
				_ = server.Close()
				return client, nil
			}
			return nil, apperr.Sentinel("connection refused")
		}).AnyTimes()
	probe := &types.ProcProbe{
		HTTP: "{item.http}", TCP: "{item.tcp}", Exec: []string{"{item.exec...}"},
		Status: "{item.status}", Contains: "{item.contains}", Hint: "{item.hint}",
	}
	procs := []types.Procedure{{
		Name: "smoke", Inputs: []string{"checks"},
		Steps: []types.ProcStep{{
			Name: "{item.name}", Each: "checks", Concurrent: true, Probe: probe,
		}},
	}}
	rec := &recorder{out: map[string]string{
		"psql -c select": "3 rows", "psql -c empty": "0 rows",
	}}
	eng := procEngine(t, rec, procs, procedure.Deps{HTTP: doer, Dialer: dialer})
	check := func(name string, kv ...any) map[string]any {
		m := map[string]any{
			"name": name, "http": "", "tcp": "", "exec": []any{},
			"status": "", "contains": "", "hint": "",
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}

	res, err := eng.Run(t.Context(), "smoke", procedure.Vars{"checks": []any{
		check("api", "http", "http://api/ok", "contains", "ok"),
		check("kong", "http", "http://kong/auth", "status", "401"),
		check("down", "http", "http://down/x"),
		check("body", "http", "http://body/body", "contains", "ok"),
		check("db", "tcp", "db:5432"),
		check("redis", "tcp", "redis:6379", "hint", "is redis up?"),
		check("seed", "exec", []any{"psql", "-c", "select"}, "contains", "3 rows"),
		check("empty", "exec", []any{"psql", "-c", "empty"}, "contains", "3 rows"),
	}}, types.RunOptions{})

	require.NoError(t, err)
	names := make([]string, 0, len(res))
	for i := range res {
		names = append(names, res[i].Name)
	}
	assert.Equal(
		t,
		[]string{"api", "kong", "down", "body", "db", "redis", "seed", "empty"},
		names,
	)
	st := statuses(res)
	for _, pass := range []string{"api", "kong", "db", "seed"} {
		assert.Equal(t, types.StatusPass, st[pass], pass)
	}
	for _, fail := range []string{"down", "body", "redis", "empty"} {
		assert.Equal(t, types.StatusFail, st[fail], fail)
	}
	assert.Contains(t, res[2].Error, "status 503")
	assert.Contains(t, res[3].Error, `does not contain "ok"`)
	assert.Contains(t, res[5].Error, "is redis up?")
	assert.Contains(t, res[7].Error, `does not contain "3 rows"`)
}

// TestServicesProcedure_ProbeRetriesUntilTimeout tests the probe's retry loop.
//
// Why this test is important:
//   - A stack that was just started answers only after a while; a probe that gives up on
//     the first refusal fails a healthy start, and one that never gives up hangs the run.
//
// What it tests:
//   - A probe that fails twice and then passes is one passing row after three attempts.
//   - A probe that never passes fails once its timeout ends, reporting the last error.
//   - A probe naming no kind, or two, fails its row with CodeInvalidInput text.
func TestServicesProcedure_ProbeRetriesUntilTimeout(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	var attempts atomic.Int32
	doer := mocks.NewMockHTTPDoer(ctrl)
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "late" && attempts.Add(1) >= 3 {
				return response(http.StatusOK, ""), nil
			}
			return response(http.StatusBadGateway, ""), nil
		}).
		AnyTimes()
	retry := func(url string) *types.ProcProbe {
		return &types.ProcProbe{HTTP: url, Timeout: "2s", Delay: "1ms", MaxDelay: "2ms"}
	}
	procs := []types.Procedure{
		{
			Name:  "late",
			Steps: []types.ProcStep{{Name: "late", Probe: retry("http://late/")}},
		},
		{Name: "dead", Steps: []types.ProcStep{{Name: "dead", Probe: &types.ProcProbe{
			HTTP: "http://dead/", Timeout: "30ms", Delay: "1ms", MaxDelay: "2ms",
		}}}},
		{Name: "none", Inputs: []string{"url"}, Steps: []types.ProcStep{{
			Name: "none", Probe: &types.ProcProbe{HTTP: "{url}"},
		}}},
		{Name: "two", Steps: []types.ProcStep{{
			Name: "two", Probe: &types.ProcProbe{HTTP: "http://a/", TCP: "a:1"},
		}}},
	}
	eng := procEngine(t, &recorder{}, procs, procedure.Deps{HTTP: doer})

	res, err := eng.Run(t.Context(), "late", nil, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, statuses(res)["late"])
	assert.Equal(t, int32(3), attempts.Load())

	res, err = eng.Run(t.Context(), "dead", nil, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, statuses(res)["dead"])
	assert.Contains(t, res[0].Error, "status 502")

	for _, name := range []string{"none", "two"} {
		res, err = eng.Run(
			t.Context(),
			name,
			procedure.Vars{"url": ""},
			types.RunOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, types.StatusFail, statuses(res)[name], name)
		assert.Contains(t, res[0].Error, "exactly one of http, tcp, exec", name)
	}
}

// TestServicesProcedure_ProbeIsAStepKind tests that compile treats probe as a step kind.
//
// Why this test is important:
//   - A row that both probes and runs a tool is ambiguous; refusing it at build keeps a
//     typo from silently dropping one of the two.
//
// What it tests:
//   - A step with both probe and tool fails NewFromConfig with CodeInvalidInput; a probe
//     with no kind field at all fails the same way.
func TestServicesProcedure_ProbeIsAStepKind(t *testing.T) {
	t.Parallel()
	for name, st := range map[string]types.ProcStep{
		"probe and tool": {Name: "x", Tool: "echo", Probe: &types.ProcProbe{TCP: "a:1"}},
		"empty probe":    {Name: "x", Probe: &types.ProcProbe{Contains: "ok"}},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			{Name: "p", Steps: []types.ProcStep{st}},
		}}, procedure.Deps{})
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}
