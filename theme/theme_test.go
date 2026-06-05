package theme_test

import (
	"testing"

	"github.com/anutron/argus-sdk/theme"
)

func TestPRIconConstants(t *testing.T) {
	if theme.IconReview == 0 {
		t.Error("IconReview is zero")
	}
	if theme.IconPRAwaiting == 0 {
		t.Error("IconPRAwaiting is zero")
	}
	if theme.IconPRChanges == 0 {
		t.Error("IconPRChanges is zero")
	}
	if theme.IconPRApproved == 0 {
		t.Error("IconPRApproved is zero")
	}
}

func TestPRColorsDefined(t *testing.T) {
	_ = theme.ColorPRAwaiting
	_ = theme.ColorPRChanges
	_ = theme.ColorPRApproved
}

func TestPRStylesDefined(t *testing.T) {
	_ = theme.StylePRAwaiting
	_ = theme.StylePRChanges
	_ = theme.StylePRApproved
}
