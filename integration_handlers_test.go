package main

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestRoundDownTen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		diff string
	}{
		{name: "rounds down", in: "128.99", want: "120", diff: "8"},
		{name: "already multiple of ten", in: "130", want: "130", diff: "0"},
		{name: "below ten", in: "8.75", want: "0", diff: "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := decimal.NewFromString(tc.in)
			got, diff := roundDownTen(in)
			if got.String() != tc.want || diff.String() != tc.diff {
				t.Fatalf("roundDownTen(%s) = (%s, %s), want (%s, %s)", tc.in, got, diff, tc.want, tc.diff)
			}
		})
	}
}

func TestNormalizeBDMobile(t *testing.T) {
	valid := []string{"01712345678", "+8801712345678", "8801712345678", "017-1234-5678"}
	for _, value := range valid {
		got, ok := normalizeBDMobile(value)
		if !ok || got != "01712345678" {
			t.Errorf("normalizeBDMobile(%q) = (%q, %v), want (01712345678, true)", value, got, ok)
		}
	}
	invalid := []string{"01212345678", "0171234567", "not-a-number"}
	for _, value := range invalid {
		if _, ok := normalizeBDMobile(value); ok {
			t.Errorf("normalizeBDMobile(%q) unexpectedly accepted", value)
		}
	}
}

func TestWorkflowSessionKey(t *testing.T) {
	got := workflowSessionKey("sell", 123456789)
	want := "workflow:sell:123456789"
	if got != want {
		t.Fatalf("workflowSessionKey() = %q, want %q", got, want)
	}
}

func TestDecimalFromPositive(t *testing.T) {
	if got, err := decimalFromPositive("0.25"); err != nil || got != "0.25" {
		t.Fatalf("decimalFromPositive valid input = (%q, %v)", got, err)
	}
	for _, value := range []string{"0", "-1", "abc"} {
		if _, err := decimalFromPositive(value); err == nil {
			t.Errorf("decimalFromPositive(%q) should fail", value)
		}
	}
}
