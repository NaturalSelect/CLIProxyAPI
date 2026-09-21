package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestBuildUsageProbeRequestSelectsOnlySafeProviderModel(t *testing.T) {
	testCases := []struct {
		name          string
		provider      string
		modelIDs      []string
		expectedModel string
		expectedOK    bool
	}{
		{
			name:          "claude selects haiku after other models",
			provider:      "claude",
			modelIDs:      []string{"deepseek-v4-flash", "claude-haiku-4-5"},
			expectedModel: "claude-haiku-4-5",
			expectedOK:    true,
		},
		{
			name:       "claude rejects models without haiku",
			provider:   "claude",
			modelIDs:   []string{"deepseek-v4-flash", "MiniMax-M3"},
			expectedOK: false,
		},
		{
			name:          "codex selects luna after other models",
			provider:      "codex",
			modelIDs:      []string{"gpt-5.6-sol", "gpt-5.6-luna"},
			expectedModel: "gpt-5.6-luna",
			expectedOK:    true,
		},
		{
			name:       "codex rejects models without luna",
			provider:   "codex",
			modelIDs:   []string{"gpt-5.6-sol", "gpt-5.6-codex"},
			expectedOK: false,
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clientID := "usage-refresh-test-" + strings.ReplaceAll(testCase.name, " ", "-")
			models := make([]*registry.ModelInfo, 0, len(testCase.modelIDs))
			for _, modelID := range testCase.modelIDs {
				models = append(models, &registry.ModelInfo{ID: modelID})
			}

			modelRegistry.RegisterClient(clientID, testCase.provider, models)
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(clientID)
			})

			request, _, ok := buildUsageProbeRequest(&Auth{
				ID:       clientID,
				Provider: testCase.provider,
			})
			if ok != testCase.expectedOK {
				t.Fatalf("buildUsageProbeRequest() ok = %t, want %t", ok, testCase.expectedOK)
			}
			if request.Model != testCase.expectedModel {
				t.Fatalf("buildUsageProbeRequest() model = %q, want %q", request.Model, testCase.expectedModel)
			}
		})
	}
}

type usageProbePrepareExecutor struct {
	prepareCalls atomic.Int32
	executeCalls atomic.Int32
	inFlight     atomic.Int32
	maxInFlight  atomic.Int32
}

func (e *usageProbePrepareExecutor) Identifier() string { return "claude" }
func (e *usageProbePrepareExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	inFlight := e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	for {
		maxInFlight := e.maxInFlight.Load()
		if inFlight <= maxInFlight || e.maxInFlight.CompareAndSwap(maxInFlight, inFlight) {
			break
		}
	}
	if prepared, _ := auth.Metadata["prepared"].(bool); prepared {
		e.executeCalls.Add(1)
	}
	return cliproxyexecutor.Response{Headers: http.Header{
		claudeRateLimit5hUtilizationHeader: []string{"10"},
	}}, nil
}
func (e *usageProbePrepareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *usageProbePrepareExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (e *usageProbePrepareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *usageProbePrepareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *usageProbePrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	prepared, _ := auth.Metadata["prepared"].(bool)
	return !prepared
}
func (e *usageProbePrepareExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	updated := auth.Clone()
	updated.Metadata = map[string]any{"prepared": true}
	return updated, nil
}

func TestProbeUsagePreparesAuthBeforeExecuting(t *testing.T) {
	clientID := "usage-probe-prepare"
	registry.GetGlobalRegistry().RegisterClient(clientID, "claude", []*registry.ModelInfo{{ID: "claude-haiku-4-5"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })

	exec := &usageProbePrepareExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.executors["claude"] = exec
	manager.auths[clientID] = &Auth{ID: clientID, Provider: "claude", Metadata: map[string]any{}}

	var probes sync.WaitGroup
	probes.Add(2)
	for range 2 {
		go func() {
			defer probes.Done()
			manager.probeUsage(context.Background(), clientID)
		}()
	}
	probes.Wait()

	if got := exec.prepareCalls.Load(); got != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1", got)
	}
	if got := exec.executeCalls.Load(); got != 1 {
		t.Fatalf("usage probe executions with prepared auth = %d, want 1", got)
	}
	if got := exec.maxInFlight.Load(); got != 1 {
		t.Fatalf("maximum concurrent usage probes = %d, want 1", got)
	}
	current, ok := manager.GetByID(clientID)
	if !ok || current == nil {
		t.Fatal("prepared auth was not saved")
	}
	if prepared, _ := current.Metadata["prepared"].(bool); !prepared {
		t.Fatal("prepared auth metadata was not saved")
	}
}

func TestPickProbeModelReturnsEmptyWithoutPreferredModel(t *testing.T) {
	testCases := []struct {
		name      string
		models    []*registry.ModelInfo
		preferred string
		expected  string
	}{
		{
			name:      "skips nil and empty entries before match",
			models:    []*registry.ModelInfo{nil, {}, {ID: "Claude-HAIKU-4-5"}},
			preferred: "haiku",
			expected:  "Claude-HAIKU-4-5",
		},
		{
			name:      "rejects valid models without preferred substring",
			models:    []*registry.ModelInfo{{ID: "deepseek-v4-flash"}, {ID: "MiniMax-M3"}},
			preferred: "haiku",
			expected:  "",
		},
		{
			name:      "returns empty for invalid entries",
			models:    []*registry.ModelInfo{nil, {}},
			preferred: "luna",
			expected:  "",
		},
		{
			name:      "returns empty for nil list",
			models:    nil,
			preferred: "haiku",
			expected:  "",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := pickProbeModel(testCase.models, testCase.preferred)
			if actual != testCase.expected {
				t.Fatalf("pickProbeModel() = %q, want %q", actual, testCase.expected)
			}
		})
	}
}
