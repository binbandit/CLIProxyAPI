package helps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const safeSubscription = `{"account_id":"account","plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":42},"secondary_window":{"used_percent":80}},"credits":{"has_credits":false,"unlimited":false,"balance":"0"}}`

func TestCodexSubscriptionDecision(t *testing.T) {
	tests := []struct {
		name, old, new string
		allow          bool
	}{
		{"included allowance", "", "", true},
		{"numeric zero", `"balance":"0"`, `"balance":0`, true},
		{"plus", `"pro"`, `"plus"`, true},
		{"spendable credits", `"has_credits":false`, `"has_credits":true`, false},
		{"unlimited credits", `"unlimited":false`, `"unlimited":true`, false},
		{"contradictory balance", `"balance":"0"`, `"balance":"0.01"`, false},
		{"negative balance", `"balance":"0"`, `"balance":"-1"`, false},
		{"unknown balance", `"balance":"0"`, `"balance":null`, false},
		{"missing credit flag", `"has_credits":false,`, ``, false},
		{"missing unlimited flag", `"unlimited":false,`, ``, false},
		{"primary exhausted", `"used_percent":42`, `"used_percent":100`, false},
		{"weekly exhausted", `"used_percent":80`, `"used_percent":100`, false},
		{"missing window value", `"used_percent":42`, `"other":42`, false},
		{"missing primary", `"primary_window":{"used_percent":42}`, `"primary_window":null`, false},
		{"negative usage", `"used_percent":42`, `"used_percent":-1`, false},
		{"not allowed", `"allowed":true`, `"allowed":false`, false},
		{"missing allowed", `"allowed":true,`, ``, false},
		{"limit reached", `"limit_reached":false`, `"limit_reached":true`, false},
		{"missing reached", `"limit_reached":false,`, ``, false},
		{"wrong account", `"account_id":"account"`, `"account_id":"other"`, false},
		{"credit based plan", `"pro"`, `"business"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := safeSubscription
			if tt.old != "" {
				body = strings.Replace(body, tt.old, tt.new, 1)
			}
			err := ValidateCodexSubscription([]byte(body), "account")
			if (err == nil) != tt.allow {
				t.Fatalf("allow=%v error=%v", tt.allow, err)
			}
		})
	}
	for _, body := range []string{"{}", "null", "invalid", safeSubscription + "{}"} {
		if ValidateCodexSubscription([]byte(body), "account") == nil {
			t.Fatalf("accepted unknown usage %s", body)
		}
	}
	var usage map[string]any
	_ = json.Unmarshal([]byte(safeSubscription), &usage)
	usage["additional_rate_limits"] = []any{map[string]any{"rate_limit": map[string]any{"allowed": false, "limit_reached": true}}}
	body, _ := json.Marshal(usage)
	if ValidateCodexSubscription(body, "account") == nil {
		t.Fatal("accepted exhausted additional limit")
	}
}

type subscriptionTransport func(*http.Request) (*http.Response, error)

func (f subscriptionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexSubscriptionProbeFailsClosedAndNeverCaches(t *testing.T) {
	a := &auth.Auth{Metadata: map[string]any{"access_token": "secret-token", "account_id": "account"}}
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		failure bool
		allow   bool
	}{
		{"allowed", 200, safeSubscription, false, true},
		{"unauthorized", 401, safeSubscription, false, false},
		{"redirect", 302, "", false, false},
		{"unavailable", 503, "", false, false},
		{"malformed", 200, "{}", false, false},
		{"oversize", 200, strings.Repeat(" ", 1<<20) + safeSubscription, false, false},
		{"network failure", 0, "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", subscriptionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != CodexUsageURL || r.Method != "GET" || r.Header.Get("Chatgpt-Account-Id") != "account" || r.Header.Get("Authorization") != "Bearer secret-token" {
					t.Fatalf("incorrect probe identity/destination")
				}
				if tt.failure {
					return nil, errors.New("sensitive network detail secret-token")
				}
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Location": []string{"https://example.com/"}}, Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
			}))
			for range 2 {
				err := CheckCodexSubscription(ctx, nil, a)
				if (err == nil) != tt.allow {
					t.Fatalf("allow=%v err=%v", tt.allow, err)
				}
				if err != nil && strings.Contains(err.Error(), "secret-token") {
					t.Fatal("leaked credentials")
				}
			}
			if calls != 2 {
				t.Fatalf("probes=%d; want 2, with no caching or redirects", calls)
			}
		})
	}
}

func TestCodexSubscriptionRejectsPaidCredentialRoutes(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"api_key": "key"}, {"base_url": "https://api.openai.com/v1"}, {"use_siwc": "true"},
		{"header:Authorization": "other"}, {"header:chatgpt-account-id": "other"}, {"header:Host": "other"},
	} {
		a := &auth.Auth{Attributes: attrs, Metadata: map[string]any{"access_token": "token", "account_id": "account"}}
		calls := 0
		ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", subscriptionTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") }))
		if CheckCodexSubscription(ctx, nil, a) == nil || calls != 0 {
			t.Fatalf("paid credential route was not blocked before network: %v", attrs)
		}
	}
}

func TestCodexSubscriptionGenericForwarding(t *testing.T) {
	for _, tt := range []struct {
		method, url, host string
		allow             bool
	}{
		{"GET", CodexUsageURL, "", true},
		{"GET", "https://chatgpt.com/backend-api/codex/models?client_version=0.154.0", "", true},
		{"POST", CodexUsageURL, "", false},
		{"POST", "https://chatgpt.com/backend-api/codex/responses", "", false},
		{"GET", "https://chatgpt.com/backend-api/codex/responses", "", false},
		{"GET", CodexUsageURL, "api.openai.com", false},
		{"GET", "https://chatgpt.com.evil.invalid/backend-api/wham/usage", "", false},
		{"GET", "http://chatgpt.com/backend-api/wham/usage", "", false},
		{"GET", "https://user@chatgpt.com/backend-api/wham/usage", "", false},
	} {
		r, _ := http.NewRequest(tt.method, tt.url, nil)
		if tt.host != "" {
			r.Host = tt.host
		}
		if got := CodexSubscriptionReadOnly(r); got != tt.allow {
			t.Errorf("%s %s host=%s allow=%v", tt.method, tt.url, tt.host, got)
		}
	}
}
