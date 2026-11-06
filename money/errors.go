package money

import "errors"

// Package-ийн sentinel алдаанууд. Дуудагч `errors.Is`-ээр ялгана; бодит алдаа нь
// тайлбар (оруулсан утга г.м.)-тай боож буцаагдана.
var (
	// ErrInvalid — текст/JSON/DB утга decimal тооны хэлбэртэй биш.
	ErrInvalid = errors.New("money: буруу хэлбэр")
	// ErrPrecision — утга шаардсан оронд яг багтахгүй (ж: "0.005" → 2 орон) эсвэл
	// float64 нь яг decimal-аар илэрхийлэгдэхгүй. Дугуйлалтыг ХЭЗЭЭ Ч чимээгүй хийхгүй.
	ErrPrecision = errors.New("money: оронгийн нарийвчлал хүрэлцэхгүй")
	// ErrOverflow — үр дүн numeric(18,2) (±9999999999999999.99) хязгаараас хэтэрсэн.
	ErrOverflow = errors.New("money: хязгаараас хэтэрсэн")
	// ErrDivByZero — тэгд хуваасан.
	ErrDivByZero = errors.New("money: тэгд хуваах боломжгүй")
	// ErrInvalidRounding — дугуйлалтын горим заагаагүй (zero value) эсвэл үл мэдэгдэх.
	ErrInvalidRounding = errors.New("money: дугуйлалтын горим буруу")
	// ErrInvalidAllocation — Allocate/Split-ийн харьцаа эсвэл тоо буруу.
	ErrInvalidAllocation = errors.New("money: хуваарилалтын харьцаа буруу")
	// ErrCurrencyMismatch — өөр валютуудыг хольсон.
	ErrCurrencyMismatch = errors.New("money: валют зөрүүтэй")
	// ErrUnknownCurrency — валютын код хүснэгтэд алга.
	ErrUnknownCurrency = errors.New("money: үл мэдэгдэх валют")
	// ErrNull — NULL утгыг NullAmount бус Amount/Rate руу уншсан.
	ErrNull = errors.New("money: NULL утга")
)
