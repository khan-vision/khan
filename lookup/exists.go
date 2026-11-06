package lookup

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Exists — values бүр тухайн org-д (Spec.Where, SoftDelete-ийн хүрээнд) байгааг
// шалгаж, ОЛДООГҮЙ утгуудыг (оролтын дараалалтай, давхардалгүй) буцаана. tx нь
// ихэвчлэн crud-ийн write транзакц. Буруу төрлийн утга (uuid багананд "abc") DB-д
// очихгүйгээр "олдсонгүй" болно. nil утгыг алгасна.
func (s Spec) Exists(tx *gorm.DB, orgID string, values []any) (missing []any, err error) {
	if tx == nil {
		return nil, errors.New("khan/lookup: *gorm.DB nil")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireOrg(orgID); err != nil {
		return nil, err
	}
	type want struct {
		orig any
		val  any
	}
	order := make([]string, 0, len(values))
	wants := map[string]want{}
	var valid []any
	for _, v := range values {
		if v == nil {
			continue
		}
		c, ok := s.coerce(v)
		if !ok {
			k := "?" + fmt.Sprint(v) // буруу утга: DB-д очихгүй, шууд дутуу
			if _, dup := wants[k]; !dup {
				wants[k] = want{orig: v}
				order = append(order, k)
			}
			continue
		}
		k := key(c)
		if _, dup := wants[k]; dup {
			continue
		}
		wants[k] = want{orig: v, val: c}
		order = append(order, k)
		valid = append(valid, c)
	}

	found := make(map[string]bool, len(valid))
	for start := 0; start < len(valid); start += existsChunk {
		end := start + existsChunk
		if end > len(valid) {
			end = len(valid)
		}
		q, err := s.scoped(tx, orgID)
		if err != nil {
			return nil, err
		}
		rows, err := q.Select(s.valueCol()+" AS value").Where(s.inValues(end-start), valid[start:end]...).Rows()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v any
			if err := rows.Scan(&v); err != nil {
				_ = rows.Close()
				return nil, err
			}
			found[key(v)] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	for _, k := range order {
		if !found[k] {
			missing = append(missing, wants[k].orig)
		}
	}
	return missing, nil
}
