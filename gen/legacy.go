package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// anyStr — JSON-ий string/number/bool/null-ийг мөр болгож уншина (Lambda spec-д нэг талбар
// заримдаа тоо, заримдаа мөр байдаг).
type anyStr string

func (s *anyStr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0, string(b) == "null":
		*s = ""
	case b[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = anyStr(v)
	case b[0] == '{' || b[0] == '[':
		*s = anyStr(b)
	default:
		*s = anyStr(b) // тоо / true / false
	}
	return nil
}

// strList — string массив эсвэл таслалаар тусгаарласан мөр (null → хоосон).
type strList []string

func (l *strList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*l = nil
		return nil
	}
	if b[0] == '[' {
		var v []string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*l = v
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*l = nil
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

// LegacyRule — формын талбарын шалгалтын нэг дүрэм (`{"type":"required","msg":"..."}`).
type LegacyRule struct {
	Type  string `json:"type"`
	Msg   anyStr `json:"msg"`
	Val   anyStr `json:"val"`
	Regex anyStr `json:"regex"`
}

// LegacyOption — Select/Radio-ийн статик сонголт.
type LegacyOption struct {
	Label anyStr `json:"label"`
	Value anyStr `json:"value"`
}

// LegacyFilterUser — relation.filterWithUser: `{"tableField":"org_id","userField":"org_id"}`.
type LegacyFilterUser struct {
	TableField string `json:"tableField"`
	UserField  string `json:"userField"`
}

// LegacyRelation — Select/GridSelector-ийн холбоос.
type LegacyRelation struct {
	Table             string             `json:"table"`
	Key               string             `json:"key"`
	Fields            strList            `json:"fields"`
	FilterWithUser    []LegacyFilterUser `json:"-"`
	SortField         string             `json:"sortField"`
	SortOrder         string             `json:"sortOrder"`
	Multiple          bool               `json:"multiple"`
	Filter            string             `json:"filter"`
	ParentFieldOfForm string             `json:"parentFieldOfForm"`
	ParentFieldOfTbl  string             `json:"parentFieldOfTable"`

	rawFilterWithUser json.RawMessage
}

func (r *LegacyRelation) UnmarshalJSON(b []byte) error {
	type plain LegacyRelation
	aux := struct {
		*plain
		FWU json.RawMessage `json:"filterWithUser"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	r.rawFilterWithUser = aux.FWU
	// filterWithUser: null | [{tableField,userField}] — өөр хэлбэр бол үл тооно.
	_ = json.Unmarshal(aux.FWU, &r.FilterWithUser)
	return nil
}

// LegacyField — формын нэг талбар (эсвэл SubForm).
type LegacyField struct {
	Model    string `json:"model"`
	Label    anyStr `json:"label"`
	FormType anyStr `json:"formType"`
	DBType   anyStr `json:"dbType"`
	Hidden   bool   `json:"hidden"`
	Disabled bool   `json:"disabled"`
	Default  anyStr `json:"default"`

	Rules       []LegacyRule    `json:"rules"`
	Relation    *LegacyRelation `json:"relation"`
	Options     []LegacyOption  `json:"options"`
	FillByUser  anyStr          `json:"fillByUserField"`
	HasUserID   bool            `json:"hasUserId"`
	Trigger     anyStr          `json:"trigger"`
	HasEquation bool            `json:"hasEquation"`
	Equations   anyStr          `json:"equations"`
	IfShowHide  anyStr          `json:"ifshowhide"`
	IsGridSrch  bool            `json:"isGridSearch"`
	CustomRegex anyStr          `json:"custom_regex"`
	CustomMsg   anyStr          `json:"custom_regex_msg"`
	File        struct {
		IsMultiple bool `json:"isMultiple"`
	} `json:"file"`

	// SubForm
	Name         anyStr        `json:"name"`
	Parent       string        `json:"parent"`
	Subtype      anyStr        `json:"subtype"`
	FormID       anyStr        `json:"formId"`
	CheckEmpty   bool          `json:"checkEmpty"`
	AddFromGrid  bool          `json:"addFromGrid"`
	UseTableType bool          `json:"useTableType"`
	DisableCrt   bool          `json:"disableCreate"`
	DisableEdt   bool          `json:"disableEdit"`
	DisableDel   bool          `json:"disableDelete"`
	SubSchema    []LegacyField `json:"schema"`
}

// IsSubForm — SubForm (мөртэй өгөгдөл) эсэх.
func (f LegacyField) IsSubForm() bool { return strings.EqualFold(string(f.FormType), "SubForm") }

// LegacyTriggers — формын before/after trigger-ууд (`pkg@Func` / `controllers.Func`).
type LegacyTriggers struct {
	Namespace string `json:"namespace"`
	Insert    struct {
		Before anyStr `json:"before"`
		After  anyStr `json:"after"`
	} `json:"insert"`
	Update struct {
		Before anyStr `json:"before"`
		After  anyStr `json:"after"`
	} `json:"update"`
	Email         json.RawMessage `json:"email"`
	CacheClearURL anyStr          `json:"cache_clear_url"`
}

// LegacyFormula — формын нэг formula (`targets` талбарын prop-ыг `template`-аар тооцно).
type LegacyFormula struct {
	Targets []struct {
		Field string `json:"field"`
		Prop  string `json:"prop"`
	} `json:"targets"`
	Template anyStr `json:"template"`
}

// LegacyForm — Lambda form schema (main/lambda/schemas/form/<id>.json)-ий хэрэгтэй хэсэг.
type LegacyForm struct {
	Model    string
	Identity string
	Fields   []LegacyField // schema дараалал
	Order    []string      // ui дарааллаар талбарын model-ууд (давхардалгүй)
	Triggers LegacyTriggers
	Formula  []LegacyFormula

	HasExtraButtons bool
	HasSteps        bool
}

type uiNode struct {
	Model    string          `json:"model"`
	FormType json.RawMessage `json:"formType"`
	Children []uiNode        `json:"children"`
}

func (n uiNode) collect(out *[]string, seen map[string]bool) {
	if n.Model != "" && len(n.FormType) > 0 {
		if !seen[n.Model] {
			seen[n.Model] = true
			*out = append(*out, n.Model)
		}
		return
	}
	for _, c := range n.Children {
		c.collect(out, seen)
	}
}

// ParseForm — form JSON-ийг уншина.
func ParseForm(r io.Reader) (*LegacyForm, error) {
	var raw struct {
		Model    string        `json:"model"`
		Identity string        `json:"identity"`
		Schema   []LegacyField `json:"schema"`
		UI       struct {
			Schema []uiNode `json:"schema"`
		} `json:"ui"`
		Triggers     json.RawMessage `json:"triggers"`
		Formula      json.RawMessage `json:"formula"`
		ExtraButtons json.RawMessage `json:"extraButtons"`
		Step         struct {
			HasPermission bool            `json:"hasPermission"`
			IsCancel      bool            `json:"isCancel"`
			List          json.RawMessage `json:"list"`
		} `json:"step"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("gen: form JSON: %w", err)
	}
	if raw.Model == "" {
		return nil, fmt.Errorf("gen: form JSON-д `model` алга")
	}
	f := &LegacyForm{Model: raw.Model, Identity: raw.Identity, Fields: raw.Schema}
	seen := map[string]bool{}
	for _, n := range raw.UI.Schema {
		n.collect(&f.Order, seen)
	}
	// ui-д ороогүй талбарыг schema дарааллаар төгсгөлд нэмнэ.
	for _, fl := range f.Fields {
		if fl.Model != "" && !seen[fl.Model] {
			seen[fl.Model] = true
			f.Order = append(f.Order, fl.Model)
		}
	}
	if len(raw.Triggers) > 0 {
		_ = json.Unmarshal(raw.Triggers, &f.Triggers) // объект биш бол үл тооно
	}
	if len(raw.Formula) > 0 {
		_ = json.Unmarshal(raw.Formula, &f.Formula)
	}
	f.HasExtraButtons = nonEmptyJSON(raw.ExtraButtons)
	f.HasSteps = raw.Step.HasPermission || raw.Step.IsCancel || nonEmptyJSON(raw.Step.List)
	return f, nil
}

// nonEmptyJSON — null / [] / {} / "" биш эсэх.
func nonEmptyJSON(b json.RawMessage) bool {
	s := strings.TrimSpace(string(b))
	switch s {
	case "", "null", "[]", "{}", `""`, "false", "0":
		return false
	}
	return true
}

// Field — model-оор хайх (SubForm-ийг оруулна).
func (f *LegacyForm) Field(model string) *LegacyField {
	for i := range f.Fields {
		if f.Fields[i].Model == model {
			return &f.Fields[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Grid

// LegacyGridColumn — grid-ийн нэг багана.
type LegacyGridColumn struct {
	Model      string `json:"model"`
	Label      anyStr `json:"label"`
	GridType   anyStr `json:"gridType"`
	Hide       bool   `json:"hide"`
	Sortable   bool   `json:"sortable"`
	Searchable bool   `json:"searchable"`
	Filterable bool   `json:"filterable"`
	Virtual    bool   `json:"virtualColumn"`
	Filter     struct {
		Type anyStr `json:"type"`
	} `json:"filter"`
}

// LegacyAggregation — columnAggregations: `{"aggregation":"SUM","column":"amount"}`.
type LegacyAggregation struct {
	Aggregation string `json:"aggregation"`
	Column      string `json:"column"`
}

// LegacyGrid — Lambda grid schema (main/lambda/schemas/grid/<id>.json)-ий хэрэгтэй хэсэг.
type LegacyGrid struct {
	Model       string `json:"model"`
	MainTable   string `json:"mainTable"`
	IsView      bool   `json:"isView"`
	Identity    string `json:"identity"`
	Sort        string `json:"sort"`
	SortOrder   string `json:"sortOrder"`
	Paging      int    `json:"paging"`
	SoftDelete  bool   `json:"softDelete"`
	IsExcel     bool   `json:"isExcel"`
	HasCheckbox bool   `json:"hasCheckbox"`
	Condition   anyStr `json:"condition"`
	IsPivot     bool   `json:"isPivot"`
	IsExcelUpl  bool   `json:"isExcelUpload"`
	// IsGlobalSearch — grid-ийн нэгдсэн хайлтын талбар (Lambda-д бүх харагдах текст баганаар хайдаг).
	IsGlobalSearch bool `json:"isGlobalSearch"`

	Actions      []string            `json:"actions"`
	Columns      []LegacyGridColumn  `json:"schema"`
	Aggregations []LegacyAggregation `json:"columnAggregations"`

	Formula   json.RawMessage `json:"formula"`
	AggFormul json.RawMessage `json:"columnAggregationsFormula"`
	Filter    json.RawMessage `json:"filter"`
	Triggers  struct {
		Namespace    anyStr `json:"namespace"`
		BeforeFetch  anyStr `json:"beforeFetch"`
		AfterFetch   anyStr `json:"afterFetch"`
		BeforeDelete anyStr `json:"beforeDelete"`
		AfterDelete  anyStr `json:"afterDelete"`
		BeforePrint  anyStr `json:"beforePrint"`
	} `json:"triggers"`
}

// ParseGrid — grid JSON-ийг уншина.
func ParseGrid(r io.Reader) (*LegacyGrid, error) {
	var g LegacyGrid
	if err := json.NewDecoder(r).Decode(&g); err != nil {
		return nil, fmt.Errorf("gen: grid JSON: %w", err)
	}
	if g.Model == "" {
		return nil, fmt.Errorf("gen: grid JSON-д `model` алга")
	}
	return &g, nil
}

// Column — model-оор хайх.
func (g *LegacyGrid) Column(model string) *LegacyGridColumn {
	for i := range g.Columns {
		if g.Columns[i].Model == model {
			return &g.Columns[i]
		}
	}
	return nil
}

// atoi — тоон мөрийг уншина (алдаа = 0).
func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
