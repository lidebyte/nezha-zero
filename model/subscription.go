package model

import (
	"encoding/json"
	"errors"
	"html/template"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/railzen/nezha-zero/pkg/utils"
)

const ServerSubscriptionGroup = "Servers"

type Currency struct {
	Code   string
	Name   string
	Symbol string
}

// CurrencyRate stores the latest provider snapshot. Configuration such as the
// provider, API key and display currency remains in Config; runtime rate data
// belongs in the database.
type CurrencyRate struct {
	Common
	BaseCode  string    `gorm:"size:3;uniqueIndex:idx_currency_rate_base_code"`
	Code      string    `gorm:"size:3;uniqueIndex:idx_currency_rate_base_code"`
	Rate      float64   `gorm:"not null"`
	Provider  string    `gorm:"size:16"`
	FetchedAt time.Time `gorm:"index"`
}

var Currencies = []Currency{
	{Code: "CNY", Name: "Chinese Yuan", Symbol: "¥"},
	{Code: "USD", Name: "US Dollar", Symbol: "$"},
	{Code: "EUR", Name: "Euro", Symbol: "€"},
	{Code: "GBP", Name: "British Pound", Symbol: "£"},
	{Code: "JPY", Name: "Japanese Yen", Symbol: "円"},
	{Code: "HKD", Name: "Hong Kong Dollar", Symbol: "HK$"},
	{Code: "TWD", Name: "New Taiwan Dollar", Symbol: "NT$"},
	{Code: "AUD", Name: "Australian Dollar", Symbol: "A$"},
	{Code: "CAD", Name: "Canadian Dollar", Symbol: "C$"},
	{Code: "SGD", Name: "Singapore Dollar", Symbol: "S$"},
	{Code: "NZD", Name: "New Zealand Dollar", Symbol: "NZ$"},
	{Code: "CHF", Name: "Swiss Franc", Symbol: "CHF"},
	{Code: "INR", Name: "Indian Rupee", Symbol: "₹"},
	{Code: "KRW", Name: "South Korean Won", Symbol: "₩"},
	{Code: "RUB", Name: "Russian Ruble", Symbol: "₽"},
	{Code: "BRL", Name: "Brazilian Real", Symbol: "R$"},
	{Code: "MXN", Name: "Mexican Peso", Symbol: "Mex$"},
	{Code: "TRY", Name: "Turkish Lira", Symbol: "₺"},
	{Code: "THB", Name: "Thai Baht", Symbol: "฿"},
	{Code: "PHP", Name: "Philippine Peso", Symbol: "₱"},
	{Code: "IDR", Name: "Indonesian Rupiah", Symbol: "Rp"},
	{Code: "MYR", Name: "Malaysian Ringgit", Symbol: "RM"},
	{Code: "ILS", Name: "Israeli New Sheqel", Symbol: "₪"},
	{Code: "SEK", Name: "Swedish Krona", Symbol: "SEK"},
	{Code: "NOK", Name: "Norwegian Krone", Symbol: "NOK"},
	{Code: "DKK", Name: "Danish Krone", Symbol: "DKK"},
	{Code: "PLN", Name: "Polish Zloty", Symbol: "zł"},
	{Code: "CZK", Name: "Czech Koruna", Symbol: "Kč"},
	{Code: "HUF", Name: "Hungarian Forint", Symbol: "Ft"},
	{Code: "RON", Name: "Romanian Leu", Symbol: "lei"},
	{Code: "BGN", Name: "Bulgarian Lev", Symbol: "лв"},
	{Code: "ISK", Name: "Icelandic Krona", Symbol: "ISK"},
	{Code: "VND", Name: "Vietnamese Dong", Symbol: "₫"},
	{Code: "AED", Name: "UAE Dirham", Symbol: "AED"},
	{Code: "SAR", Name: "Saudi Riyal", Symbol: "SAR"},
	{Code: "ZAR", Name: "South African Rand", Symbol: "ZAR"},
}

var currencyCodePattern = regexp.MustCompile(`(?i)(?:^|[^A-Z])([A-Z]{3})(?:$|[^A-Z])`)
var priceNumberPattern = regexp.MustCompile(`[-+]?\d[\d.,]*`)

func CurrencyCodes() []string {
	codes := make([]string, 0, len(Currencies))
	for _, currency := range Currencies {
		codes = append(codes, currency.Code)
	}
	return codes
}

func IsSupportedCurrency(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, currency := range Currencies {
		if currency.Code == code {
			return true
		}
	}
	return false
}

func DetectCurrency(value string) string {
	upper := strings.ToUpper(value)
	for _, match := range currencyCodePattern.FindAllStringSubmatch(upper, -1) {
		if len(match) > 1 && IsSupportedCurrency(match[1]) {
			return match[1]
		}
	}
	symbols := []struct{ Symbol, Code string }{
		{"NT$", "TWD"}, {"HK$", "HKD"}, {"NZ$", "NZD"}, {"Mex$", "MXN"},
		{"A$", "AUD"}, {"C$", "CAD"}, {"S$", "SGD"}, {"R$", "BRL"},
		{"￥", "CNY"}, {"¥", "CNY"}, {"円", "JPY"}, {"€", "EUR"}, {"£", "GBP"},
		{"₹", "INR"}, {"₩", "KRW"}, {"₽", "RUB"}, {"₺", "TRY"}, {"฿", "THB"},
		{"₱", "PHP"}, {"₫", "VND"}, {"₪", "ILS"}, {"zł", "PLN"}, {"Kč", "CZK"},
		{"лв", "BGN"}, {"$", "USD"},
	}
	for _, item := range symbols {
		if strings.Contains(value, item.Symbol) {
			return item.Code
		}
	}
	return ""
}

func ParsePriceAmount(value string) (float64, bool) {
	number := priceNumberPattern.FindString(value)
	if number == "" {
		return 0, false
	}
	lastComma, lastDot := strings.LastIndex(number, ","), strings.LastIndex(number, ".")
	switch {
	case lastComma >= 0 && lastDot >= 0:
		if lastComma > lastDot {
			number = strings.ReplaceAll(strings.ReplaceAll(number, ".", ""), ",", ".")
		} else {
			number = strings.ReplaceAll(number, ",", "")
		}
	case lastComma >= 0:
		digitsAfter := len(number) - lastComma - 1
		if digitsAfter == 1 || digitsAfter == 2 {
			number = strings.ReplaceAll(number, ",", ".")
		} else {
			number = strings.ReplaceAll(number, ",", "")
		}
	}
	amount, err := strconv.ParseFloat(number, 64)
	return amount, err == nil
}

func ConvertCurrency(amount float64, from, to string, rates map[string]float64) (float64, error) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return amount, nil
	}
	fromRate, fromOK := rates[from]
	toRate, toOK := rates[to]
	if !fromOK || !toOK || fromRate <= 0 || toRate <= 0 {
		return 0, errors.New("exchange rate unavailable")
	}
	return amount / fromRate * toRate, nil
}

func SortedCurrencies() []Currency {
	items := append([]Currency(nil), Currencies...)
	sort.Slice(items, func(i, j int) bool { return items[i].Code < items[j].Code })
	return items
}

// Subscription stores subscriptions entered manually. Server subscriptions are
// read directly from Server.PublicNote and are never copied into this table.
type Subscription struct {
	Common
	Name      string
	StartDate time.Time
	EndDate   time.Time `gorm:"index"`
	Price     string
	PriceUnit string
	Currency  string `gorm:"size:3"`
	Link      string
	Note      string
	Group     string `gorm:"column:group_name"`
	Disabled  bool   `gorm:"default:false"`
}

func (s Subscription) MarshalForDashboard() template.JS {
	type dashboardSubscription struct {
		ID        uint64 `json:"ID"`
		Name      string `json:"Name"`
		StartDate string `json:"StartDate"`
		EndDate   string `json:"EndDate"`
		Price     string `json:"Price"`
		PriceUnit string `json:"PriceUnit"`
		Currency  string `json:"Currency"`
		Link      string `json:"Link"`
		Note      string `json:"Note"`
		Group     string `json:"Group"`
		Enable    bool   `json:"Enable"`
	}
	data, _ := utils.Json.Marshal(dashboardSubscription{
		ID:        s.ID,
		Name:      s.Name,
		StartDate: formatSubscriptionDate(s.StartDate),
		EndDate:   formatSubscriptionDate(s.EndDate),
		Price:     s.Price,
		PriceUnit: s.PriceUnit,
		Currency:  s.Currency,
		Link:      s.Link,
		Note:      s.Note,
		Group:     s.Group,
		Enable:    !s.Disabled,
	})
	return template.JS(data)
}

type ServerSubscription struct {
	ServerID  uint64
	Name      string
	StartDate time.Time
	EndDate   time.Time
	Price     string
	PriceUnit string
	Currency  string
	Group     string
}

// ParseServerSubscription reads billing data from the existing server row.
func ParseServerSubscription(server *Server, now time.Time) ServerSubscription {
	result := ServerSubscription{Group: ServerSubscriptionGroup}
	if server == nil {
		return result
	}
	result.ServerID = server.ID
	result.Name = server.Name

	var note struct {
		BillingDataMod struct {
			StartDate   string          `json:"startDate"`
			EndDate     string          `json:"endDate"`
			Cycle       string          `json:"cycle"`
			Amount      json.RawMessage `json:"amount"`
			AutoRenewal json.RawMessage `json:"autoRenewal"`
		} `json:"billingDataMod"`
	}
	if json.Unmarshal([]byte(server.PublicNote), &note) != nil {
		return result
	}
	result.StartDate, _ = ParseSubscriptionDate(note.BillingDataMod.StartDate)
	result.EndDate, _ = ParseSubscriptionDate(note.BillingDataMod.EndDate)
	result.Price = rawSubscriptionValue(note.BillingDataMod.Amount)
	result.PriceUnit = strings.TrimSpace(note.BillingDataMod.Cycle)
	result.Currency = DetectCurrency(result.Price)
	if result.Price != "" && result.Currency == "" {
		result.Currency = "USD"
	}

	if result.EndDate.IsZero() || !subscriptionAutoRenewal(note.BillingDataMod.AutoRenewal) {
		return result
	}
	months := SubscriptionCycleMonths(note.BillingDataMod.Cycle)
	if months == 0 {
		return result
	}
	for i := 0; !result.EndDate.After(now.In(result.EndDate.Location())) && i < 2400; i++ {
		result.EndDate = result.EndDate.AddDate(0, months, 0)
	}
	return result
}

func ParseSubscriptionDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "0000-00-00") {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	location := time.FixedZone("UTC+8", 8*60*60)
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func SubscriptionRemainingDays(now, end time.Time) int {
	if end.IsZero() {
		return 0
	}
	from := now.In(end.Location())
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	toDate := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(toDate.Sub(fromDate) / (24 * time.Hour))
}

func formatSubscriptionDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

func rawSubscriptionValue(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) == nil {
			return decoded
		}
	}
	return value
}

func subscriptionAutoRenewal(raw json.RawMessage) bool {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return value == "1" || strings.EqualFold(value, "true")
}

func SubscriptionCycleMonths(cycle string) int {
	switch strings.ToLower(strings.TrimSpace(cycle)) {
	case "":
		return 12
	case "月", "mo", "month", "monthly", "m":
		return 1
	case "季", "季度", "quarterly", "q":
		return 3
	case "半", "半年", "half", "semi-annually", "h":
		return 6
	case "年", "yr", "year", "annually", "y":
		return 12
	case "两年", "2 years", "2-year", "biennially":
		return 24
	case "三年", "3 years", "3-year", "triennially":
		return 36
	case "五年", "5 years", "5-year":
		return 60
	default:
		return 0
	}
}
