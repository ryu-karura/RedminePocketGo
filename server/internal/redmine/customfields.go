package redmine

import "strings"

// ResolvedCustomField はチケットのカスタムフィールド値と定義を id で突合した
// 結果（Design.md §6.4、§7.8）。DisplayValue は選択肢ラベル解決
// （list / key_value_list）または参照解決（version / user / attachment。
// httpapi が追加の上流呼び出しで埋める）が必要なフォーマットのみ持つ。
// それ以外（string/text/int/float/date/bool/link）は空のままとし、
// フロントが Value から直接整形する。
type ResolvedCustomField struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	FieldFormat    string          `json:"field_format,omitempty"`
	IsRequired     bool            `json:"is_required,omitempty"`
	Multiple       bool            `json:"multiple,omitempty"`
	MinLength      int             `json:"min_length,omitempty"`
	MaxLength      int             `json:"max_length,omitempty"`
	PossibleValues []PossibleValue `json:"possible_values,omitempty"`
	Value          any             `json:"value"`
	DisplayValue   string          `json:"display_value,omitempty"`
}

// MergeCustomFields はチケットのカスタムフィールド値を定義と id で突合する。
// defs が空（定義取得が失敗した場合。管理者権限なしなどで起こりうる。
// Design.md §6.4）でも、値だけの degrade 表示になる。表示順は values の
// 並び（Redmine が既にトラッカーの表示順で返す）をそのまま使い、
// 並べ替えない。
func MergeCustomFields(values []CustomFieldValue, defs []CustomFieldDef) []ResolvedCustomField {
	byID := make(map[int]CustomFieldDef, len(defs))
	for _, d := range defs {
		byID[d.ID] = d
	}
	out := make([]ResolvedCustomField, 0, len(values))
	for _, v := range values {
		r := ResolvedCustomField{ID: v.ID, Name: v.Name, Value: v.Value}
		if d, ok := byID[v.ID]; ok {
			r.FieldFormat = d.FieldFormat
			r.IsRequired = d.IsRequired
			r.Multiple = d.Multiple
			r.MinLength = d.MinLength
			r.MaxLength = d.MaxLength
			r.PossibleValues = d.PossibleValues
			if d.FieldFormat == "list" || d.FieldFormat == "key_value_list" {
				r.ResolveDisplayValue(func(raw string) string {
					return possibleValueLabel(d.PossibleValues, raw)
				})
			}
		}
		out = append(out, r)
	}
	return out
}

// ResolveDisplayValue は resolve(raw) で得た表示名を DisplayValue に埋める
// （複数値は「、」区切り）。resolve が空文字を返した値（削除済み参照や
// 未知の選択肢など）は生の値のまま表示する。
func (r *ResolvedCustomField) ResolveDisplayValue(resolve func(raw string) string) {
	raws := RawCustomFieldValues(r.Value)
	if len(raws) == 0 {
		return
	}
	labels := make([]string, 0, len(raws))
	for _, raw := range raws {
		if label := resolve(raw); label != "" {
			labels = append(labels, label)
		} else {
			labels = append(labels, raw)
		}
	}
	r.DisplayValue = strings.Join(labels, "、")
}

// RawCustomFieldValues は value（文字列 / 複数選択の配列 / nil）を
// 文字列列へ正規化する。
func RawCustomFieldValues(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func possibleValueLabel(choices []PossibleValue, raw string) string {
	for _, c := range choices {
		if c.Value == raw {
			return c.Label
		}
	}
	return ""
}
