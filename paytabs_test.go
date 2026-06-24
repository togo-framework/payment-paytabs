package paytabs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/togo-framework/payment"
)

func newTestProvider(url string) *provider {
	return &provider{profile: "prof", key: "skey", host: url, hc: &http.Client{Timeout: 5 * time.Second}}
}

func TestCreateCheckoutSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "skey" {
			t.Errorf("missing server key auth: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["cart_amount"].(float64) != 12.50 {
			t.Errorf("amount: got %v want 12.50", body["cart_amount"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tran_ref": "TST0001", "redirect_url": "https://pay.example/abc"})
	}))
	defer ts.Close()

	cs, err := newTestProvider(ts.URL).CreateCheckoutSession(context.Background(), payment.CheckoutRequest{
		Amount: payment.Money{Amount: 1250, Currency: "SAR"}, SuccessURL: "https://ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.ID != "TST0001" || cs.URL != "https://pay.example/abc" {
		t.Fatalf("unexpected session: %+v", cs)
	}
}

func TestRefund(t *testing.T) {
	hit := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tran_type"] != "refund" || body["tran_ref"] != "TST0001" {
			t.Errorf("bad refund body: %v", body)
		}
		hit = true
		_ = json.NewEncoder(w).Encode(map[string]any{"tran_ref": "REF0001"})
	}))
	defer ts.Close()
	amt := payment.Money{Amount: 500, Currency: "SAR"}
	if err := newTestProvider(ts.URL).Refund(context.Background(), payment.RefundRequest{ChargeID: "TST0001", Amount: &amt}); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("refund endpoint not called")
	}
}

func TestHandleWebhookSignature(t *testing.T) {
	p := newTestProvider("")
	body := []byte(`{"tran_ref":"TST0001","payment_result":{"response_status":"A"}}`)
	mac := hmac.New(sha256.New, []byte(p.key))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	ev, err := p.HandleWebhook(context.Background(), map[string]string{"signature": good}, body)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "charge.succeeded" || ev.ID != "TST0001" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if _, err := p.HandleWebhook(context.Background(), map[string]string{"signature": "deadbeef"}, body); err == nil {
		t.Fatal("expected invalid-signature error")
	}
}
