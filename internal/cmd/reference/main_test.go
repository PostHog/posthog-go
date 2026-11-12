package main

import (
	"slices"
	"strings"
	"testing"
)

func TestMigrationPageTitlesOneGuideWithItsH1(t *testing.T) {
	head, body := migrationPage([]string{"# Migrate the Go SDK from 1.x.x to 2.0.0\n\nIntro."})
	if !strings.Contains(head, `title: "Migrate the Go SDK from 1.x.x to 2.0.0"`) {
		t.Fatalf("head = %q", head)
	}
	if !slices.Equal(body, []string{"Intro."}) {
		t.Fatalf("body = %q", body)
	}
}

func TestMigrationPageKeepsSeveralGuides(t *testing.T) {
	guides := []string{"# Guide 2\n\nB.", "# Guide 1\n\nA."}
	head, body := migrationPage(slices.Clone(guides))
	if !strings.Contains(head, `title: "PostHog Go SDK migration guides"`) {
		t.Fatalf("head = %q", head)
	}
	if !slices.Equal(body, guides) {
		t.Fatalf("body = %q", body)
	}
}
