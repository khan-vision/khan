package db

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrConflict — optimistic lock зөрчил: мөрийг уншсанаас хойш өөр хэн нэгэн
// өөрчилсөн (version таарсонгүй). Caller дахин уншиж, хэрэглэгчид мэдэгдэнэ
// (HTTP 409). errors.Is(err, ErrConflict)-ээр шалгана.
var ErrConflict = errors.New("db: мөр өөр хэрэглэгчээр өөрчлөгдсөн байна (version зөрсөн)")

var (
	identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	tableRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)
)

const zeroUUID = "00000000-0000-0000-0000-000000000000"

// UpdateVersioned — `version` баганатай мөрийг optimistic locking-оор шинэчилнэ:
//
//	UPDATE table SET <values>, version = version + 1, updated_at = now()
//	WHERE <where> AND version = <version>
//
// table нь "schema.table" эсвэл "table" (жижиг үсэг, тоо, `_`). where нь
// fail-closed multi-tenant дүрмээр ЗААВАЛ хоосон биш `org_id` түлхүүртэй байна
// (ихэвчлэн мөн "id"). values-д "version" болон "updated_at" өгч болохгүй —
// тэдгээрийг функц өөрөө удирдана. Баганы нэр бүр шалгагдаж quote хийгдэнэ,
// утгууд параметр болж орно.
//
// Амжилттай бол шинэ version (version+1) буцаана. Мөр ХЭЗЭЭ Ч олдохгүй бол
// gorm.ErrRecordNotFound; мөр байгаа ч version зөрсөн бол ErrConflict
// (wrap хийгдсэн, errors.Is-ээр таарна). tx нь caller-ийн транзакц (эсвэл
// энгийн *gorm.DB) байж болно.
func UpdateVersioned(tx *gorm.DB, table string, where map[string]any, version int64, values map[string]any) (int64, error) {
	if tx == nil {
		return 0, ErrNilDB
	}
	if err := validateVersioned(table, where, values); err != nil {
		return 0, err
	}
	res := versionedUpdate(tx, table, where, version, values)
	if res.Error != nil {
		return 0, fmt.Errorf("db: %s шинэчилж чадсангүй: %w", table, res.Error)
	}
	if res.RowsAffected > 0 {
		return version + 1, nil
	}
	// 0 мөр: байхгүй юу, version зөрсөн үү?
	var n int64
	cnt := whereClauses(tx.Session(&gorm.Session{NewDB: true}).Table(table), where).Count(&n)
	if cnt.Error != nil {
		return 0, fmt.Errorf("db: %s шалгаж чадсангүй: %w", table, cnt.Error)
	}
	if n == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return 0, fmt.Errorf("%s: %w", table, ErrConflict)
}

func validateVersioned(table string, where, values map[string]any) error {
	if !tableRe.MatchString(table) {
		return fmt.Errorf("db: хүснэгтийн нэр буруу: %q", table)
	}
	if len(where) == 0 {
		return errors.New("db: where хоосон байж болохгүй")
	}
	for k := range where {
		if !identRe.MatchString(k) {
			return fmt.Errorf("db: where баганын нэр буруу: %q", k)
		}
	}
	if _, ok := where["version"]; ok {
		return errors.New("db: where-д version өгч болохгүй (version параметрээр дамжуулна)")
	}
	switch org := where["org_id"].(type) {
	case nil:
		return errors.New("db: where-д org_id заавал хэрэгтэй (multi-tenant)")
	case string:
		if org == "" || org == zeroUUID {
			return errors.New("db: org_id хоосон байж болохгүй (multi-tenant)")
		}
	}
	if len(values) == 0 {
		return errors.New("db: шинэчлэх утга хоосон байна")
	}
	for k := range values {
		if !identRe.MatchString(k) {
			return fmt.Errorf("db: values баганын нэр буруу: %q", k)
		}
		if k == "version" || k == "updated_at" {
			return fmt.Errorf("db: values-д %s өгч болохгүй (автоматаар тавигдана)", k)
		}
	}
	return nil
}

// whereClauses — where map-ийг тогтвортой дарааллаар (түлхүүрээр эрэмбэлж)
// quote хийсэн Eq нөхцөл болгож нэмнэ.
func whereClauses(q *gorm.DB, where map[string]any) *gorm.DB {
	keys := make([]string, 0, len(where))
	for k := range where {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q = q.Where(clause.Eq{Column: clause.Column{Name: k}, Value: where[k]})
	}
	return q
}

// versionedUpdate — UPDATE-ийг (DryRun-д SQL-ийг) бүрдүүлж ажиллуулна.
func versionedUpdate(tx *gorm.DB, table string, where map[string]any, version int64, values map[string]any) *gorm.DB {
	set := make(map[string]any, len(values)+2)
	for k, v := range values {
		set[k] = v
	}
	set["version"] = gorm.Expr("version + 1")
	set["updated_at"] = gorm.Expr("now()")
	q := whereClauses(tx.Session(&gorm.Session{NewDB: true}).Table(table), where)
	return q.Where(clause.Eq{Column: clause.Column{Name: "version"}, Value: version}).Updates(set)
}
