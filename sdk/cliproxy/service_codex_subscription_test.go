package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type subscriptionRoundTrip func(*http.Request) (*http.Response, error)

func (f subscriptionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the executor actually installed by the service, including after reload.
func TestServiceCodexNeverSendsInferenceWithSpendableCredits(t *testing.T) {
	for _, test := range []struct {
		name                      string
		stream, websocket, replay bool
		alt                       string
	}{
		{"response", false, false, false, ""},
		{"stream", true, false, false, ""},
		{"websocket response", false, true, false, ""},
		{"websocket stream", true, true, false, ""},
		{"websocket continuation", true, true, true, ""},
		{"compact", false, false, false, "responses/compact"},
		{"image", false, false, false, "images/generations"},
		{"image stream", true, false, false, "images/generations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
			a := &coreauth.Auth{ID: "subscription-policy", Provider: "codex", Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"}}
			a.Attributes = map[string]string{"websockets": "true"}
			inference := 0
			probes := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", subscriptionRoundTrip(func(r *http.Request) (*http.Response, error) {
				body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[]}}\n\n"
				if r.URL.Path == "/backend-api/wham/usage" {
					probes++
					body = `{"account_id":"test-account","plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0}},"credits":{"has_credits":true,"unlimited":false,"balance":"20000"}}`
				} else {
					inference++
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))
			if test.websocket {
				ctx = exec.WithDownstreamWebsocket(ctx)
			}
			if test.replay {
				ctx = exec.WithRequiredUpstreamWebsocket(ctx)
			}
			for _, reload := range []bool{false, true} {
				s.ensureExecutorsForAuthWithMode(a, reload)
				e, _ := s.coreManager.Executor("codex")
				request := exec.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"test"}`)}
				opts := exec.Options{SourceFormat: translator.FormatOpenAIResponse, Alt: test.alt}
				var err error
				if test.stream {
					_, err = e.ExecuteStream(ctx, a, request, opts)
				} else {
					_, err = e.Execute(ctx, a, request, opts)
				}
				if err == nil || !strings.Contains(err.Error(), "subscription-only") {
					t.Errorf("expected subscription-only refusal, got %v", err)
				}
			}
			if inference != 0 {
				t.Errorf("sent %d billable requests despite spendable credits", inference)
			}
			if probes != 2 {
				t.Errorf("quota probes=%d, want a fresh probe on each attempt", probes)
			}
		})
	}
}

type subscriptionTransportProvider struct{ transport http.RoundTripper }

func (p subscriptionTransportProvider) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return p.transport
}

func TestSubscriptionPolicyThroughPublicResponsesAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, path      string
		stream, credits bool
	}{
		{"responses blocked", "/v1/responses", false, true},
		{"stream blocked", "/v1/responses", true, true},
		{"compact blocked", "/v1/responses/compact", false, true},
		{"subscription allowed", "/v1/responses", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := "gpt-subscription-policy"
			manager := coreauth.NewManager(nil, nil, nil)
			s := &Service{cfg: &config.Config{}, coreManager: manager}
			a := &coreauth.Auth{ID: "public-subscription-policy", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "test-token", "account_id": "account"}}
			if _, err := manager.Register(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			GlobalModelRegistry().RegisterClient(a.ID, "codex", []*ModelInfo{{ID: model}})
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })
			s.ensureExecutorsForAuth(a)
			probes, inference := 0, 0
			manager.SetRoundTripperProvider(subscriptionTransportProvider{subscriptionRoundTrip(func(r *http.Request) (*http.Response, error) {
				var body string
				if r.URL.Path == "/backend-api/wham/usage" {
					probes++
					balance := "0"
					if test.credits {
						balance = "20000"
					}
					body = fmt.Sprintf(`{"account_id":"account","plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0}},"credits":{"has_credits":%t,"unlimited":false,"balance":"%s"}}`, test.credits, balance)
				} else {
					inference++
					body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
			router := gin.New()
			router.POST("/v1/responses", h.Responses)
			router.POST("/v1/responses/compact", h.Compact)
			req := httptest.NewRequest("POST", test.path, strings.NewReader(fmt.Sprintf(`{"model":"%s","input":"test","stream":%t}`, model, test.stream)))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if test.credits {
				if recorder.Code != 429 || !strings.Contains(recorder.Body.String(), "subscription-only") {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if inference != 0 {
					t.Fatalf("sent %d paid-capable inference requests", inference)
				}
			} else if recorder.Code != 200 || inference != 1 {
				t.Fatalf("valid subscription failed: status=%d inference=%d body=%s", recorder.Code, inference, recorder.Body.String())
			}
			if probes != 1 {
				t.Fatalf("probes=%d want 1", probes)
			}
		})
	}
}
