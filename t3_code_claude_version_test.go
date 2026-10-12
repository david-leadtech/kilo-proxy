package main

import "testing"

func TestT3CodeNativeClaudeVersionMinimums(t *testing.T) {
	for _, test := range []struct{ id, before, minimum string }{
		{"anthropic/claude-opus-5.5", "2.1.279", "2.1.280"},
		{"anthropic/claude-sonnet-5-5-20261001", "2.1.283", "2.1.284"},
		{"anthropic/claude-fable-5.1", "2.1.256", "2.1.257"},
	} {
		library := emptyModelLibrary()
		library.Models = []modelLibraryItem{{ID: test.id}}
		for _, version := range []string{"", "unknown", test.before} {
			if err := validateT3CodeNativeClaudeVersion(library, claudeCaps(version)); err == nil {
				t.Fatalf("%s accepted incompatible CLI %q", test.id, version)
			}
		}
		for _, version := range []string{test.minimum, "2.1.288", "3.0.0"} {
			if err := validateT3CodeNativeClaudeVersion(library, claudeCaps(version)); err != nil {
				t.Fatalf("%s rejected compatible CLI %q: %v", test.id, version, err)
			}
		}
	}
}
