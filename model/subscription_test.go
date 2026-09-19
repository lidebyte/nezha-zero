package model

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDetectCurrency(t *testing.T) {
	tests := map[string]string{
		"USD 12.50": "USD", "$3.99": "USD", "HK$ 20": "HKD",
		"NT$800": "TWD", "¥30": "CNY", "2000円": "JPY",
	}
	for input, want := range tests {
		if got := DetectCurrency(input); got != want {
			t.Errorf("DetectCurrency(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseAndConvertCurrency(t *testing.T) {
	for input, want := range map[string]float64{"$1,234.56": 1234.56, "€1.234,56": 1234.56, "CNY 20": 20} {
		got, ok := ParsePriceAmount(input)
		if !ok || math.Abs(got-want) > 0.0001 {
			t.Errorf("ParsePriceAmount(%q) = %v, %v; want %v, true", input, got, ok, want)
		}
	}
	got, err := ConvertCurrency(12, "USD", "CNY", map[string]float64{"EUR": 1, "USD": 1.2, "CNY": 8})
	if err != nil || math.Abs(got-80) > 0.0001 {
		t.Fatalf("ConvertCurrency() = %v, %v; want 80, nil", got, err)
	}
}

func TestParseServerSubscription(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	server := &Server{
		Common: Common{ID: 42},
		Name:   "node-a",
		PublicNote: `{"billingDataMod":{"startDate":"2026-01-01","endDate":"2026-09-01",` +
			`"cycle":"月","amount":"$3.99","autoRenewal":true}}`,
	}
	got := ParseServerSubscription(server, now)
	if got.ServerID != 42 || got.Name != "node-a" || got.Group != ServerSubscriptionGroup {
		t.Fatalf("unexpected identity fields: %+v", got)
	}
	if got.StartDate.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("unexpected start date: %v", got.StartDate)
	}
	if got.EndDate.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("unexpected renewed end date: %v", got.EndDate)
	}
	if got.Price != "$3.99" {
		t.Fatalf("unexpected price: %q", got.Price)
	}
	if got.PriceUnit != "月" {
		t.Fatalf("unexpected price unit: %q", got.PriceUnit)
	}
	if got.Currency != "USD" {
		t.Fatalf("unexpected currency: %q", got.Currency)
	}
}

func TestServerSubscriptionDefaultsCurrencyAndCycle(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	server := &Server{
		PublicNote: `{"billingDataMod":{"endDate":"2026-01-01","amount":"10U","autoRenewal":true}}`,
	}
	got := ParseServerSubscription(server, now)
	if got.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", got.Currency)
	}
	if got.EndDate.Format("2006-01-02") != "2027-01-01" {
		t.Fatalf("default annual renewal date = %v, want 2027-01-01", got.EndDate)
	}
	if months := SubscriptionCycleMonths(""); months != 12 {
		t.Fatalf("empty billing cycle = %d months, want 12", months)
	}
	if months := SubscriptionCycleMonths("半年"); months != 6 {
		t.Fatalf("semiannual billing cycle = %d months, want 6", months)
	}
}

func TestSubscriptionMarshalForDashboardEscapesScriptContent(t *testing.T) {
	s := Subscription{Common: Common{ID: 7}, Name: `</script><script>alert(1)</script>`, Group: "Default"}
	encoded := string(s.MarshalForDashboard())
	if strings.Contains(encoded, "</script>") {
		t.Fatalf("unsafe script terminator in JSON: %s", encoded)
	}
	if !strings.Contains(encoded, `"Enable":true`) {
		t.Fatalf("enabled subscription state missing from JSON: %s", encoded)
	}
	s.Disabled = true
	if encoded = string(s.MarshalForDashboard()); !strings.Contains(encoded, `"Enable":false`) {
		t.Fatalf("disabled subscription state missing from JSON: %s", encoded)
	}
}

func TestSubscriptionRemainingDays(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 9, 19, 23, 59, 0, 0, location)
	end := time.Date(2026, 9, 20, 0, 0, 0, 0, location)
	if got := SubscriptionRemainingDays(now, end); got != 1 {
		t.Fatalf("remaining days = %d, want 1", got)
	}
}

func TestRenewSubscriptionEndDate(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, location)

	tests := []struct {
		name    string
		end     time.Time
		cycle   string
		want    time.Time
		renewed bool
	}{
		{
			name:    "renews expired monthly subscription through today",
			end:     time.Date(2026, 7, 20, 0, 0, 0, 0, location),
			cycle:   "月",
			want:    time.Date(2026, 10, 20, 0, 0, 0, 0, location),
			renewed: true,
		},
		{
			name:    "keeps future expiration",
			end:     time.Date(2027, 9, 20, 0, 0, 0, 0, location),
			cycle:   "年",
			want:    time.Date(2027, 9, 20, 0, 0, 0, 0, location),
			renewed: false,
		},
		{
			name:    "does not renew lifetime subscription",
			end:     time.Date(2025, 9, 20, 0, 0, 0, 0, location),
			cycle:   "永续",
			want:    time.Date(2025, 9, 20, 0, 0, 0, 0, location),
			renewed: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, renewed := RenewSubscriptionEndDate(test.end, test.cycle, now)
			if renewed != test.renewed || !got.Equal(test.want) {
				t.Fatalf("RenewSubscriptionEndDate() = (%s, %t), want (%s, %t)", got, renewed, test.want, test.renewed)
			}
		})
	}
}
