package main

import (
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/taimport"
)

func TestFormatChannelResult_realRun(t *testing.T) {
	res := taimport.ChannelResult{
		Subscribed: 12, Active: 10, Inactive: 2, Skipped: 37,
		InactiveNames: []string{"Dead Channel", "Gone Too"},
	}

	got := formatChannelResult(res, false)

	for _, want := range []string{"12", "10", "2", "37", "Dead Channel", "Gone Too"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("real run mentioned a dry run:\n%s", got)
	}
}

func TestFormatChannelResult_dryRunSaysSo(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 3, Active: 3}

	got := formatChannelResult(res, true)

	if !strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("dry run not labelled:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "nothing was written") {
		t.Errorf("dry run did not say nothing was written:\n%s", got)
	}
}

func TestFormatChannelResult_noInactiveOmitsTheList(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 5, Active: 5}

	got := formatChannelResult(res, false)

	if strings.Contains(strings.ToLower(got), "inactive channel") {
		t.Errorf("listed inactive channels when there are none:\n%s", got)
	}
}

func TestRunImportChannels_requiresURLAndToken(t *testing.T) {
	if err := runImportChannels([]string{}); err == nil {
		t.Fatal("err = nil, want an error when --ta-url is missing")
	}
	if err := runImportChannels([]string{"--ta-url", "http://ta:8000"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-token is missing")
	}
}
