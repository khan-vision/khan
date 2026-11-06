# money — дизайны гэрээ

`github.com/khan-vision/khan/money` нь Postgres `numeric(18,2)`-той 1:1 таардаг **яг (float-гүй)** мөнгөний
төрөл. Шалтгаан: ERP-ийн Go код ~34 газар `float64` ашиглаж дугуйлалтын алдаа (0.1+0.2, НДШ/НӨАТ-ийн
цент зөрүү) гаргадаг. Гадны хамаарал (shopspring/decimal) нэмээгүй — зөвхөн stdlib.

## 1. Шийдвэрүүд

| Асуудал | Шийдвэр | Шалтгаан |
|---|---|---|
| Дотоод төлөөлөл | `Amount struct{ v int64 }` — 1/100 нэгж | `numeric(18,2)` дээд ±9999999999999999.99 = 18 цифр < int64. Нэмэх/хасахад wrap хийхгүй (2×10^18 < 9.2×10^18). |
| Хязгаар | Бүх Amount `\|v\| ≤ MaxMinor` (=numeric(18,2)) | Үүссэн Amount бүр DB-д хадгалагдана; хэтрэл → `ErrOverflow`. |
| Нууц талбар | `Amount(5)` cast боломжгүй | Хүчингүй утга үүсгэх боломжгүй. Zero value = 0.00; `==` ажиллана. |
| Валют | `Amount` валютгүй; `Money{Amount, Currency}` тусдаа | Багана бүр нэг валюттай. Валюттай дүнд зөрүүтэй валют → `ErrCurrencyMismatch`. |
| Валютын орон | Amount үргэлж 2 орон; `Currency.Scale()` (MNT 2, JPY/KRW 0) нь `Money.Valid/Round/Human`-д | JPY нь `numeric(18,2)`-д `1000.00` гэж хадгалагдана. 3 оронтой (KWD) валют дэмжигдэхгүй. |
| JSON гаралт | **string** `"1234.50"` | JS `Number` 2^53-аас дээш яг биш. (`crud` overlay нь `json.Number` гаргадаг — `a.JSONNumber()`.) |
| JSON оролт | string **эсвэл** тоо (`12.5`, `1e2`) яг; `null` = no-op | Хуучин клиентүүд тоо илгээдэг. Тоо нь float-оор дамжихгүй, текстээр задарна. |
| Алдаа | Бүх алдаа `error` (sentinel + `errors.Is`); panic байхгүй | Чимээгүй wrap/дугуйлалт байхгүй. |
| Дугуйлалт | `Rounding` горимыг **заавал** заана (zero value = `ErrInvalidRounding`) | Санамсаргүй анхдагч горим байхгүй. |

## 2. API товч

```go
// Үүсгэх / задлах
money.Parse("1234.50")            // strict: -?\d+(\.\d+)?([eE][+-]?\d+)? (validate-тай ижил)
money.ParseLoose(" +.5 ")         // зай, "+", ".5", "1." зөвшөөрнө
money.ParseRounded("0.005", money.HalfUp)
money.FromMinor(123450); money.FromInt(5)
money.FromFloat(12.5)             // зөвхөн ЯГ үед; 0.1+0.2 → ErrPrecision
money.FromFloatRound(f, mode)     // хуучин float64 кодын түр гүүр

// Гаргах
a.String()   // "1234.50"  (DB/API каноник)      a.Human() // "1,234.50"
a.Minor(); a.Float64() /* зөвхөн дэлгэц */; a.JSONNumber()

// Тооцоо (бүгд (Amount, error))
a.Add(b); a.Sub(b); money.Sum(xs...); a.MulInt(n)
a.MulRate(rate, mode); a.Div(n, mode)
a.AddRate(vat, mode)                    // a + a×vat
a.IncludedPart(vat, mode)               // татвар орсон дүнгээс татварын хэсэг: a×r/(1+r)
net, tax, err := gross.SplitGross(vat, mode) // net+tax == gross ЯГ
a.Allocate(60, 40); a.AllocateRates(r1, r2); a.Split(3)   // нийлбэр ЯГ
a.Neg(); a.Abs(); a.Sign(); a.IsZero(); a.Cmp(b); money.Min/Max

// Хувь / коэффициент (8 орон хүртэл, яг)
money.ParseRate("0.105"); money.ParsePercent("10.5") // ижил утга; r.Percent() == "10.5"

// Валют
money.ParseCurrency("usd"); money.USD.Scale(); money.Currencies()
m, _ := money.New(a, money.MNT); m.Human() // "1,234.50 ₮"; $ € ¥ ₩ £ урд талд
m.Add(o) /* ErrCurrencyMismatch */; m.Round(mode) /* JPY: бүхэл */; m.Valid()
m.Split(3); m.Allocate(1, 1, 1); m.Div(3, mode); m.MulRate(rate, mode) // валютын орноор (доор)
```

**Валютын орон (Money):** `Money.Split/Allocate` нь JPY/KRW (scale 0)-д бүхэл нэгжээр хуваана
(JPY 10 → 4, 3, 3 иен), `Money.Div/MulRate` нь **валютын орон хүртэл** `mode`-оор дугуйлна (JPY 10×0.15 HalfUp = 2).
Тиймээс үр дүн үргэлж `Valid()`, Split/Allocate-ийн нийлбэр ЯГ. Эх `Money` өөрөө оронд багтахгүй (JPY 10.50) бол
`Split/Allocate` → `ErrPrecision`; `Money.UnmarshalJSON` мөн ижил шалгана. Валютгүй `Amount` функцүүд 2 оронтой хэвээр —
JPY-д `Amount`-аар тооцоод `New`-ээр оруулах бол `Money` методыг ашигла. `Money` дээр `AddRate/IncludedPart/SplitGross`
байхгүй (Amount-level); JPY-д ашиглавал үр дүнг `Money.Round(mode)`-оор баталгаажуул. `New` нь оронд багтахгүй дүнг
зөвшөөрдөг (Valid-аар шалгана); `Parse/FromMinor` нь валютгүй тул scale-ийг шалгадаггүй.

Нэр: `Format` нэрийг `fmt.Formatter`-тэй зөрчилдөхөөс сэргийлж ашиглаагүй — `String()`/`Human()`.

### DB / GORM

- `Amount`, `NullAmount`, `Rate` нь `driver.Valuer` (яг текст) + `sql.Scanner` (`string`, `[]byte`, `int64` = бүхэл
  нэгж, `float64` яг үед л) + `GormDataType()` (`numeric(18,2)`, Rate `numeric(18,8)`).
- 2-оос олон оронтой numeric (ж: `SUM(x)/3`) уншвал `ErrPrecision` — чимээгүй дугуйлахгүй; SQL-д `ROUND(…, 2)` хийх
  эсвэл Go-д дугуйл.
- `NULL` → `Amount` руу `ErrNull`; NULL-тай баганад `NullAmount` эсвэл `*Amount`.
- Payload: `json:"amount"` талбар `money.Amount` болговол гаралт `"12.50"` (string) болно. **Одоогийн нийтэд ил
  API-ийн төрлийг (number) өөрчлөх бол клиентийг шалга**; payload ижил байх ёстой газарт `a.JSONNumber()` ашигла.
- Гүйцэтгэл: нэмэх/хасах/харьцуулах нь int64; `MulRate/Div/Allocate` нь `math/big`-ээр (зөв байдал > хурд).

## 3. Дугуйлалтын заавар

| Горим | Утга | Хэзээ |
|---|---|---|
| `HalfUp` | хагас нь тэгээс холдоно (2.5→3, -2.5→-3) | Арилжаа, нэхэмжлэх, ихэнх татвар (хуулиар өөр заагаагүй бол) |
| `HalfEven` | хагас нь тэгш рүү (banker's) | Олон мөрийн дугуйлалт хуримтлагдах тайлан/хүү; хазайлт багатай |
| `HalfDown` | хагас нь тэг рүү | Хуулиар шаардсан үед |
| `Down` | тэг рүү таслах | Хөнгөлөлт/урамшууллын дээд хязгаар |
| `Up` | тэгээс холдох | Доод хураамж, "дор хаяж" |
| `Floor` / `Ceil` | -∞ / +∞ рүү | Тэмдгээс хамаарахгүй чиглэл хэрэгтэй үед |

Дүрэм:
1. **Дугуйлалтыг нэг л удаа, эцсийн шатанд** хий. Завсрын үр дүнг дугуйлж дахин үржүүлэхгүй.
2. **Хуваахдаа `Div` биш `Allocate/Split`** — `Div` үлдэгдлийг алддаг (10.00/3×3=9.99). `Allocate` нийлбэрийг ЯГ хадгална.
3. **Татвар задлахад `SplitGross`** — `net = gross - tax` тул `net+tax == gross`.
4. `Allocate` нь хамгийн их үлдэгдлийн арга; тэнцвэл **эхний индекс** түрүүлнэ (тодорхойлогдсон, тест хийгдсэн).
5. Татварын хууль (НӨАТ, НДШ, ХХОАТ-ын хувь/дүрэм) энд кодлогдоогүй — дуудагч `Rate` дамжуулна. НДШ/ХХОАТ-ын
   томьёо зөвхөн `main/app/payrollcalc`-д (CLAUDE.md); энэ package нь тэнд ашиглах *үндэс*.

## 4. `main` дахь float64-оос шилжих

Нэг газрыг нэг PR-аар, тестээр баталж шилжүүл.

1. **Модель**: `Amount float64 \`gorm:"type:numeric(18,2)"\`` → `Amount money.Amount`. Багана өөрчлөгдөхгүй.
2. **Бүх `float64` арифметик** → `Add/Sub/MulRate`. `x * 0.1` → `x.MulRate(rate, money.HalfUp)`.
3. **Хуучин дуудагч float шаардвал** шилжилтийн хугацаанд хил дээр л:
   - float → Amount: `money.FromFloatRound(f, money.HalfUp)` (дотоод хуучин тооцооны үр дүн) эсвэл `FromFloat` (яг байх ёстой утга).
   - Amount → float: `a.Float64()` (дэлгэцэнд л).
4. **Хэсэглэж хуваах** (хуваарьт төлбөр, цалингийн хуваарилалт): `total/n` биш `total.Split(n)` / `total.Allocate(w...)`.
5. **Татвар**: `vat, _ := money.ParsePercent(cfg.VAT)`; нэмэх → `AddRate`, орсон дүнгээс задлах → `SplitGross`.
6. **Нийлбэр**: `for … sum += x` → `money.Sum(xs...)` (хэтрэлтийг баригдана).
7. **Хүсэлт/хариу**: DTO-д `money.Amount` — оролт string/тоо хоёуланг хүлээнэ; хариу string. `validate.Money` талбарын
   decimal текстийг шууд `money.Parse`-д өгч болно (ижил grammar).
8. **Тест**: цент зөрүүтэй жишээ (`0.1+0.2`, `1.005`, `10/3`) нэмж, хуучин float үр дүнтэй харьцуул.
   Цалин: `payrollcalc` дотор шилжих үед хуучин үр дүнгээс 0.01 зөрвөл дугуйлалтын горимыг тааруул, тестэд тэмдэглэ.

## 5. Тест

`go test -race ./money/` (DB тест `KHAN_TEST_DB_*` тохируулаагүй бол алгасагдана). Fuzz:
`go test -run '^$' -fuzz '^FuzzParseFormat$' -fuzztime=10s ./money/` — `FuzzParseFormat`, `FuzzFormatParse`,
`FuzzAllocate` (нийлбэр инвариант), `FuzzRounding` (жишиг big.Rat-тай тулгана). Бодит Postgres: AutoMigrate →
`numeric(18,2)`, хязгаарын утга, `SUM`, `NULL`, `::text` дүрслэл.
