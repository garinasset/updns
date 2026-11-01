package nftset

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// NFTSetManager handles operations with nftables sets
type NFTSetManager struct {
	enabled bool
}

// NewNFTSetManager creates a new nftset manager
func NewNFTSetManager() *NFTSetManager {
	return &NFTSetManager{
		enabled: true,
	}
}

// AddIPToSet adds an IP address to an nftables set
func (m *NFTSetManager) AddIPToSet(ip string, setName string) error {
	if !m.enabled {
		return nil
	}

	// Validate IP
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}

	family, table, name, err := parseSetReference(setName)
	if err != nil {
		return err
	}

	cmd := exec.Command("nft", "add", "element", family, table, name, "{", ip, "}")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add %s to nftset %s: %w: %s", ip, setName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// AddIPsToSet adds multiple IP addresses to an nftables set
func (m *NFTSetManager) AddIPsToSet(ips []string, setName string) error {
	if !m.enabled || len(ips) == 0 {
		return nil
	}

	// Validate all IPs first
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("invalid IP address: %s", ip)
		}
	}

	family, table, name, err := parseSetReference(setName)
	if err != nil {
		return err
	}

	args := []string{"add", "element", family, table, name, "{"}
	for i, ip := range ips {
		args = append(args, ip)
		if i < len(ips)-1 {
			args = append(args, ",")
		}
	}
	args = append(args, "}")

	cmd := exec.Command("nft", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add IPs to nftset %s: %w: %s", setName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// RemoveIPFromSet removes an IP address from an nftables set
func (m *NFTSetManager) RemoveIPFromSet(ip string, setName string) error {
	if !m.enabled {
		return nil
	}

	family, table, name, err := parseSetReference(setName)
	if err != nil {
		return err
	}

	cmd := exec.Command("nft", "delete", "element", family, table, name, "{", ip, "}")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove %s from nftset %s: %w: %s", ip, setName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// FlushSet removes all entries from an nftables set
func (m *NFTSetManager) FlushSet(setName string) error {
	if !m.enabled {
		return nil
	}

	family, table, name, err := parseSetReference(setName)
	if err != nil {
		return err
	}

	cmd := exec.Command("nft", "flush", "set", family, table, name)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to flush nftset %s: %w: %s", setName, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// GetSetContents returns the current contents of an nftables set
func (m *NFTSetManager) GetSetContents(setName string) ([]string, error) {
	if !m.enabled {
		return []string{}, nil
	}

	family, table, name, err := parseSetReference(setName)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("nft", "list", "set", family, table, name)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list nftset %s: %w", setName, err)
	}

	return parseSetElements(string(output)), nil
}

func parseSetReference(reference string) (family, table, name string, err error) {
	parts := strings.Split(reference, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", "", fmt.Errorf("nftables set name must not be empty")
		}
		return "inet", "fw4", parts[0], nil
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", fmt.Errorf("invalid nftables set reference %q; expected family/table/set", reference)
		}
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("invalid nftables set reference %q; expected set or family/table/set", reference)
	}
}

func parseSetElements(output string) []string {
	var ips []string
	var elements strings.Builder
	inElements := false
	for _, line := range strings.Split(output, "\n") {
		if !inElements {
			index := strings.Index(line, "elements")
			if index < 0 {
				continue
			}
			line = line[index+len("elements"):]
			if equals := strings.IndexByte(line, '='); equals >= 0 {
				line = line[equals+1:]
			}
			inElements = true
		}
		elements.WriteString(line)
		elements.WriteByte(' ')
		if strings.Contains(line, "}") {
			break
		}
	}
	if !inElements {
		return ips
	}
	for _, value := range strings.FieldsFunc(elements.String(), func(r rune) bool {
		return r == '{' || r == '}' || r == ',' || r == ' ' || r == '\t'
	}) {
		if ip := net.ParseIP(value); ip != nil {
			ips = append(ips, ip.String())
		}
	}
	return ips
}

// SetEnabled enables or disables nftset operations
func (m *NFTSetManager) SetEnabled(enabled bool) {
	m.enabled = enabled
}

// IsEnabled returns whether nftset operations are enabled
func (m *NFTSetManager) IsEnabled() bool {
	return m.enabled
}
