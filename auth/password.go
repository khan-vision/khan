package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// DefaultCost — HashPassword-ийн анхдагч bcrypt cost.
const DefaultCost = bcrypt.DefaultCost

// dummyHash — санамсаргүй нууц үгийн cost=10 bcrypt hash. Байхгүй хэрэглэгчийн шалгалтад
// ашиглана (CheckPassword).
const dummyHash = "$2a$10$LWyvFabSTnrDNRXgZrhqwOZ3vWDlCZNyHBr0TCZOgKt/CZzkrRCdy"

// PasswordOption — HashPassword-ийн сонголт.
type PasswordOption func(*passwordOptions)

type passwordOptions struct{ cost int }

// WithCost — bcrypt cost (bcrypt.MinCost..MaxCost). Хүрээнээс гарвал DefaultCost.
func WithCost(cost int) PasswordOption {
	return func(o *passwordOptions) {
		if cost >= bcrypt.MinCost && cost <= bcrypt.MaxCost {
			o.cost = cost
		}
	}
}

// HashPassword — нууц үгийг bcrypt-ээр hash хийнэ. bcrypt 72 байтаас урт нууц
// үгийг хүлээн авдаггүй тул алдаа буцаана (чимээгүй таслахгүй).
func HashPassword(password string, opts ...PasswordOption) (string, error) {
	o := passwordOptions{cost: DefaultCost}
	for _, fn := range opts {
		fn(&o)
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), o.cost)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return "", errors.New("auth: нууц үг 72 байтаас урт байж болохгүй")
		}
		return "", err
	}
	return string(b), nil
}

// CheckPassword — password нь hash-тай таарч байвал true.
//
// Цагийн мэдрэмтгий байдал: hash хоосон (хэрэглэгч олдоогүй) эсвэл задрахгүй бол
// cost=10 dummy hash-тай жинхэнэ bcrypt харьцуулалт хийж false буцаана — тиймээс
// "login байхгүй" ба "нууц үг буруу" хоёрын хариу өгөх хугацаа ойролцоо (login
// жагсаалт тандах халдлагаас хамгаална). Жинхэнэ hash-ууд cost=10-аас өндөр бол
// ялгаа үлдэнэ; тэр үед хэрэглэгч олдоогүй үед ч өөрийн cost-той dummy-г
// өөрөө CheckPassword-д дамжуулна.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
