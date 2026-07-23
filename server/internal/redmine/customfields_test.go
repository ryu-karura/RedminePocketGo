package redmine

import "testing"

func TestMergeCustomFieldsResolvesListLabel(t *testing.T) {
	values := []CustomFieldValue{{ID: 3, Name: "優先タグ", Value: "a"}}
	defs := []CustomFieldDef{{
		ID: 3, Name: "優先タグ", FieldFormat: "list", IsRequired: true,
		MinLength:      1,
		MaxLength:      10,
		PossibleValues: []PossibleValue{{Value: "a", Label: "重要"}, {Value: "b", Label: "通常"}},
	}}
	out := MergeCustomFields(values, defs)
	if len(out) != 1 {
		t.Fatalf("got %d entries; want 1", len(out))
	}
	if !out[0].IsRequired || out[0].FieldFormat != "list" {
		t.Errorf("def not merged: %+v", out[0])
	}
	if out[0].MinLength != 1 || out[0].MaxLength != 10 {
		t.Errorf("min/max length not merged: %+v", out[0])
	}
	if out[0].DisplayValue != "重要" {
		t.Errorf("display_value = %q; want 重要", out[0].DisplayValue)
	}
}

func TestMergeCustomFieldsMultipleJoinsWithReadingPointComma(t *testing.T) {
	values := []CustomFieldValue{{ID: 3, Value: []any{"a", "b"}}}
	defs := []CustomFieldDef{{
		ID: 3, FieldFormat: "key_value_list", Multiple: true,
		PossibleValues: []PossibleValue{{Value: "a", Label: "重要"}, {Value: "b", Label: "通常"}},
	}}
	out := MergeCustomFields(values, defs)
	if out[0].DisplayValue != "重要、通常" {
		t.Errorf("display_value = %q; want 重要、通常", out[0].DisplayValue)
	}
}

func TestMergeCustomFieldsUnknownChoiceFallsBackToRawValue(t *testing.T) {
	values := []CustomFieldValue{{ID: 3, Value: "z"}}
	defs := []CustomFieldDef{{ID: 3, FieldFormat: "list", PossibleValues: []PossibleValue{{Value: "a", Label: "重要"}}}}
	out := MergeCustomFields(values, defs)
	if out[0].DisplayValue != "z" {
		t.Errorf("display_value = %q; want raw fallback z", out[0].DisplayValue)
	}
}

func TestMergeCustomFieldsDegradesWithoutDefs(t *testing.T) {
	// 定義取得が失敗した場合（管理者権限なしなど）。生値のまま、必須・選択肢
	// なしで返す（Design.md §6.4 の degrade 方針）。
	values := []CustomFieldValue{{ID: 1, Name: "備考", Value: "メモ"}}
	out := MergeCustomFields(values, nil)
	if len(out) != 1 || out[0].Value != "メモ" {
		t.Fatalf("out = %+v", out)
	}
	if out[0].IsRequired || out[0].FieldFormat != "" || out[0].DisplayValue != "" {
		t.Errorf("degraded entry should have no def metadata: %+v", out[0])
	}
}

func TestMergeCustomFieldsPreservesInputOrder(t *testing.T) {
	values := []CustomFieldValue{{ID: 2}, {ID: 1}, {ID: 3}}
	out := MergeCustomFields(values, nil)
	if out[0].ID != 2 || out[1].ID != 1 || out[2].ID != 3 {
		t.Errorf("order changed: %+v", out)
	}
}

func TestResolveDisplayValueUsesLookupWithRawFallback(t *testing.T) {
	r := ResolvedCustomField{FieldFormat: "version", Value: "3"}
	r.ResolveDisplayValue(func(raw string) string {
		if raw == "3" {
			return "v2.0"
		}
		return ""
	})
	if r.DisplayValue != "v2.0" {
		t.Errorf("display_value = %q; want v2.0", r.DisplayValue)
	}

	r2 := ResolvedCustomField{FieldFormat: "version", Value: "999"} // 削除済みバージョン等
	r2.ResolveDisplayValue(func(string) string { return "" })
	if r2.DisplayValue != "999" {
		t.Errorf("display_value = %q; want raw fallback 999", r2.DisplayValue)
	}
}

func TestResolveDisplayValueMultipleUsers(t *testing.T) {
	r := ResolvedCustomField{FieldFormat: "user", Multiple: true, Value: []any{"2", "5"}}
	names := map[string]string{"2": "Alice", "5": "Bob"}
	r.ResolveDisplayValue(func(raw string) string { return names[raw] })
	if r.DisplayValue != "Alice、Bob" {
		t.Errorf("display_value = %q; want Alice、Bob", r.DisplayValue)
	}
}
