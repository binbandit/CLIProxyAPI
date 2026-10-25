package helps

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const CodexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// CodexSubscriptionError is account-scoped: the manager may try another eligible
// credential, but must never retry this account by falling back to paid credits.
type CodexSubscriptionError struct{ Reason string }

func (e *CodexSubscriptionError) Error() string              { return "codex subscription-only: " + e.Reason }
func (e *CodexSubscriptionError) StatusCode() int            { return http.StatusTooManyRequests }
func (e *CodexSubscriptionError) RetryAfter() *time.Duration { d := time.Minute; return &d }

func subscriptionDenied(reason string) error { return &CodexSubscriptionError{Reason: reason} }

// CodexSubscriptionReadOnly permits only known, non-inference discovery routes.
// In particular, generic HTTP forwarding must not bypass the inference gate.
func CodexSubscriptionReadOnly(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "chatgpt.com" || r.URL.User != nil {
		return false
	}
	if r.Host != "" && r.Host != "chatgpt.com" {
		return false
	}
	if r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
		return false
	}
	switch r.URL.Path {
	case "/backend-api/wham/usage", "/backend-api/codex/models", "/backend-api/subscriptions", "/backend-api/wham/rate-limit-reset-credits":
		return true
	default:
		return false
	}
}

// CheckCodexSubscription deliberately does not cache a successful check. The
// allowance is shared with other devices and applications. A balance check is
// conservative: OpenAI exposes no documented per-request no-credit guarantee,
// so an account with spendable credits is refused even before its plan runs out.
func CheckCodexSubscription(ctx context.Context, cfg *config.Config, a *auth.Auth) error {
	if a == nil {
		return subscriptionDenied("OAuth subscription credentials are required")
	}
	if strings.TrimSpace(a.Attributes["api_key"]) != "" {
		return subscriptionDenied("API-key billing is prohibited")
	}
	if base := strings.TrimRight(strings.TrimSpace(a.Attributes["base_url"]), "/"); base != "" && base != "https://chatgpt.com/backend-api/codex" {
		return subscriptionDenied("custom billing endpoints are prohibited")
	}
	for key := range a.Attributes {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(lower, "header:") {
			switch strings.TrimSpace(strings.TrimPrefix(lower, "header:")) {
			case "authorization", "chatgpt-account-id", "host":
				return subscriptionDenied("credential header overrides are prohibited")
			}
		}
	}
	for _, value := range []any{a.Attributes["use_siwc"], a.Metadata["use_siwc"]} {
		enabled := false
		switch v := value.(type) {
		case bool:
			enabled = v
		case string:
			enabled, _ = strconv.ParseBool(strings.TrimSpace(v))
		}
		if enabled {
			return subscriptionDenied("SIWC billing is not verified as subscription-only")
		}
	}
	token, _ := a.Metadata["access_token"].(string)
	account, _ := a.Metadata["account_id"].(string)
	if strings.TrimSpace(token) == "" || strings.TrimSpace(account) == "" {
		return subscriptionDenied("OAuth subscription identity is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, CodexUsageURL, nil)
	if err != nil {
		return subscriptionDenied("could not construct the allowance check")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Chatgpt-Account-Id", account)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "codex-cli")
	client := NewUtlsHTTPClient(ctx, cfg, a, 0)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return subscriptionDenied("subscription allowance could not be verified; request blocked")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return subscriptionDenied("subscription allowance check failed; request blocked")
	}
	const maxUsageBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUsageBytes+1))
	if err != nil || len(body) > maxUsageBytes {
		return subscriptionDenied("subscription allowance response is unreadable")
	}
	return ValidateCodexSubscription(body, account)
}

type subscriptionWindow struct {
	UsedPercent *float64 `json:"used_percent"`
}
type subscriptionLimit struct {
	Allowed   *bool               `json:"allowed"`
	Reached   *bool               `json:"limit_reached"`
	Primary   *subscriptionWindow `json:"primary_window"`
	Secondary *subscriptionWindow `json:"secondary_window"`
}

// ValidateCodexSubscription treats missing or contradictory quota fields as
// unknown, never as zero usage or permission to use a credit balance.
func ValidateCodexSubscription(body []byte, account string) error {
	var usage struct {
		Account    string             `json:"account_id"`
		Plan       string             `json:"plan_type"`
		Limit      *subscriptionLimit `json:"rate_limit"`
		Additional []struct {
			Limit *subscriptionLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
		Credits *struct {
			HasCredits *bool           `json:"has_credits"`
			Unlimited  *bool           `json:"unlimited"`
			Balance    json.RawMessage `json:"balance"`
		} `json:"credits"`
	}
	if json.Unmarshal(body, &usage) != nil || usage.Account != account || account == "" {
		return subscriptionDenied("subscription identity or usage response is unverified")
	}
	if usage.Plan != "plus" && usage.Plan != "pro" {
		return subscriptionDenied("an included Plus or Pro subscription allowance is required")
	}
	c := usage.Credits
	if c == nil || c.HasCredits == nil || c.Unlimited == nil {
		return subscriptionDenied("credit availability is unknown")
	}
	if *c.HasCredits || *c.Unlimited {
		return subscriptionDenied("account has spendable credits; this connection cannot guarantee that they will not be used")
	}
	balance := strings.TrimSpace(string(c.Balance))
	if strings.HasPrefix(balance, "\"") {
		if json.Unmarshal(c.Balance, &balance) != nil {
			return subscriptionDenied("credit balance is unknown")
		}
	}
	amount, ok := new(big.Rat).SetString(balance)
	if !ok || amount.Sign() != 0 {
		return subscriptionDenied("a verified zero credit balance is required")
	}
	if !subscriptionLimitAvailable(usage.Limit) {
		return subscriptionDenied("included subscription allowance is exhausted or unknown")
	}
	for _, extra := range usage.Additional {
		if !subscriptionLimitAvailable(extra.Limit) {
			return subscriptionDenied("an additional subscription allowance is exhausted or unknown")
		}
	}
	return nil
}

func subscriptionLimitAvailable(limit *subscriptionLimit) bool {
	if limit == nil || limit.Allowed == nil || !*limit.Allowed || limit.Reached == nil || *limit.Reached || limit.Primary == nil {
		return false
	}
	for _, window := range []*subscriptionWindow{limit.Primary, limit.Secondary} {
		if window == nil {
			continue
		}
		if window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 || *window.UsedPercent >= 100 {
			return false
		}
	}
	return true
}
