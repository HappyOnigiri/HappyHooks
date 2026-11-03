package i18n

import (
	"strings"
	"testing"
)

// withCatalogs は登録された表を tables に置き換えて f を走らせ、終わったら元に戻す。
func withCatalogs(t *testing.T, tables []Catalog, f func()) {
	t.Helper()
	saved := catalogs
	catalogs = tables
	defer func() { catalogs = saved }()
	f()
}

func TestParseAndNormalize(t *testing.T) {
	//nolint:gocritic // mapKey: 前後の空白を除いて解釈することを確かめる入力として " EN " を置いている。
	for value, want := range map[string]Language{"": English, "en": English, " EN ": English, "ja": Japanese, "JA": Japanese} {
		got, err := Parse(value)
		if err != nil || got != want {
			t.Errorf("Parse(%q)=(%q,%v), want %q", value, got, err, want)
		}
		if got := Normalize(value); got != want {
			t.Errorf("Normalize(%q)=%q, want %q", value, got, want)
		}
	}
	for _, value := range []string{"fr", "english", "ja_JP"} {
		if _, err := Parse(value); err == nil || !strings.Contains(err.Error(), "en or ja") {
			t.Errorf("Parse(%q) must fail, got %v", value, err)
		}
		if got := Normalize(value); got != English {
			t.Errorf("Normalize(%q)=%q, want en", value, got)
		}
	}
}

func TestTextChoosesTheLanguageAndFillsTheTemplate(t *testing.T) {
	catalog := Catalog{
		"x.plain":    {EN: "hello", JA: "こんにちは"},
		"x.template": {EN: "hello {{.Name}}", JA: "{{.Name}} さん"},
		"x.no-ja":    {EN: "only english"},
		"x.broken":   {EN: "fine {{.Name}}", JA: "broken {{.Name"},
	}
	cases := []struct {
		language Language
		id       string
		data     map[string]any
		want     string
	}{
		{English, "x.plain", nil, "hello"},
		{Japanese, "x.plain", nil, "こんにちは"},
		{English, "x.template", map[string]any{"Name": "alice"}, "hello alice"},
		{Japanese, "x.template", map[string]any{"Name": "alice"}, "alice さん"},
		// 訳が空なら英語に戻す。
		{Japanese, "x.no-ja", nil, "only english"},
		// 訳の template が壊れていれば英語に戻す。
		{Japanese, "x.broken", map[string]any{"Name": "alice"}, "fine alice"},
		// 差し込みが足りなければ、英語でも展開できないので英語の原文を返す。
		{Japanese, "x.template", nil, "hello {{.Name}}"},
		// 未知の ID は ID そのものを返す。
		{English, "x.unknown", nil, "x.unknown"},
	}
	for _, testCase := range cases {
		if got := catalog.Text(testCase.language, testCase.id, testCase.data); got != testCase.want {
			t.Errorf("Text(%s, %s)=%q, want %q", testCase.language, testCase.id, got, testCase.want)
		}
	}
	if got := catalog.T(Japanese, "x.plain"); got != "こんにちは" {
		t.Errorf("T=%q", got)
	}
}

func TestRegisterAndGlobalText(t *testing.T) {
	withCatalogs(t, nil, func() {
		first := Register(Catalog{"a.one": {EN: "one", JA: "いち"}})
		Register(Catalog{"b.two": {EN: "two {{.N}}", JA: "に {{.N}}"}})
		if first.T(English, "a.one") != "one" {
			t.Fatal("Register must return the catalog")
		}
		if got := Text(Japanese, "b.two", map[string]any{"N": 2}); got != "に 2" {
			t.Errorf("Text=%q", got)
		}
		if got := Text(English, "c.three", nil); got != "c.three" {
			t.Errorf("unknown=%q", got)
		}
		all, err := All()
		if err != nil || len(all) != 2 {
			t.Fatalf("All=%v, %v", all, err)
		}
		if err := Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidateReportsBrokenCatalogs(t *testing.T) {
	for name, tables := range map[string][]Catalog{
		"duplicate":        {{"a.x": {EN: "x", JA: "x"}}, {"a.x": {EN: "y", JA: "y"}}},
		"empty english":    {{"a.x": {EN: " ", JA: "x"}}},
		"empty japanese":   {{"a.x": {EN: "x"}}},
		"different fields": {{"a.x": {EN: "{{.A}}", JA: "{{.B}}"}}},
		"broken english":   {{"a.x": {EN: "{{.A", JA: "{{.A}}"}}},
		"broken japanese":  {{"a.x": {EN: "{{.A}}", JA: "{{.A"}}},
		"control":          {{"a.x": {EN: "{{if .A}}x{{end}}", JA: "{{if .A}}x{{end}}"}}},
		"pipeline":         {{"a.x": {EN: "{{.A | printf}}", JA: "{{.A | printf}}"}}},
		"nested field":     {{"a.x": {EN: "{{.A.B}}", JA: "{{.A.B}}"}}},
	} {
		withCatalogs(t, tables, func() {
			if err := Validate(); err == nil {
				t.Errorf("%s: Validate must fail", name)
			}
		})
	}
}
