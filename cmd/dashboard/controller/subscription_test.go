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
	autoRenewalDisabled := false
	second := model.Subscription{Name: "second", Group: "Default", AutoRenewal: &autoRenewalDisabled}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
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
	if !subscriptions[0].AutoRenewalEnabled() {
		t.Fatal("default auto renewal was not persisted as enabled")
	}
	if subscriptions[1].AutoRenewalEnabled() {
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
