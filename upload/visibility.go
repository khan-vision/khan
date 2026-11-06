package upload

import "fmt"

// Visibility — оруулсан файлын харагдах байдал.
type Visibility string

const (
	// VisibilityPublic — `/uploaded/...` дээр cookie-гүй, нийтэд нээлттэй (<img src>).
	VisibilityPublic Visibility = "public"
	// VisibilityPrivate — тусдаа хавтсанд, org-оор хуваагдсан; Serve() ХЭЗЭЭ Ч
	// үйлчлэхгүй. Зөвхөн гарын үсэгтэй URL (ServePrivate) эсвэл нэвтэрсэн
	// байгууллагын гишүүнд (ServePrivateAuth) уншигдана.
	VisibilityPrivate Visibility = "private"
)

// Valid — allowlist-ийн (public|private) утга эсэх.
func (v Visibility) Valid() bool {
	return v == VisibilityPublic || v == VisibilityPrivate
}

// fillPrivateURL — Signer байвал opaque path-д богино настай холбоос бичнэ.
// Гарын үсэг зурах алдааг далдлахгүй буцаана (дуудагч 500 болгоно).
func (u *Uploader) fillPrivateURL(dst *string, exp *int64, opaque string) error {
	if u.signer == nil {
		return nil
	}
	r, err := u.signer.Sign(opaque, u.privTTL, SignOptions{})
	if err != nil {
		return fmt.Errorf("upload: холбоос гарын үсэглэж чадсангүй: %w", err)
	}
	*dst = u.cfg.BaseURL + r.URL
	*exp = r.ExpiresAt.Unix()
	return nil
}
