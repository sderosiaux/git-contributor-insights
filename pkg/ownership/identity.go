package ownership

import (
	"sort"
	"strings"
)

func appendUniqueEmail(emails []string, email string) []string {
	email = strings.TrimSpace(email)
	if email == "" {
		return emails
	}
	for _, existing := range emails {
		if strings.EqualFold(existing, email) {
			return emails
		}
	}
	return append(emails, email)
}

// groupPeople is a read-time projection over immutable email-based attribution.
// It reuses existing SQLite snapshots and keeps organizations tied to the email
// and date on each original commit. The complete frozen identity set determines
// groups, so a person has the same ID across scopes, dates and branch histories.
func (e *engine) personGroups() (map[string]string, map[string]Person) {
	identities := make([]Person, 0, len(e.identities))
	for _, person := range e.identities {
		identities = append(identities, person)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].ID < identities[j].ID })
	byName := map[string]string{}
	resolved := map[string]string{}
	people := map[string]Person{}
	for _, person := range identities {
		name := strings.Join(strings.Fields(person.Name), " ")
		key := strings.ToLower(name)
		id := person.ID
		// Missing names provide no evidence that two email identities match.
		if key != "" {
			if previous, exists := byName[key]; exists {
				id = previous
			} else {
				byName[key] = id
			}
		}
		resolved[person.ID] = id
		group, exists := people[id]
		if !exists {
			group = Person{ID: id, Name: name, Email: person.Email}
		} else {
			group.IdentityBasis = "matching-name"
		}
		group.AliasIDs = append(group.AliasIDs, person.ID)
		group.Emails = appendUniqueEmail(group.Emails, person.Email)
		for _, email := range person.Emails {
			group.Emails = appendUniqueEmail(group.Emails, email)
		}
		people[id] = group
	}
	return resolved, people
}

func (e *engine) groupPeople(data *Dataset) {
	resolved, people := e.personGroups()
	resolve := func(id string) string {
		if group, exists := resolved[id]; exists {
			return group
		}
		return id
	}
	visible := map[string]bool{}
	for _, person := range data.People {
		visible[resolve(person.ID)] = true
	}
	data.People = []Person{}
	for id := range visible {
		person := people[id]
		sort.Strings(person.Emails)
		data.People = append(data.People, person)
	}
	sort.Slice(data.People, func(i, j int) bool { return data.People[i].ID < data.People[j].ID })
	for s := range data.Snapshots {
		for f := range data.Snapshots[s].Files {
			file := &data.Snapshots[s].Files[f]
			values := map[string]Contribution{}
			seen := map[string]map[string]bool{}
			for _, contribution := range file.Contributions {
				id := resolve(contribution.PersonID)
				key := id + "\x00" + contribution.OrganizationID
				value, exists := values[key]
				if !exists {
					value = Contribution{PersonID: id, OrganizationID: contribution.OrganizationID, Evidence: []Evidence{}}
					seen[key] = map[string]bool{}
				}
				value.Lines += contribution.Lines
				value.ChangedLines += contribution.ChangedLines
				for _, evidence := range contribution.Evidence {
					evidenceKey := evidence.Kind + "\x00" + evidence.SHA + "\x00" + evidence.Path
					if !seen[key][evidenceKey] {
						value.Evidence = append(value.Evidence, evidence)
						seen[key][evidenceKey] = true
					}
				}
				values[key] = value
			}
			file.Contributions = make([]Contribution, 0, len(values))
			for _, value := range values {
				file.Contributions = append(file.Contributions, value)
			}
			sort.Slice(file.Contributions, func(i, j int) bool {
				a, b := file.Contributions[i], file.Contributions[j]
				return a.PersonID+"\x00"+a.OrganizationID < b.PersonID+"\x00"+b.OrganizationID
			})
		}
	}
	for n := range data.Series {
		point := &data.Series[n]
		values := map[string]SeriesContribution{}
		for _, contribution := range point.Contributions {
			contribution.PersonID = resolve(contribution.PersonID)
			key := contributionKey(contribution)
			value := values[key]
			value.PersonID, value.OrganizationID = contribution.PersonID, contribution.OrganizationID
			value.Lines += contribution.Lines
			value.ChangedLines += contribution.ChangedLines
			values[key] = value
		}
		point.Contributions = make([]SeriesContribution, 0, len(values))
		for _, value := range values {
			point.Contributions = append(point.Contributions, value)
		}
		sort.Slice(point.Contributions, func(i, j int) bool {
			return contributionKey(point.Contributions[i]) < contributionKey(point.Contributions[j])
		})
	}
	data.Method.Identity += " Displayed people group canonical email identities with the same non-empty Git display name, ignoring case and repeated whitespace. This is a name-matching heuristic, not verified identity; homonyms may be grouped. All email aliases remain available and historical organization attribution is unchanged. Stored attribution remains email-based."
}
