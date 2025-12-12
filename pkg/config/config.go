package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// VendorConfig represents configuration for identifying a vendor
type VendorConfig struct {
	Domains          []string `yaml:"domains"`
	GithubCompanies  []string `yaml:"github_companies"`
	Usernames        []string `yaml:"usernames"`
}

// Config represents the complete configuration file
type Config struct {
	Vendors map[string]VendorConfig `yaml:"vendors"`
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// GetVendorNames returns a list of all configured vendor names
func (c *Config) GetVendorNames() []string {
	names := make([]string, 0, len(c.Vendors))
	for name := range c.Vendors {
		names = append(names, name)
	}
	return names
}

// GetAllCategories returns all possible categories (vendors + community)
func (c *Config) GetAllCategories() []string {
	categories := c.GetVendorNames()
	return append(categories, "community")
}

// ClassifyByEmail classifies a contributor by email domain
func (c *Config) ClassifyByEmail(email string) string {
	if email == "" {
		return ""
	}

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}

	domain := strings.ToLower(parts[1])

	for vendorName, vendor := range c.Vendors {
		for _, d := range vendor.Domains {
			if strings.ToLower(d) == domain {
				return vendorName
			}
		}
	}

	return ""
}

// ClassifyByCompany classifies a contributor by GitHub company field
func (c *Config) ClassifyByCompany(company string) string {
	if company == "" {
		return ""
	}

	companyLower := strings.ToLower(strings.TrimSpace(company))

	for vendorName, vendor := range c.Vendors {
		for _, vendorCompany := range vendor.GithubCompanies {
			if strings.Contains(companyLower, strings.ToLower(vendorCompany)) {
				return vendorName
			}
		}
	}

	return ""
}

// ClassifyByUsername classifies a contributor by GitHub username
func (c *Config) ClassifyByUsername(username string) string {
	if username == "" {
		return ""
	}

	usernameLower := strings.ToLower(username)

	for vendorName, vendor := range c.Vendors {
		for _, u := range vendor.Usernames {
			if strings.ToLower(u) == usernameLower {
				return vendorName
			}
		}
	}

	return ""
}

// Classify classifies a contributor using all available signals
// Priority: username > email > company > community
// When no vendors are configured, automatically classifies by email domain
func (c *Config) Classify(email, company, username string) string {
	// If no vendors configured, use automatic domain classification
	if len(c.Vendors) == 0 {
		return AutoClassifyByDomain(email)
	}

	// Try username first (most explicit)
	if vendor := c.ClassifyByUsername(username); vendor != "" {
		return vendor
	}

	// Try email domain
	if vendor := c.ClassifyByEmail(email); vendor != "" {
		return vendor
	}

	// Try company field
	if vendor := c.ClassifyByCompany(company); vendor != "" {
		return vendor
	}

	// Default to community
	return "community"
}
