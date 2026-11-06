package crud

import (
	"strings"
	"testing"
	"time"

	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

func TestTypedColumnsRoundTrip(t *testing.T) {
	f := newFx(t)
	f.exec(`CREATE TABLE ` + f.tbl("typed") + ` (id uuid PRIMARY KEY, org_id uuid NOT NULL, dt timestamptz, tm time, num numeric(18,6),
		big numeric(18,2), phone text, email text, flag boolean, qty bigint, kind text)`)
	res := &Resource[map[string]any]{
		Name: "test.typed", Label: "Төрөл", Table: f.tbl("typed"), Timestamps: Off(),
		Fields: []Field{
			{Name: "dt", Kind: validate.DateTime, Rules: validate.Rules{Nullable: true}},
			{Name: "tm", Kind: validate.Time, Rules: validate.Rules{Nullable: true}},
			{Name: "num", Kind: validate.Number, Rules: validate.Rules{Nullable: true}},
			{Name: "big", Kind: validate.Money, Rules: validate.Rules{Nullable: true}},
			{Name: "phone", Kind: validate.Phone, Rules: validate.Rules{Nullable: true}},
			{Name: "email", Kind: validate.Email, Rules: validate.Rules{Nullable: true}},
			{Name: "flag", Kind: validate.Bool, Rules: validate.Rules{Nullable: true}},
			{Name: "qty", Kind: validate.Int, Rules: validate.Rules{Nullable: true}},
		},
		List: queryDefault(), Can: canByRole, DB: func() *gorm.DB { return f.db },
	}
	app := mountOn(t, "/typed", res)
	c := do(t, app, "POST", "/typed", f.orgA, 1, map[string]any{
		"dt": "2026-03-04T13:06:07+08:00", "tm": "9:30", "num": "0.000001", "big": "9999999999999999.99",
		"phone": "+976 9911-2233", "email": " Bat@Example.MN ", "flag": 0, "qty": 9007199254740993,
	})
	if c.Status != 422 || c.errs()["tm"] == nil {
		t.Fatalf("9:30 буруу формат: %s", c.Raw)
	}
	c = do(t, app, "POST", "/typed", f.orgA, 1, map[string]any{
		"dt": "2026-03-04T13:06:07+08:00", "tm": "09:30", "num": "0.000001", "big": "9999999999999999.99",
		"phone": "+976 9911-2233", "email": " Bat@Example.MN ", "flag": 0, "qty": "9007199254740993",
	})
	mustStatus(t, c, 201)
	if dt, err := time.Parse(time.RFC3339, c.str("dt")); err != nil || !dt.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) || c.str("tm") != "09:30:00" || c.str("phone") != "99112233" || c.str("email") != "bat@example.mn" || c.JSON["flag"] != false {
		t.Errorf("%s", c.Raw)
	}
	// Money/Number хариунд ч яг (float64 болж алдагдаагүй)
	if !strings.Contains(string(c.Raw), `"big":9999999999999999.99`) || !strings.Contains(string(c.Raw), `"num":0.000001`) {
		t.Errorf("decimal хариунд яг байх ёстой: %s", c.Raw)
	}
	// Money яг, float-аар дамжаагүй
	if f.count(`SELECT count(*) FROM `+f.tbl("typed")+` WHERE big::text = '9999999999999999.99' AND num::text = '0.000001' AND qty = 9007199254740993`) != 1 {
		t.Error("exact decimal/int64 хадгалагдаагүй")
	}
	// null → NULL
	u := do(t, app, "PUT", "/typed/"+c.str("id"), f.orgA, 1, map[string]any{"dt": nil, "big": "", "num": nil})
	mustStatus(t, u, 200)
	if u.JSON["dt"] != nil || u.JSON["big"] != nil || u.str("phone") != "99112233" {
		t.Errorf("%s", u.Raw)
	}
}
