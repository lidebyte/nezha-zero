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
	"strconv"
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
		if subscription.Disabled || !subscription.AutoRenewal {
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
		log.Printf("NEZHA>> subscription %d (%s) auto-renewed: end_date %s -> %s",
			subscription.ID, subscription.Name,
			subscription.EndDate.Format("2006-01-02"), renewedEnd.Format("2006-01-02"))
		subscription.EndDate = renewedEnd
	}
	for _, rule := range rules {
		if !rule.Enabled() || !rule.IsSubscriptionExpirationRule() {
			continue
		}
		config := rule.Rules[0]
		var due []model.Subscription
		for _, subscription := range subscriptions {
			if subscription.Disabled || subscription.EndDate.IsZero() ||
				model.IsLifetimeSubscriptionCycle(subscription.PriceUnit) ||
				!expirationReminderCoversID(config.Cover, config.Ignore, subscription.ID) {
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

func currencyUsageFromHeader(header http.Header) (used, limit int64, ok bool) {
	limit, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Limit-Month")), 10, 64)
	if err != nil || limit <= 0 {
		return 0, 0, false
	}
	remaining, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Remaining-Month")), 10, 64)
	if err != nil || remaining < 0 {
		return 0, 0, false
	}
	used = limit - remaining
	if used < 0 {
		used = 0
	} else if used > limit {
		used = limit
	}
	return used, limit, true
}

func currencyUsageFromResponse(provider string, header http.Header) (used, limit *int64) {
	if provider != "apilayer" {
		return nil, nil
	}
	valueUsed, valueLimit, ok := currencyUsageFromHeader(header)
	if !ok {
		return nil, nil
	}
	return &valueUsed, &valueLimit
}

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
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return fmt.Errorf("request currency provider failed: %v", err)
	}
	defer resp.Body.Close()
	monthlyUsed, monthlyLimit := currencyUsageFromResponse(Conf.CurrencyProvider, resp.Header)
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
	ratesRaw, err := json.Marshal(rates)
	if err != nil {
		return fmt.Errorf("encode currency rates: %w", err)
	}

	snapshot := model.CurrencySnapshot{
		ID:           model.CurrentCurrencySnapshotID,
		BaseCode:     base,
		Provider:     Conf.CurrencyProvider,
		RatesRaw:     string(ratesRaw),
		MonthlyUsed:  monthlyUsed,
		MonthlyLimit: monthlyLimit,
		FetchedAt:    now,
	}
	return DB.Save(&snapshot).Error
}

func CurrencyRateSnapshot() (map[string]float64, time.Time, error) {
	var snapshot model.CurrencySnapshot
	if err := DB.First(&snapshot, model.CurrentCurrencySnapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return map[string]float64{}, time.Time{}, nil
		}
		return nil, time.Time{}, err
	}
	if snapshot.Provider != Conf.CurrencyProvider {
		return map[string]float64{}, time.Time{}, nil
	}
	rates := make(map[string]float64)
	if err := json.Unmarshal([]byte(snapshot.RatesRaw), &rates); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode currency rates: %w", err)
	}
	return rates, snapshot.FetchedAt, nil
}

func CurrencyUsageSnapshot() (model.CurrencyUsageStatus, bool, error) {
	if Conf.CurrencyProvider != "apilayer" {
		return model.CurrencyUsageStatus{}, false, nil
	}

	var snapshot model.CurrencySnapshot
	if err := DB.First(&snapshot, model.CurrentCurrencySnapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.CurrencyUsageStatus{}, false, nil
		}
		return model.CurrencyUsageStatus{}, false, err
	}
	if snapshot.Provider != Conf.CurrencyProvider {
		return model.CurrencyUsageStatus{}, false, nil
	}

	now := time.Now()
	fetchedAt := snapshot.FetchedAt.In(now.Location())
	if snapshot.MonthlyUsed == nil || snapshot.MonthlyLimit == nil ||
		*snapshot.MonthlyLimit <= 0 || fetchedAt.Year() != now.Year() || fetchedAt.Month() != now.Month() {
		return model.CurrencyUsageStatus{}, false, nil
	}
	return model.CurrencyUsageStatus{
		MonthlyUsed:  *snapshot.MonthlyUsed,
		MonthlyLimit: *snapshot.MonthlyLimit,
	}, true, nil
}
