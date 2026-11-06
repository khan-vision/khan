package money

import (
	"errors"
	"testing"

	"github.com/khan-vision/khan/testkit"
)

type dbRow struct {
	ID   int `gorm:"primaryKey;autoIncrement"`
	Amt  Amount
	Opt  NullAmount
	Ptr  *Amount
	Rate Rate
}

func (dbRow) TableName() string { return "money_test_rows" }

// Postgres numeric(18,2) дээр бодитоор: AutoMigrate-ийн багана, хязгаарын утга, SUM, NULL, -0.
func TestPostgresRoundtrip(t *testing.T) {
	tx := testkit.TxDB(t)
	if err := tx.AutoMigrate(&dbRow{}); err != nil {
		t.Fatal(err)
	}
	var typ, prec, scale string
	row := tx.Raw(`SELECT data_type, numeric_precision::text, numeric_scale::text FROM information_schema.columns
		WHERE table_name = 'money_test_rows' AND column_name = 'amt'`).Row()
	if err := row.Scan(&typ, &prec, &scale); err != nil {
		t.Fatal(err)
	}
	if typ != "numeric" || prec != "18" || scale != "2" {
		t.Fatalf("багана %s(%s,%s), numeric(18,2) хүлээсэн", typ, prec, scale)
	}

	max, min := Amount{MaxMinor}, Amount{-MaxMinor}
	p := mustA(t, "0.01")
	in := []dbRow{
		{Amt: mustA(t, "1234.50"), Opt: NullAmountOf(mustA(t, "-0.05")), Ptr: &p, Rate: mustR(t, "0.105")},
		{Amt: max}, {Amt: min, Opt: NullAmountOf(Zero)}, {Amt: Zero}, {Amt: mustA(t, "-0")}, {Amt: mustA(t, "0.1")},
	}
	if err := tx.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	var out []dbRow
	if err := tx.Order("id").Find(&out).Error; err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("%d мөр", len(out))
	}
	for i := range in {
		if out[i].Amt != in[i].Amt || out[i].Opt != in[i].Opt || out[i].Rate != in[i].Rate {
			t.Errorf("мөр %d: %+v ≠ %+v", i, out[i], in[i])
		}
		if (out[i].Ptr == nil) != (in[i].Ptr == nil) || (in[i].Ptr != nil && *out[i].Ptr != *in[i].Ptr) {
			t.Errorf("мөр %d ptr", i)
		}
	}
	// PG-ийн текст дүрслэл нь Amount.String()-тэй ижил
	var txt []string
	if err := tx.Model(&dbRow{}).Order("id").Pluck("amt::text", &txt).Error; err != nil {
		t.Fatal(err)
	}
	for i, s := range txt {
		if s != in[i].Amt.String() {
			t.Errorf("amt::text %q ≠ %q", s, in[i].Amt)
		}
	}
	// SUM / хоёр Amount-ийн PG нийлбэр нь Go-ийн Sum-тай ижил (0.1 нь float-оор 0.1000000001 болдоггүй)
	var sum Amount
	if err := tx.Model(&dbRow{}).Where("amt BETWEEN ? AND ?", Zero, mustA(t, "2000")).Select("SUM(amt)").Row().Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.String() != "1234.60" {
		t.Errorf("SUM = %s", sum)
	}
	// Where параметр Amount-аар
	var cnt int64
	if err := tx.Model(&dbRow{}).Where("amt = ?", max).Count(&cnt).Error; err != nil || cnt != 1 {
		t.Errorf("count = %d, %v", cnt, err)
	}
	// 2-оос олон оронтой numeric (PG-ийн хуваалт) нь чимээгүй дугуйлагдахгүй
	var bad Amount
	err := tx.Raw(`SELECT 10.00::numeric / 3`).Row().Scan(&bad)
	if !errors.Is(err, ErrPrecision) {
		t.Errorf("10/3 err = %v", err)
	}
	// NULL → Amount алдаа, NullAmount Valid=false
	var nul NullAmount
	if err := tx.Raw(`SELECT NULL::numeric`).Row().Scan(&nul); err != nil || nul.Valid {
		t.Error(nul, err)
	}
	var na Amount
	if err := tx.Raw(`SELECT NULL::numeric`).Row().Scan(&na); !errors.Is(err, ErrNull) {
		t.Errorf("NULL → Amount err = %v", err)
	}
	// DB хэтрэлт: PG numeric(18,2) нь 10^16-г няцаана (бидний Amount хэзээ ч үүсгэхгүй)
	if err := tx.Exec(`INSERT INTO money_test_rows (amt) VALUES (10000000000000000.00)`).Error; err == nil {
		t.Error("PG хязгаараас хэтэрсэн утгыг зөвшөөрсөн")
	}
}
