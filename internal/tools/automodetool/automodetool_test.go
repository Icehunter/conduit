package automodetool_test

import (
	"context"
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tools/automodetool"
)

func TestEnterAutoMode_SetsMode(t *testing.T) {
	var got permissions.Mode
	tool := &automodetool.EnterAutoMode{
		SetMode:     func(m permissions.Mode) { got = m },
		CurrentMode: func() permissions.Mode { return permissions.ModeDefault },
		AskEnter:    func(_ context.Context) bool { return true },
	}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if got != permissions.ModeBypassPermissions {
		t.Errorf("mode = %v; want ModeBypassPermissions", got)
	}
}

func TestEnterAutoMode_UserDeclines(t *testing.T) {
	called := false
	tool := &automodetool.EnterAutoMode{
		SetMode:     func(_ permissions.Mode) { called = true },
		CurrentMode: func() permissions.Mode { return permissions.ModeDefault },
		AskEnter:    func(_ context.Context) bool { return false },
	}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected error result when user declines")
	}
	if called {
		t.Error("SetMode should not be called when user declines")
	}
}

func TestEnterAutoMode_AlreadyAutoSkipsPrompt(t *testing.T) {
	var asked bool
	tool := &automodetool.EnterAutoMode{
		SetMode:     func(_ permissions.Mode) {},
		CurrentMode: func() permissions.Mode { return permissions.ModeBypassPermissions },
		AskEnter:    func(_ context.Context) bool { asked = true; return false },
	}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("unexpected error when already in auto mode: %s", res.Content)
	}
	if asked {
		t.Error("AskEnter should not be called when already in auto mode")
	}
}

func TestEnterAutoMode_Metadata(t *testing.T) {
	tool := &automodetool.EnterAutoMode{}
	if tool.Name() != "EnterAutoMode" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("EnterAutoMode should be read-only")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("EnterAutoMode should be concurrency safe")
	}
}

func TestExitAutoMode_SetsDefaultMode(t *testing.T) {
	var got permissions.Mode
	tool := &automodetool.ExitAutoMode{
		SetMode: func(m permissions.Mode) { got = m },
	}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if got != permissions.ModeDefault {
		t.Errorf("mode = %v; want ModeDefault", got)
	}
}

func TestExitAutoMode_Metadata(t *testing.T) {
	tool := &automodetool.ExitAutoMode{}
	if tool.Name() != "ExitAutoMode" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("ExitAutoMode should be read-only")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("ExitAutoMode should be concurrency safe")
	}
}
