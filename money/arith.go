package money

import (
	"fmt"
	"math/big"
	"math/bits"
	"sort"
)

// absU — |n| uint64-ээр (MinInt64-д ч зөв).
func absU(n int64) uint64 {
	if n < 0 {
		return -uint64(n)
	}
	return uint64(n)
}

// MulInt — a×n (тоо ширхэг, тоо хэмжээ). Хязгаараас хэтэрвэл ErrOverflow.
func (a Amount) MulInt(n int64) (Amount, error) {
	hi, lo := bits.Mul64(absU(a.v), absU(n))
	if hi != 0 || lo > uint64(maxRaw) {
		return Amount{}, fmt.Errorf("%w: %v × %d", ErrOverflow, a, n)
	}
	v := int64(lo)
	if (a.v < 0) != (n < 0) {
		v = -v
	}
	return Amount{v}, nil
}

// MulRate — a×r, mode-оор 2 орон хүртэл дугуйлна. Хувь (НӨАТ г.м.): ParsePercent("10").
func (a Amount) MulRate(r Rate, mode Rounding) (Amount, error) {
	num := new(big.Int).Mul(bigFrom(a.v), bigFrom(r.v))
	q, err := divRound(num, bigFrom(rateUnit), mode)
	if err != nil {
		return Amount{}, err
	}
	return fromBig(q)
}

// Div — a/n, mode-оор дугуйлна. n==0 → ErrDivByZero. Тэнцүү хуваахад Split-ийг ашигла
// (Div нь үлдэгдлийг алддаг).
func (a Amount) Div(n int64, mode Rounding) (Amount, error) {
	q, err := divRound(bigFrom(a.v), bigFrom(n), mode)
	if err != nil {
		return Amount{}, err
	}
	return fromBig(q)
}

// AddRate — a + a×r (ж: татваргүй үнэд НӨАТ нэмэх), a×r-ийг mode-оор дугуйлна.
func (a Amount) AddRate(r Rate, mode Rounding) (Amount, error) {
	t, err := a.MulRate(r, mode)
	if err != nil {
		return Amount{}, err
	}
	return a.Add(t)
}

// IncludedPart — татвар орсон нийт дүнгээс (a, gross) татварын хэсэг: a×r/(1+r), mode-оор дугуйлна.
func (a Amount) IncludedPart(r Rate, mode Rounding) (Amount, error) {
	num := new(big.Int).Mul(bigFrom(a.v), bigFrom(r.v))
	den := new(big.Int).Add(bigFrom(rateUnit), bigFrom(r.v))
	q, err := divRound(num, den, mode)
	if err != nil {
		return Amount{}, err
	}
	return fromBig(q)
}

// SplitGross — татвар орсон дүнг (a) татваргүй дүн ба татварт задлана; net+tax == a ЯГ.
func (a Amount) SplitGross(r Rate, mode Rounding) (net, tax Amount, err error) {
	tax, err = a.IncludedPart(r, mode)
	if err != nil {
		return Amount{}, Amount{}, err
	}
	net, err = a.Sub(tax)
	if err != nil {
		return Amount{}, Amount{}, err
	}
	return net, tax, nil
}

// Allocate — a-г ratios-ийн харьцаагаар хувааж, нийлбэр нь a-тай ЯГ тэнцэнэ.
// Арга: хамгийн их үлдэгдлийн (largest remainder) — суурь хувь = floor(|a|×r/Σr), үлдсэн
// минор нэгжүүдийг бутархай нь хамгийн их хэсгүүдэд 1-ээр өгнө; тэнцвэл ЭХНИЙ индекс түрүүлнэ.
// Харьцаа 0 бол тэр хэсэг үргэлж 0. Сөрөг a-д тэмдгийг эцэст нь буцаана (симметр).
// Харьцаа сөрөг, бүгд 0, эсвэл хоосон/MaxParts-аас олон бол ErrInvalidAllocation.
//
// Цалин/хуваарьт: a.Allocate(1, 1, 1) нь Split(3)-тай ижил; 60/40 → Allocate(60, 40).
func (a Amount) Allocate(ratios ...int64) ([]Amount, error) {
	n := len(ratios)
	if n == 0 || n > MaxParts {
		return nil, fmt.Errorf("%w: хэсгийн тоо %d", ErrInvalidAllocation, n)
	}
	sum := new(big.Int)
	for _, r := range ratios {
		if r < 0 {
			return nil, fmt.Errorf("%w: сөрөг харьцаа %d", ErrInvalidAllocation, r)
		}
		sum.Add(sum, bigFrom(r))
	}
	if sum.Sign() == 0 {
		return nil, fmt.Errorf("%w: харьцааны нийлбэр 0", ErrInvalidAllocation)
	}
	abs := int64(absU(a.v))
	shares := make([]int64, n)
	rems := make([]*big.Int, n)
	var allocated int64
	for i, r := range ratios {
		p := new(big.Int).Mul(bigFrom(abs), bigFrom(r))
		q, m := new(big.Int).QuoRem(p, sum, new(big.Int))
		shares[i] = q.Int64() // q <= abs
		rems[i] = m
		allocated += shares[i]
	}
	left := abs - allocated
	if left < 0 || left > int64(n) {
		return nil, fmt.Errorf("%w: дотоод алдаа (үлдэгдэл %d)", ErrInvalidAllocation, left)
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool { return rems[idx[x]].Cmp(rems[idx[y]]) > 0 })
	for j := int64(0); j < left; j++ {
		shares[idx[j]]++
	}
	out := make([]Amount, n)
	for i, s := range shares {
		if a.v < 0 {
			s = -s
		}
		out[i] = Amount{s}
	}
	return out, nil
}

// AllocateRates — Allocate, харьцааг Rate-ээр өгөх (ж: 33.3%, 66.7%).
func (a Amount) AllocateRates(rates ...Rate) ([]Amount, error) {
	r := make([]int64, len(rates))
	for i, x := range rates {
		r[i] = x.v
	}
	return a.Allocate(r...)
}

// Split — a-г n тэнцүү хэсэгт хуваана; нийлбэр нь a-тай ЯГ тэнцэнэ. Үлдэгдэл минор нэгжийг
// эхний хэсгүүд 1-ээр авна (10.00 / 3 → 3.34, 3.33, 3.33). n<1 эсвэл >MaxParts → ErrInvalidAllocation.
func (a Amount) Split(n int) ([]Amount, error) {
	if n < 1 || n > MaxParts {
		return nil, fmt.Errorf("%w: хэсгийн тоо %d", ErrInvalidAllocation, n)
	}
	abs := int64(absU(a.v))
	base, rem := abs/int64(n), abs%int64(n)
	out := make([]Amount, n)
	for i := range out {
		s := base
		if int64(i) < rem {
			s++
		}
		if a.v < 0 {
			s = -s
		}
		out[i] = Amount{s}
	}
	return out, nil
}
