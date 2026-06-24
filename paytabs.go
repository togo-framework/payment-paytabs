// Package paytabs is a PayTabs driver for togo payment. Blank-import it and set
// PAYMENT_DRIVER=paytabs, PAYTABS_PROFILE_ID, PAYTABS_SERVER_KEY, PAYTABS_REGION.
// Hosted checkout (/payment/request, tran_type=sale, tran_class=ecom), token
// charges, refunds (tran_type=refund) and IPN webhooks are implemented against
// the PayTabs API (https://support.paytabs.com).
package paytabs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/togo-framework/payment"
	"github.com/togo-framework/togo"
)

// regionHosts maps a PAYTABS_REGION to its API host (default: global).
var regionHosts = map[string]string{
	"ARE":    "https://secure.paytabs.com",
	"SAU":    "https://secure.paytabs.sa",
	"EGY":    "https://secure-egypt.paytabs.com",
	"OMN":    "https://secure-oman.paytabs.com",
	"JOR":    "https://secure-jordan.paytabs.com",
	"GLOBAL": "https://secure-global.paytabs.com",
}

func init() {
	payment.RegisterDriver("paytabs", func(k *togo.Kernel) (payment.PaymentProvider, error) {
		profile := os.Getenv("PAYTABS_PROFILE_ID")
		key := os.Getenv("PAYTABS_SERVER_KEY")
		if profile == "" || key == "" {
			return nil, errors.New("payment-paytabs: set PAYTABS_PROFILE_ID and PAYTABS_SERVER_KEY")
		}
		host := regionHosts[strings.ToUpper(os.Getenv("PAYTABS_REGION"))]
		if host == "" {
			host = "https://secure.paytabs.com"
		}
		return &provider{profile: profile, key: key, host: host, hc: &http.Client{Timeout: 20 * time.Second}}, nil
	})
}

type provider struct {
	profile string
	key     string
	host    string
	hc      *http.Client
}

// post sends a JSON request to the PayTabs API with the server-key auth header.
func (p *provider) post(ctx context.Context, path string, payload map[string]any) (map[string]any, error) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.host+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", p.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if resp.StatusCode >= 300 {
		return m, fmt.Errorf("payment-paytabs: %s %d: %s", path, resp.StatusCode, string(b))
	}
	return m, nil
}

// major converts the smallest-unit Money amount (e.g. cents) to PayTabs' decimal.
func major(m payment.Money) float64 { return float64(m.Amount) / 100.0 }

func (p *provider) request(ctx context.Context, tranType string, amount payment.Money, c payment.Customer, meta map[string]string, successURL, token string) (map[string]any, error) {
	body := map[string]any{
		"profile_id":       p.profile,
		"tran_type":        tranType,
		"tran_class":       "ecom",
		"cart_id":          fmt.Sprintf("cart-%d", time.Now().UnixNano()),
		"cart_currency":    strings.ToUpper(amount.Currency),
		"cart_amount":      major(amount),
		"cart_description": firstNonEmpty(meta["description"], "togo payment"),
	}
	if successURL != "" {
		body["return"] = successURL
	}
	if cb := meta["callback"]; cb != "" {
		body["callback"] = cb
	}
	if c.Email != "" || c.Name != "" {
		body["customer_details"] = map[string]any{"name": c.Name, "email": c.Email}
	}
	if token != "" {
		body["payment_token"] = token
	}
	return p.post(ctx, "/payment/request", body)
}

func (p *provider) CreateCharge(ctx context.Context, r payment.ChargeRequest) (*payment.Charge, error) {
	if r.Token == "" {
		return nil, errors.New("payment-paytabs: CreateCharge needs a payment_token; use CreateCheckoutSession for the hosted page")
	}
	m, err := p.request(ctx, "sale", r.Amount, r.Customer, r.Metadata, "", r.Token)
	if err != nil {
		return nil, err
	}
	ref, _ := m["tran_ref"].(string)
	status := "pending"
	if pr, ok := m["payment_result"].(map[string]any); ok {
		if rs, _ := pr["response_status"].(string); rs == "A" {
			status = "succeeded"
		} else if rs != "" {
			status = "failed"
		}
	}
	return &payment.Charge{ID: ref, Status: status, Amount: r.Amount, Provider: "paytabs", Raw: m}, nil
}

func (p *provider) CreateCheckoutSession(ctx context.Context, r payment.CheckoutRequest) (*payment.CheckoutSession, error) {
	m, err := p.request(ctx, "sale", r.Amount, r.Customer, r.Metadata, r.SuccessURL, "")
	if err != nil {
		return nil, err
	}
	ref, _ := m["tran_ref"].(string)
	u, _ := m["redirect_url"].(string)
	return &payment.CheckoutSession{ID: ref, URL: u}, nil
}

func (p *provider) Refund(ctx context.Context, r payment.RefundRequest) error {
	body := map[string]any{
		"profile_id":       p.profile,
		"tran_type":        "refund",
		"tran_class":       "ecom",
		"cart_id":          fmt.Sprintf("refund-%d", time.Now().UnixNano()),
		"tran_ref":         r.ChargeID,
		"cart_description": "refund",
	}
	if r.Amount != nil {
		body["cart_currency"] = strings.ToUpper(r.Amount.Currency)
		body["cart_amount"] = major(*r.Amount)
	}
	_, err := p.post(ctx, "/payment/request", body)
	return err
}

func (p *provider) CreateCustomer(context.Context, payment.Customer) (string, error) {
	return "", errors.New("payment-paytabs: customers are not a PayTabs concept; pass customer_details per charge")
}

func (p *provider) CreateSubscription(context.Context, payment.SubscriptionRequest) (*payment.Subscription, error) {
	return nil, errors.New("payment-paytabs: recurring is via PayTabs agreements/token reuse — not exposed by this driver yet")
}

// HandleWebhook parses a PayTabs IPN, verifying the `signature` header (HMAC-SHA256
// of the raw body with the server key) when present.
func (p *provider) HandleWebhook(_ context.Context, headers map[string]string, body []byte) (*payment.WebhookEvent, error) {
	if sig := header(headers, "signature"); sig != "" {
		mac := hmac.New(sha256.New, []byte(p.key))
		mac.Write(body)
		if !hmac.Equal([]byte(strings.ToLower(sig)), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			return nil, errors.New("payment-paytabs: invalid webhook signature")
		}
	}
	var ev map[string]any
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	ref, _ := ev["tran_ref"].(string)
	typ := "payment.update"
	if pr, ok := ev["payment_result"].(map[string]any); ok {
		if rs, _ := pr["response_status"].(string); rs == "A" {
			typ = "charge.succeeded"
		} else if rs != "" {
			typ = "charge.failed"
		}
	}
	return &payment.WebhookEvent{Type: typ, ID: ref, Provider: "paytabs", Raw: ev}, nil
}

func header(h map[string]string, k string) string {
	for key, v := range h {
		if strings.EqualFold(key, k) {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
