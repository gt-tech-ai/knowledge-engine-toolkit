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

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
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
				return response(http.StatusOK, textNope), nil
			default:
				return response(http.StatusServiceUnavailable, ""), nil
			}
		}).
		AnyTimes()

	dialer := mocks.NewMockDialer(ctrl)
	dialer.EXPECT().DialContext(gomock.Any(), textTCP, gomock.Any()).
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
		Name: "smoke", Inputs: []string{textChecks},
		Steps: []types.ProcStep{{
			Name: "{item.name}", Each: textChecks, Concurrent: true, Probe: probe,
		}},
	}}
	rec := &recorder{out: map[string]string{
		"psql -c select": "3 rows", "psql -c empty": "0 rows",
	}}
	eng := procEngine(t, rec, procs, &procedure.Deps{HTTP: doer, Dialer: dialer})

	testServicesProcedureProbeKindsRest(t, eng)
}

// testServicesProcedureProbeKindsRest continues TestServicesProcedure_ProbeKinds with
// its remaining steps.
func testServicesProcedureProbeKindsRest(t *testing.T, eng interfaces.ProcedureRunner) {
	t.Helper()

	check := func(name string, kv ...any) map[string]any {
		m := map[string]any{
			textName: name, "http": "", textTCP: "", "exec": []any{},
			textStatus: "", "contains": "", "hint": "",
		}
		for i := 0; i+1 < len(kv); i += value2 {
			m[asType[string](t, kv[i])] = kv[i+1]
		}

		return m
	}

	res, err := eng.Run(t.Context(), "smoke", procedure.Vars{textChecks: []any{
		check(textAPI, "http", "http://api/ok", "contains", textOk),
		check("kong", "http", "http://kong/auth", textStatus, text401),
		check(textDown, "http", "http://down/x"),
		check(textBody, "http", "http://body/body", "contains", textOk),
		check(textDB, textTCP, "db:5432"),
		check(textRedis, textTCP, "redis:6379", "hint", "is redis up?"),
		check(textSeed, "exec", []any{textPsql, "-c", "select"}, "contains", "3 rows"),
		check(textEmpty, "exec", []any{textPsql, "-c", textEmpty}, "contains", "3 rows"),
	}}, types.RunOptions{})

	require.NoError(t, err)

	names := make([]string, 0, len(res))
	for i := range res {
		names = append(names, res[i].Name)
	}

	assert.Equal(
		t,
		[]string{
			textAPI,
			"kong",
			textDown,
			textBody,
			textDB,
			textRedis,
			textSeed,
			textEmpty,
		},
		names,
	)

	st := statuses(res)
	for _, pass := range []string{textAPI, "kong", textDB, textSeed} {
		assert.Equal(t, types.StatusPass, st[pass], pass)
	}

	for _, fail := range []string{textDown, textBody, textRedis, textEmpty} {
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
			if r.URL.Host == textLate && attempts.Add(1) >= 3 {
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
			Name:  textLate,
			Steps: []types.ProcStep{{Name: textLate, Probe: retry("http://late/")}},
		},
		{Name: textDead, Steps: []types.ProcStep{{Name: textDead, Probe: &types.ProcProbe{
			HTTP: "http://dead/", Timeout: "30ms", Delay: "1ms", MaxDelay: "2ms",
		}}}},
		{Name: textNone, Inputs: []string{textURL}, Steps: []types.ProcStep{{
			Name: textNone, Probe: &types.ProcProbe{HTTP: "{url}"},
		}}},
		{Name: textTwo, Steps: []types.ProcStep{{
			Name: textTwo, Probe: &types.ProcProbe{HTTP: "http://a/", TCP: "a:1"},
		}}},
	}
	eng := procEngine(t, &recorder{}, procs, &procedure.Deps{HTTP: doer})

	res, err := eng.Run(t.Context(), textLate, nil, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, statuses(res)[textLate])
	assert.Equal(t, int32(int32Arg3), attempts.Load())

	res, err = eng.Run(t.Context(), textDead, nil, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, statuses(res)[textDead])
	assert.Contains(t, res[0].Error, "status 502")

	for _, name := range []string{textNone, textTwo} {
		res, err = eng.Run(
			t.Context(),
			name,
			procedure.Vars{textURL: ""},
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
		"probe and tool": {Name: textX, Tool: textEcho, Probe: &types.ProcProbe{TCP: "a:1"}},
		"empty probe":    {Name: textX, Probe: &types.ProcProbe{Contains: textOk}},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			{Name: textP, Steps: []types.ProcStep{st}},
		}}, &procedure.Deps{})
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}
