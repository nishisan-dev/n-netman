// Package netlink provides wrappers for managing Linux netlink policy rules.
package netlink

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

// RulePriority is the default priority for n-netman policy rules.
// Using 100 to leave room for higher-priority system rules.
const RulePriority = 100

// EnsureRuleByInterface creates policy rules for a bridge interface.
// Creates both iif (input) and oif (output) rules pointing to the table.
//
// NOTE: We use 'ip rule add' command directly instead of vishvananda/netlink
// library's RuleAdd due to issues where RuleAdd returns "invalid argument"
// for rules with iif/oif. This is similar to the FDB workaround.
func EnsureRuleByInterface(bridgeName string, table int) error {
	// Verify interface exists
	_, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to find interface %s: %w", bridgeName, err)
	}

	// Create iif rule: ip rule add iif <bridge> lookup <table> priority <prio>
	if err := addRuleViaCLI("iif", bridgeName, table, RulePriority); err != nil {
		return fmt.Errorf("failed to create iif rule for %s: %w", bridgeName, err)
	}

	// Create oif rule: ip rule add oif <bridge> lookup <table> priority <prio>
	if err := addRuleViaCLI("oif", bridgeName, table, RulePriority+1); err != nil {
		return fmt.Errorf("failed to create oif rule for %s: %w", bridgeName, err)
	}

	return nil
}

// addRuleViaCLI adds a policy rule using the ip command.
func addRuleViaCLI(direction, ifname string, table, priority int) error {
	// ip rule add iif/oif <ifname> lookup <table> priority <priority>
	cmd := exec.Command("ip", "rule", "add",
		direction, ifname,
		"lookup", strconv.Itoa(table),
		"priority", strconv.Itoa(priority))

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		// Check if rule already exists
		if strings.Contains(outputStr, "File exists") {
			return nil
		}
		return fmt.Errorf("ip rule add failed: %s: %w", outputStr, err)
	}

	return nil
}

// DeleteRulesByInterface removes policy rules for a bridge interface.
func DeleteRulesByInterface(bridgeName string, table int) error {
	// Delete iif rule
	if err := deleteRuleViaCLI("iif", bridgeName, table, RulePriority); err != nil {
		return fmt.Errorf("failed to delete iif rule for %s: %w", bridgeName, err)
	}

	// Delete oif rule
	if err := deleteRuleViaCLI("oif", bridgeName, table, RulePriority+1); err != nil {
		return fmt.Errorf("failed to delete oif rule for %s: %w", bridgeName, err)
	}

	return nil
}

// deleteRuleViaCLI removes a policy rule using the ip command.
func deleteRuleViaCLI(direction, ifname string, table, priority int) error {
	cmd := exec.Command("ip", "rule", "del",
		direction, ifname,
		"lookup", strconv.Itoa(table),
		"priority", strconv.Itoa(priority))

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		// Ignore "No such process" (rule doesn't exist)
		if strings.Contains(outputStr, "No such process") ||
			strings.Contains(outputStr, "No such file or directory") {
			return nil
		}
		return fmt.Errorf("ip rule del failed: %s: %w", outputStr, err)
	}

	return nil
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
