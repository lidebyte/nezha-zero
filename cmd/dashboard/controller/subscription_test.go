package controller

import (
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/railzen/nezha-zero/model"
	"github.com/railzen/nezha-zero/resource"
	"github.com/railzen/nezha-zero/service/singleton"
)

func TestEmbeddedTemplatesParse(t *testing.T) {
	if _, err := template.New("").Funcs(funcMap).ParseFS(resource.TemplateFS, "template/**/*.html"); err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
}

func TestBatchUpdateSubscriptionGroup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Subscription{}); err != nil {
		t.Fatal(err)
	}
	first := model.Subscription{Name: "first", Group: "Default", Price: "10", PriceUnit: "month", Currency: "USD"}
	second := model.Subscription{Name: "second", Group: "Default"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&second).Update("auto_renewal", false).Error; err != nil {
		t.Fatal(err)
	}

	previousDB, previousConf := singleton.DB, singleton.Conf
	singleton.DB = db
	singleton.Conf = &model.Config{EnableSubscription: true}
	t.Cleanup(func() {
		singleton.DB = previousDB
		singleton.Conf = previousConf
	})

	body := fmt.Sprintf(`{"Subscriptions":[%d,%d],"Group":"Production"}`, first.ID, second.ID)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/batch-update-subscription-group", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")

	(&memberAPI{}).batchUpdateSubscriptionGroup(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var subscriptions []model.Subscription
	if err := db.Order("id").Find(&subscriptions).Error; err != nil {
		t.Fatal(err)
	}
	for _, subscription := range subscriptions {
		if subscription.Group != "Production" {
			t.Fatalf("subscription %d group = %q", subscription.ID, subscription.Group)
		}
	}
	if subscriptions[0].PriceUnit != "month" {
		t.Fatalf("price unit was not persisted: %q", subscriptions[0].PriceUnit)
	}
	if subscriptions[0].Currency != "USD" {
		t.Fatalf("currency was not persisted: %q", subscriptions[0].Currency)
	}
	if !subscriptions[0].AutoRenewal {
		t.Fatal("default auto renewal was not persisted as enabled")
	}
	if subscriptions[1].AutoRenewal {
		t.Fatal("disabled auto renewal was not persisted")
	}
}

func TestNormalizeSubscriptionLink(t *testing.T) {
	tests := map[string]string{
		"":                    "",
		" example.com/path ":  "https://example.com/path",
		"http://example.com":  "http://example.com",
		"HTTPS://example.com": "HTTPS://example.com",
	}
	for input, want := range tests {
		if got := normalizeSubscriptionLink(input); got != want {
			t.Errorf("normalizeSubscriptionLink(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatSubscriptionAmount(t *testing.T) {
	tests := []struct {
		amount float64
		want   string
	}{
		{1200, "1,200.00"},
		{1234567.8, "1,234,567.80"},
		{9.9, "9.90"},
		{0.5, "0.50"},
		{-12, "-12.00"},
		{0, "0.00"},
	}
	for _, test := range tests {
		if got := formatSubscriptionAmount(test.amount); got != test.want {
			t.Errorf("formatSubscriptionAmount(%v) = %q, want %q", test.amount, got, test.want)
		}
	}
}

func TestSubscriptionPriceDivisor(t *testing.T) {
	tests := []struct {
		priceUnit string
		want      int
	}{
		{"月", 1},
		{"季度", 3},
		{"半年", 6},
		{"年", 12},
		{"两年", 24},
		{"三年", 36},
		{"五年", 60},
		{"quarterly", 3},
		{"永续", 1},
		{"", 1},
		{"每周", 1},
	}
	for _, test := range tests {
		if got := subscriptionPriceDivisor(true, test.priceUnit); got != test.want {
			t.Errorf("subscriptionPriceDivisor(true, %q) = %d, want %d", test.priceUnit, got, test.want)
		}
	}
	if got := subscriptionPriceDivisor(false, "年"); got != 1 {
		t.Errorf("subscriptionPriceDivisor(false, ...) = %d, want 1", got)
	}
}
