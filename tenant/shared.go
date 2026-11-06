package tenant

import (
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// Hybrid (shared) хүснэгт: org_id NULLABLE, NULL = бүх байгууллагад харагдах СИСТЕМИЙН мөр
// (ж: attendance.lut_attendance_request). Унших нөхцөл нь `(org_id = ? OR org_id IS NULL)`;
// бичих нөхцөл нь үргэлж `org_id = ?` (tenant өөрийн мөрийг л өөрчилнө), системийн мөрийг
// зөвхөн ил тод (SystemRow) тэмдэглэсэн statement өөрчилнө.

// SharedCond — hybrid хүснэгтийн унших нөхцөл `(<prefix>org_id = ? OR <prefix>org_id IS NULL)`.
// prefix нь "" эсвэл "t." (цэгтэй). Guard яг энэ хэлбэрийг org-scope гэж хүлээн авна. Аргумент нь
// нэг org_id утга.
func SharedCond(prefix string) string {
	return "(" + prefix + ClaimOrgID + " = ? OR " + prefix + ClaimOrgID + " IS NULL)"
}

// settingSystemRow — SystemRow-ийн statement-level тэмдэглэгээний түлхүүр.
const settingSystemRow = "khan:tenant:system_row"

// SystemRow — энэ statement-ийг СИСТЕМИЙН мөр (org_id IS NULL)-д зориулсан гэж тэмдэглэнэ: Guard
// түүнийг org-нөхцөлгүй гэж зөрчил болгохгүй. ЗӨВХӨН нэг statement-д үйлчилнэ (транзакц/context
// бүхлийг чөлөөлөхгүй — SystemTx-ээс ялгаатай). Дуудагч нь системийн мөр бичих эрхийг (ж: sysadmin)
// өөрөө баталгаажуулсан байх ёстой.
//
//	tenant.SystemRow(tx.Table("t")).Where("org_id IS NULL AND id = ?", id).Updates(...)
func SystemRow(db *gorm.DB) *gorm.DB { return db.Set(settingSystemRow, true) }

func isSystemRow(db *gorm.DB) bool {
	v, ok := db.Get(settingSystemRow)
	b, _ := v.(bool)
	return ok && b
}

// sharedCondRe — SharedCond-ийн хоёр дараалал, бүтнээрээ (anchored): `(x.org_id = ? OR x.org_id IS NULL)`
// эсвэл `(x.org_id IS NULL OR x.org_id = ?)`. Хоёр org_id нь ижил prefix-тэй байх ёстой.
var sharedCondRe = regexp.MustCompile(`(?i)^\(\s*(?:(?:[a-z_][a-z0-9_]*\.)?"?org_id"?\s*=\s*(?:\?|\$\d+)\s+or\s+(?:[a-z_][a-z0-9_]*\.)?"?org_id"?\s+is\s+null|(?:[a-z_][a-z0-9_]*\.)?"?org_id"?\s+is\s+null\s+or\s+(?:[a-z_][a-z0-9_]*\.)?"?org_id"?\s*=\s*(?:\?|\$\d+))\s*\)$`)

// isSharedCond — SQL нь яг SharedCond хэлбэр (бусад нөхцөл нэмэгдээгүй) эсэх.
func isSharedCond(sql string) bool {
	s := strings.TrimSpace(sql)
	if !sharedCondRe.MatchString(s) {
		return false
	}
	// Хоёр org_id-ийн prefix ижил эсэх (`a.org_id = ? OR b.org_id IS NULL` биш).
	prefixes := orgPrefixRe.FindAllStringSubmatch(s, -1)
	return len(prefixes) == 2 && strings.EqualFold(prefixes[0][1], prefixes[1][1])
}

var orgPrefixRe = regexp.MustCompile(`(?i)((?:[a-z_][a-z0-9_]*\.)?)"?org_id"?`)
