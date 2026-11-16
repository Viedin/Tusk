package db

import (
	"strings"
	"testing"
)

func TestValidateChanges(t *testing.T) {
	text := "exact value"
	valid := RowChange{Original: []*string{&text, nil}, Values: map[string]*string{"note": nil}}
	if err := ValidateChanges([]string{"id", "note"}, []RowChange{valid}); err != nil {
		t.Fatal(err)
	}
	for name, changes := range map[string][]RowChange{
		"empty":                   nil,
		"missing snapshot":        {{Original: []*string{&text}, Values: map[string]*string{"note": &text}}},
		"no change":               {{Original: valid.Original}},
		"unknown column":          {{Original: valid.Original, Values: map[string]*string{"absent": &text}}},
		"mixed delete and update": {{Original: valid.Original, Values: valid.Values, Delete: true}},
		"too many rows":           make([]RowChange, QueryRowLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateChanges([]string{"id", "note"}, changes); err == nil {
				t.Fatal("accepted invalid changes")
			}
		})
	}
	if err := ValidateChanges([]string{"id", "id"}, []RowChange{valid}); err == nil {
		t.Fatal("accepted duplicate columns")
	}
	large := strings.Repeat("x", 10<<20)
	if err := ValidateChanges([]string{"id"}, []RowChange{{Original: []*string{&large}, Delete: true}}); err == nil {
		t.Fatal("accepted oversized changes")
	}
}
