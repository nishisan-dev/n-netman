// Package netlink provides wrappers for managing Linux netlink policy rules.
package netlink

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

// RulePriority is the default priority for n-netman policy rules.
// Using 100 to leave room for higher-priority system rules.
const RulePriority = 100

// EnsureRuleByInterface creates policy rules for a bridge interface.
// Creates both iif (input) and oif (output) rules pointing to the table.
func EnsureRuleByInterface(bridgeName string, table int) error {
	// Verify interface exists
	_, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to find interface %s: %w", bridgeName, err)
	}

	// Create iif rule: ip rule add iif <bridge> lookup <table>
	iifRule := &netlink.Rule{
		Family:   netlink.FAMILY_V4,
		IifName:  bridgeName,
		Table:    table,
		Priority: RulePriority,
	}
	if err := ensureRule(iifRule); err != nil {
		return fmt.Errorf("failed to create iif rule for %s: %w", bridgeName, err)
	}

	// Create oif rule: ip rule add oif <bridge> lookup <table>
	oifRule := &netlink.Rule{
		Family:   netlink.FAMILY_V4,
		OifName:  bridgeName,
		Table:    table,
		Priority: RulePriority + 1,
	}
	if err := ensureRule(oifRule); err != nil {
		return fmt.Errorf("failed to create oif rule for %s: %w", bridgeName, err)
	}

	return nil
}

// DeleteRulesByInterface removes policy rules for a bridge interface.
func DeleteRulesByInterface(bridgeName string, table int) error {
	// Delete iif rule
	iifRule := &netlink.Rule{
		IifName:  bridgeName,
		Table:    table,
		Priority: RulePriority,
	}
	if err := netlink.RuleDel(iifRule); err != nil {
		// Ignore "no such process" error (rule doesn't exist)
		if err.Error() != "no such process" {
			return fmt.Errorf("failed to delete iif rule for %s: %w", bridgeName, err)
		}
	}

	// Delete oif rule
	oifRule := &netlink.Rule{
		OifName:  bridgeName,
		Table:    table,
		Priority: RulePriority + 1,
	}
	if err := netlink.RuleDel(oifRule); err != nil {
		if err.Error() != "no such process" {
			return fmt.Errorf("failed to delete oif rule for %s: %w", bridgeName, err)
		}
	}

	return nil
}

// ensureRule creates a rule if it doesn't exist.
func ensureRule(rule *netlink.Rule) error {
	// Check if rule already exists
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}

	for _, r := range rules {
		if rulesEqual(&r, rule) {
			return nil // Rule already exists
		}
	}

	// Create the rule
	if err := netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("failed to add rule: %w", err)
	}

	return nil
}

// rulesEqual checks if two rules are functionally equivalent.
func rulesEqual(a, b *netlink.Rule) bool {
	return a.Table == b.Table &&
		a.IifName == b.IifName &&
		a.OifName == b.OifName &&
		a.Priority == b.Priority
}

// ListRulesByTable returns all rules pointing to a specific table.
func ListRulesByTable(table int) ([]netlink.Rule, error) {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("failed to list rules: %w", err)
	}

	var result []netlink.Rule
	for _, r := range rules {
		if r.Table == table {
			result = append(result, r)
		}
	}
	return result, nil
}
