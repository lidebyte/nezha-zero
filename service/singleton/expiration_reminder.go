package singleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"gorm.io/gorm"

	"github.com/railzen/nezha-zero/model"
)

func loadExpirationReminders() {
	if _, err := Cron.AddFunc("0 0 9 * * *", CheckExpirationReminders); err != nil {
		log.Printf("NEZHA>> register expiration reminder failed: %v", err)
		return
	}
}

func CheckExpirationReminders() {
	checkServerExpirationReminders()
	checkSubscriptionExpirationReminders()
}

func checkServerExpirationReminders() {
	var rules []model.AlertRule
	if err := DB.Order("id").Find(&rules).Error; err != nil {
		log.Printf("NEZHA>> load expiration reminder rules failed: %v", err)
		return
	}

	type serverExpiration struct {
		id         uint64
		name       string
		ip         string
		expiration time.Time
	}
	type serverSnapshotSource struct {
		id         uint64
		name       string
		publicNote string
		server     *model.Server
	}
	now := time.Now()
	var expirations []serverExpiration
	var servers []serverSnapshotSource
	ServerLock.RLock()
	for id, server := range ServerList {
		if server == nil {
			continue
		}
		servers = append(servers, serverSnapshotSource{
			id:         id,
			name:       server.Name,
			publicNote: server.PublicNote,
			server:     server,
		})
	}
	ServerLock.RUnlock()

	for _, source := range servers {
		if expiration, ok := parsePublicNoteExpiration(source.publicNote, now); ok {
			host, _, _, _, _ := source.server.RuntimeSnapshot()
			ip := ""
			if host != nil {
				ip = IPDesensitize(host.IP)
			}
			expirations = append(expirations, serverExpiration{id: source.id, name: source.name, ip: ip, expiration: expiration})
		}
	}

	for _, rule := range rules {
		if !rule.Enabled() || !rule.IsExpirationRule() {
			continue
		}
		config := rule.Rules[0]
		var due []serverExpiration
		for _, item := range expirations {
			if !expirationReminderCoversID(config.Cover, config.Ignore, item.id) {
				continue
			}
			days := calendarDaysBetween(now.In(item.expiration.Location()), item.expiration)
			if days == config.AdvanceDays || (config.DailyReminder && days >= 0 && days < config.AdvanceDays) {
				due = append(due, item)
			}
		}
		if len(due) == 0 {
			continue
		}
		sort.Slice(due, func(i, j int) bool { return due[i].id < due[j].id })
		var message strings.Builder
		message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "ExpirationReminderTitle"}))
		message.WriteByte(' ')
		message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "ExpirationReminderIntro"}))
		for _, item := range due {
			days := calendarDaysBetween(now.In(item.expiration.Location()), item.expiration)
			message.WriteByte('\n')
			message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID:   "ExpirationReminderItem",
				PluralCount: days,
				TemplateData: map[string]interface{}{
					"Name": item.name,
					"IP":   item.ip,
					"Date": item.expiration.Format("2006-01-02"),
					"Days": days,
				},
			}))
		}
		SendNotification(rule.NotificationTag, message.String(), nil)
	}
}

func checkSubscriptionExpirationReminders() {
	if !Conf.EnableSubscription {
		return
	}
	var rules []model.AlertRule
	if err := DB.Order("id").Find(&rules).Error; err != nil {
		log.Printf("NEZHA>> load subscription reminder rules failed: %v", err)
		return
	}
	var subscriptions []model.Subscription
	if err := DB.Order("id").Find(&subscriptions).Error; err != nil {
		log.Printf("NEZHA>> load subscriptions failed: %v", err)
		return
	}
	now := time.Now()
	for i := range subscriptions {
		subscription := &subscriptions[i]
		if subscription.Disabled || !subscription.AutoRenewalEnabled() {
			continue
		}
		renewedEnd, renewed := model.RenewSubscriptionEndDate(subscription.EndDate, subscription.PriceUnit, now)
		if !renewed {
			continue
		}
		if err := DB.Model(subscription).Update("end_date", renewedEnd).Error; err != nil {
			log.Printf("NEZHA>> renew subscription %d failed: %v", subscription.ID, err)
			continue
		}
		subscription.EndDate = renewedEnd
	}
	for _, rule := range rules {
		if !rule.Enabled() || !rule.IsSubscriptionExpirationRule() {
			continue
		}
		config := rule.Rules[0]
		var due []model.Subscription
		for _, subscription := range subscriptions {
			if subscription.Disabled || subscription.EndDate.IsZero() || !expirationReminderCoversID(config.Cover, config.Ignore, subscription.ID) {
				continue
			}
			days := calendarDaysBetween(now.In(subscription.EndDate.Location()), subscription.EndDate)
			if days == config.AdvanceDays || (config.DailyReminder && days >= 0 && days < config.AdvanceDays) {
				due = append(due, subscription)
			}
		}
		if len(due) == 0 {
			continue
		}
		sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
		var message strings.Builder
		message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "SubscriptionExpirationReminderTitle"}))
		message.WriteByte(' ')
		message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "SubscriptionExpirationReminderIntro"}))
		for _, item := range due {
			days := calendarDaysBetween(now.In(item.EndDate.Location()), item.EndDate)
			message.WriteByte('\n')
			message.WriteString(Localizer.MustLocalize(&i18n.LocalizeConfig{
				MessageID:   "SubscriptionExpirationReminderItem",
				PluralCount: days,
				TemplateData: map[string]interface{}{
					"ID":    item.ID,
					"Name":  item.Name,
					"Group": item.Group,
					"Date":  item.EndDate.Format("2006-01-02"),
					"Days":  days,
				},
			}))
		}
		SendNotification(rule.NotificationTag, message.String(), nil)
	}
}

func expirationReminderCoversID(cover uint64, selected map[uint64]bool, id uint64) bool {
	if cover == model.RuleCoverIgnoreAll {
		return selected[id]
	}
	return !selected[id]
}

func calendarDaysBetween(from, to time.Time) int {
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	to = to.In(from.Location())
	toDate := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(toDate.Sub(fromDate) / (24 * time.Hour))
}

func parsePublicNoteExpiration(publicNote string, now time.Time) (time.Time, bool) {
	item := model.ParseServerSubscription(&model.Server{PublicNote: publicNote}, now)
	return item.EndDate, !item.EndDate.IsZero()
}

const maxCurrencyResponseSize = 1 << 20

var currencyHTTPClient = &http.Client{Timeout: 15 * time.Second}

type currencyAPIResponse struct {
	Success *bool              `json:"success"`
	Base    string             `json:"base"`
	Rates   map[string]float64 `json:"rates"`
	Message string             `json:"message"`
	Error   struct {
		Code int    `json:"code"`
		Type string `json:"type"`
		Info string `json:"info"`
	} `json:"error"`
}

func loadCurrencyRates() {
	if _, err := Cron.AddFunc("0 0 2 * * *", func() {
		if !Conf.EnableSubscription || !Conf.EnableCurrencyConversion {
			return
		}
		if err := RefreshCurrencyRates(); err != nil {
			log.Printf("NEZHA>> refresh currency rates failed: %v", err)
		}
	}); err != nil {
		log.Printf("NEZHA>> register currency refresh failed: %v", err)
	}
}

func RefreshCurrencyRates() error {
	apiKey := strings.TrimSpace(Conf.CurrencyAPIKey)
	if apiKey == "" {
		return errors.New("currency API key is empty")
	}

	params := url.Values{}
	params.Set("base", "EUR")
	params.Set("symbols", strings.Join(model.CurrencyCodes(), ","))
	var endpoint string
	var req *http.Request
	var err error
	switch Conf.CurrencyProvider {
	case "apilayer":
		endpoint = "https://api.apilayer.com/fixer/latest?" + params.Encode()
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err == nil {
			req.Header.Set("apikey", apiKey)
		}
	case "fixer":
		params.Set("access_key", apiKey)
		endpoint = "https://data.fixer.io/api/latest?" + params.Encode()
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
	default:
		return errors.New("unsupported currency provider")
	}
	if err != nil {
		return err
	}

	resp, err := currencyHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request currency provider: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCurrencyResponseSize+1))
	if err != nil {
		return fmt.Errorf("read currency provider response: %w", err)
	}
	if len(body) > maxCurrencyResponseSize {
		return errors.New("currency provider response is too large")
	}
	var payload currencyAPIResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode currency provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (payload.Success != nil && !*payload.Success) {
		message := strings.TrimSpace(payload.Error.Info)
		if message == "" {
			message = strings.TrimSpace(payload.Message)
		}
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("currency provider rejected the request: %s", message)
	}
	if len(payload.Rates) == 0 {
		return errors.New("currency provider returned no rates")
	}
	base := strings.ToUpper(strings.TrimSpace(payload.Base))
	if base == "" {
		base = "EUR"
	}
	payload.Rates[base] = 1
	now := time.Now()
	rates := make(map[string]float64, len(payload.Rates))
	for code, rate := range payload.Rates {
		code = strings.ToUpper(strings.TrimSpace(code))
		if !model.IsSupportedCurrency(code) || rate <= 0 {
			continue
		}
		rates[code] = rate
	}
	if len(rates) < 2 {
		return errors.New("currency provider returned insufficient supported rates")
	}
	stored := make([]model.CurrencyRate, 0, len(rates))
	for code, rate := range rates {
		stored = append(stored, model.CurrencyRate{
			BaseCode:  base,
			Code:      code,
			Rate:      rate,
			Provider:  Conf.CurrencyProvider,
			FetchedAt: now,
		})
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("1 = 1").Delete(&model.CurrencyRate{}).Error; err != nil {
			return err
		}
		return tx.Create(&stored).Error
	})
}

func CurrencyRateSnapshot() (map[string]float64, time.Time, error) {
	var stored []model.CurrencyRate
	if err := DB.Order("code").Find(&stored).Error; err != nil {
		return nil, time.Time{}, err
	}
	rates := make(map[string]float64, len(stored))
	var fetchedAt time.Time
	for _, item := range stored {
		rates[item.Code] = item.Rate
		if item.FetchedAt.After(fetchedAt) {
			fetchedAt = item.FetchedAt
		}
	}
	return rates, fetchedAt, nil
}
