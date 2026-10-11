package ownership

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const unknownOrganization = "unknown"

type affiliationConfig struct {
	Vendors map[string]struct {
		Domains         []string `yaml:"domains"`
		GithubCompanies []string `yaml:"github_companies"`
	} `yaml:"vendors"`
	People []struct {
		Email        string `yaml:"email"`
		Affiliations []struct {
			Organization string `yaml:"organization"`
			From         string `yaml:"from"`
			Until        string `yaml:"until"`
		} `yaml:"affiliations"`
	} `yaml:"people"`
}

type affiliationPeriod struct {
	organization string
	from, until  time.Time
}

type affiliations struct {
	domains       map[string]string
	people        map[string][]affiliationPeriod
	organizations map[string]Organization
}

func loadAffiliations(filename string) (*affiliations, error) {
	a := &affiliations{
		domains: map[string]string{}, people: map[string][]affiliationPeriod{},
		organizations: map[string]Organization{unknownOrganization: {ID: unknownOrganization, Name: "Unknown affiliation", Basis: "No configured historical affiliation or matched email domain"}},
	}
	if filename == "" {
		return a, nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read affiliation configuration: %w", err)
	}
	var config affiliationConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse affiliation configuration: %w", err)
	}
	names := make([]string, 0, len(config.Vendors))
	for name := range config.Vendors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" || name == unknownOrganization {
			return nil, fmt.Errorf("invalid organization name %q", name)
		}
		a.organizations[name] = Organization{ID: name, Name: name, Basis: "Configured email domain"}
		for _, value := range config.Vendors[name].Domains {
			domain := strings.ToLower(strings.TrimSpace(value))
			if domain == "" || strings.Contains(domain, "@") {
				return nil, fmt.Errorf("invalid email domain %q for %s", value, name)
			}
			if previous, exists := a.domains[domain]; exists && previous != name {
				return nil, fmt.Errorf("email domain %q belongs to both %q and %q", domain, previous, name)
			}
			a.domains[domain] = name
		}
	}
	for _, person := range config.People {
		email := strings.ToLower(strings.TrimSpace(person.Email))
		if email == "" {
			return nil, fmt.Errorf("historical affiliation requires a person email")
		}
		if _, exists := a.people[email]; exists {
			return nil, fmt.Errorf("duplicate historical affiliation person %q", email)
		}
		a.people[email] = []affiliationPeriod{}
		for _, affiliation := range person.Affiliations {
			name := strings.TrimSpace(affiliation.Organization)
			if name == "" {
				return nil, fmt.Errorf("historical affiliation for %s requires an organization", email)
			}
			period := affiliationPeriod{organization: name}
			if affiliation.From != "" {
				period.from, err = parseDate(affiliation.From, false)
				if err != nil {
					return nil, fmt.Errorf("affiliation from for %s: %w", email, err)
				}
			}
			if affiliation.Until != "" {
				// Historical periods are [from, until), including date-only boundaries.
				period.until, err = parseDate(affiliation.Until, false)
				if err != nil {
					return nil, fmt.Errorf("affiliation until for %s: %w", email, err)
				}
			}
			if !period.from.IsZero() && !period.until.IsZero() && !period.from.Before(period.until) {
				return nil, fmt.Errorf("affiliation for %s has an empty or reversed date range", email)
			}
			a.people[email] = append(a.people[email], period)
			if name != unknownOrganization {
				basis := "Configured historical affiliation"
				if existing, ok := a.organizations[name]; ok && strings.Contains(existing.Basis, "email domain") {
					basis = "Configured email domain or historical affiliation"
				}
				a.organizations[name] = Organization{ID: name, Name: name, Basis: basis}
			}
		}
		periods := a.people[email]
		sort.Slice(periods, func(i, j int) bool { return periods[i].from.Before(periods[j].from) })
		for i := 1; i < len(periods); i++ {
			if periods[i-1].until.IsZero() || periods[i].from.Before(periods[i-1].until) {
				return nil, fmt.Errorf("overlapping historical affiliations for %s", email)
			}
		}
	}
	return a, nil
}

// Historical configuration overrides domain inference, including gaps: a gap is
// unknown rather than an invitation to apply the person's current employer.
func (a *affiliations) organization(c commitInfo) string {
	for _, email := range []string{strings.ToLower(c.email), strings.ToLower(c.rawEmail)} {
		if periods, exists := a.people[email]; exists {
			for _, period := range periods {
				if (period.from.IsZero() || !c.authored.Before(period.from)) && (period.until.IsZero() || c.authored.Before(period.until)) {
					return period.organization
				}
			}
			return unknownOrganization
		}
	}
	parts := strings.Split(strings.ToLower(c.rawEmail), "@")
	if len(parts) == 2 {
		if organization, ok := a.domains[parts[1]]; ok {
			return organization
		}
	}
	return unknownOrganization
}

func parseDate(value string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q; use YYYY-MM-DD or RFC3339", value)
}
