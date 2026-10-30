package aisession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
)

// RetainKnowledge compares semantic tool grants across native binding spellings.
// Callers must still freshly validate runtime/provider identity and availability.
// Missing permissions stop execution; they are never restored by broad grants.
func RetainKnowledge(previous, next aipolicy.Resolution) error {
	before, err := knowledgeSet(previous)
	if err != nil {
		return err
	}
	after, err := knowledgeSet(next)
	if err != nil {
		return err
	}
	missing := []string{}
	for key := range before {
		if !after[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("knowledge permissions would be reduced (%s); reconnect the scoped vault/memory bindings and revalidate the target before continuing", strings.Join(missing, ", "))
	}
	return nil
}

func knowledgeSet(resolution aipolicy.Resolution) (map[string]bool, error) {
	// Use the native adapter's allowlist so normalization cannot authorize a tool
	// that the resolver would reject, including wildcard or destructive tools.
	if _, err := aipolicy.KnowledgeArgs(aipolicy.Runtime{Agent: resolution.Agent}, resolution.KnowledgeApprovals); err != nil {
		return nil, fmt.Errorf("invalid knowledge grants; revalidate the target: %w", err)
	}
	result := map[string]bool{}
	for _, grant := range resolution.KnowledgeApprovals {
		scope := "memory"
		if grant.Server == "vmem" {
			scope = "vault"
		}
		result[scope+"/"+grant.Tool] = true
	}
	return result, nil
}
