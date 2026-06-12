// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

// Package rtk is an in-process port of RTK (Rust Token Killer).
// It intercepts tool output from BashTool and compresses it before the agent
// sees it, saving 60-90% of tokens on common dev operations.
//
// Architecture mirrors RTK's Rust source at /Volumes/Engineering/Icehunter/rtk/src/:
//   - registry.go   — command classification (ports discover/registry.rs)
//   - filters/      — per-category output transformers (ports cmds/*)
//   - ansi.go       — ANSI escape stripping (ports core/utils.rs)
package rtk

import (
	"log"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/ccr"
)

// defaultCCRStore is the lazily-initialized package-level CCR store.
var (
	ccrOnce  sync.Once
	ccrStore *ccr.Store
)

func getStore() *ccr.Store {
	ccrOnce.Do(func() {
		ccrStore = ccr.DefaultStore()
	})
	return ccrStore
}

// Result is returned by Filter.
type Result struct {
	Original   string
	Filtered   string
	SavedBytes int
	SavingsPct float64
	Category   string
	// Handle is set when the original output was stored in the CCR store.
	// Format: "ccr:<key>". Empty when no compression occurred.
	Handle string
}

// IsClassified returns true if the command is handled by a RTK filter rule.
// Used by /rtk discover to find unhandled commands.
func IsClassified(cmd string) bool {
	return classify(strings.TrimSpace(cmd)) != nil
}

// Filter applies RTK compression to the output of the given shell command.
// cmd is the full command string; output is its combined stdout+stderr.
// Returns the (possibly compressed) output and metadata.
func Filter(cmd, output string) Result {
	cmd = strings.TrimSpace(cmd)
	output = stripANSI(output)
	orig := len(output)

	result := Result{Original: output, Filtered: output}

	// Apply command-rule compression if a rule is registered for this command.
	if rule := classify(cmd); rule != nil {
		filtered := rule.filter(cmd, output)
		comp := len(filtered)
		saved := orig - comp
		pct := 0.0
		if orig > 0 {
			pct = float64(saved) / float64(orig) * 100
		}
		result.Filtered = filtered
		result.SavedBytes = saved
		result.SavingsPct = pct
		result.Category = rule.category

		// Store the original output in CCR so the agent can retrieve it later.
		// We store the pre-filter content to give access to the full uncompressed stream.
		if saved > 0 {
			handle, err := getStore().Put(output)
			if err != nil {
				log.Printf("rtk: ccr put failed; handle not set: %v", err)
			} else {
				result.Handle = handle
			}
		}
	}

	// SmartCrusher: content-based JSON compression, fires as fallback.
	// Runs whether or not a command rule matched, as long as the output
	// was not already heavily compressed (SavedBytes >= 50% of original).
	if result.SavedBytes*2 < orig {
		if crushedOut, ok := applySmartCrusher(cmd, result.Filtered, getStore()); ok {
			result.Filtered = crushedOut
			// Re-compute savings against the original input.
			comp := len(result.Filtered)
			saved := orig - comp
			if saved > result.SavedBytes {
				result.SavedBytes = saved
				result.SavingsPct = float64(saved) / float64(orig) * 100
			}
			// Handle from SmartCrusher is embedded in footer; clear the RTK handle
			// to avoid a duplicate footer from bashtool.
			result.Handle = ""
		}
	}

	return result
}
