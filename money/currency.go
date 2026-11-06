package money

import (
	"fmt"
	"strings"
)

// Currency — ISO 4217 валютын код ("MNT"). Zero value ("") нь DefaultCurrency (MNT) гэж тооцогдоно.
type Currency string

// Дэмжигдсэн валютууд. Хүснэгт хязгаартай: Amount-ийн орон 2 тул 3 оронтой (KWD, BHD) валют ороогүй.
const (
	MNT Currency = "MNT" // Монгол төгрөг
	USD Currency = "USD"
	EUR Currency = "EUR"
	CNY Currency = "CNY"
	JPY Currency = "JPY" // 0 орон
	KRW Currency = "KRW" // 0 орон
	RUB Currency = "RUB"
	GBP Currency = "GBP"
	KZT Currency = "KZT"
	CHF Currency = "CHF"
	AUD Currency = "AUD"
	CAD Currency = "CAD"
	HKD Currency = "HKD"
	SGD Currency = "SGD"
	TRY Currency = "TRY"
	INR Currency = "INR"

	// DefaultCurrency — валют заагаагүй үеийн анхдагч.
	DefaultCurrency = MNT
)

// CurrencyInfo — валютын мэдээлэл.
type CurrencyInfo struct {
	Code        Currency
	Scale       int    // ISO 4217 minor unit-ийн орон (0 эсвэл 2)
	Symbol      string // дэлгэцийн тэмдэг
	SymbolAfter bool   // true: "1,234.50 ₮"; false: "$1,234.50"
	Name        string // монгол нэр
}

func lookupCurrency(code Currency) (CurrencyInfo, bool) {
	i := func(sc int, sym string, after bool, name string) (CurrencyInfo, bool) {
		return CurrencyInfo{Code: code, Scale: sc, Symbol: sym, SymbolAfter: after, Name: name}, true
	}
	switch code {
	case MNT:
		return i(2, "₮", true, "Монгол төгрөг")
	case USD:
		return i(2, "$", false, "АНУ-ын доллар")
	case EUR:
		return i(2, "€", false, "Евро")
	case CNY:
		return i(2, "CN¥", false, "Хятад юань")
	case JPY:
		return i(0, "¥", false, "Японы иен")
	case KRW:
		return i(0, "₩", false, "Солонгос вон")
	case RUB:
		return i(2, "₽", true, "Оросын рубль")
	case GBP:
		return i(2, "£", false, "Английн фунт")
	case KZT:
		return i(2, "₸", true, "Казахстан тэнгэ")
	case CHF:
		return i(2, "CHF", true, "Швейцарь франк")
	case AUD:
		return i(2, "A$", false, "Австралийн доллар")
	case CAD:
		return i(2, "C$", false, "Канадын доллар")
	case HKD:
		return i(2, "HK$", false, "Хонг Конг доллар")
	case SGD:
		return i(2, "S$", false, "Сингапур доллар")
	case TRY:
		return i(2, "₺", true, "Туркийн лир")
	case INR:
		return i(2, "₹", false, "Энэтхэгийн рупи")
	}
	return CurrencyInfo{}, false
}

// Currencies — дэмжигдсэн валютуудын кодууд (тогтмол дараалал).
func Currencies() []Currency {
	return []Currency{MNT, USD, EUR, CNY, JPY, KRW, RUB, GBP, KZT, CHF, AUD, CAD, HKD, SGD, TRY, INR}
}

// ParseCurrency — кодыг (том/жижиг үсэг үл ялгана, гадна зайг тооцохгүй) шалгаж каноник болгоно.
// Хоосон мөр → DefaultCurrency.
func ParseCurrency(s string) (Currency, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultCurrency, nil
	}
	c := Currency(strings.ToUpper(s))
	if _, ok := lookupCurrency(c); !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownCurrency, clip(s))
	}
	return c, nil
}

// Code — каноник код ("" → MNT).
func (c Currency) Code() Currency {
	if c == "" {
		return DefaultCurrency
	}
	return c
}

// Info — валютын мэдээлэл; үл мэдэгдэх бол ErrUnknownCurrency.
func (c Currency) Info() (CurrencyInfo, error) {
	info, ok := lookupCurrency(c.Code())
	if !ok {
		return CurrencyInfo{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, clip(string(c)))
	}
	return info, nil
}

// Scale — валютын минор нэгжийн орон (MNT 2, JPY 0).
func (c Currency) Scale() (int, error) {
	info, err := c.Info()
	return info.Scale, err
}

// String — каноник код.
func (c Currency) String() string { return string(c.Code()) }
