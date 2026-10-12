// Package ownership extracts reproducible ownership snapshots from local Git history.
package ownership

type Options struct {
	// Revision freezes a managed checkout at an exact commit and uses that
	// commit's mailmap. Empty preserves the local working-tree behavior.
	Revision   string
	Since      string
	Until      string
	ConfigPath string
	Snapshots  int
	Interval   string
	WindowDays int
	Workers    int
}

type Dataset struct {
	SeriesEncoding string         `json:"seriesEncoding,omitempty"`
	Repository     Repository     `json:"repository"`
	People         []Person       `json:"people"`
	Organizations  []Organization `json:"organizations"`
	Snapshots      []Snapshot     `json:"snapshots"`
	Method         Method         `json:"method"`
	Warnings       []string       `json:"warnings"`
	Timeline       Timeline       `json:"timeline"`
	Series         []SeriesPoint  `json:"series,omitempty"`
	Commit         *Commit        `json:"commit,omitempty"`
}

// Timeline describes real evaluation dates, never interpolated data values.
type Timeline struct {
	Interval   string `json:"interval"`
	Since      string `json:"since"`
	Until      string `json:"until"`
	Points     int    `json:"points"`
	Calendar   bool   `json:"calendar"`
	Complete   bool   `json:"complete"`
	Revisions  int    `json:"revisions"`
	Tip        string `json:"tip,omitempty"`
	Label      string `json:"label,omitempty"`
	TotalIndex int    `json:"totalIndex"`
}

type CommitFilter struct {
	Query  string
	From   string
	To     string
	Offset int
	Limit  int
}

type Commit struct {
	AI          []AISignal `json:"ai,omitempty"`
	SHA         string     `json:"sha"`
	Parents     []string   `json:"parents"`
	Date        string     `json:"date"`
	AuthorName  string     `json:"authorName"`
	AuthorEmail string     `json:"authorEmail"`
	Summary     string     `json:"summary"`
	Mainline    bool       `json:"mainline"`
	Refs        []string   `json:"refs"`
	Lines       int        `json:"lines"`
	Files       int        `json:"files"`
}

type CommitPage struct {
	Commits []Commit `json:"commits"`
	Total   int      `json:"total"`
	Offset  int      `json:"offset"`
	Limit   int      `json:"limit"`
	HasMore bool     `json:"hasMore"`
}

type SeriesPoint struct {
	Date          string               `json:"date"`
	CommittedDate string               `json:"committedDate,omitempty"`
	Revision      string               `json:"revision"`
	Kind          string               `json:"kind"`
	Lines         int                  `json:"lines"`
	Contributions []SeriesContribution `json:"contributions"`
}

type SeriesContribution struct {
	PersonID       string `json:"personId"`
	OrganizationID string `json:"organizationId"`
	Lines          int    `json:"lines"`
	ChangedLines   int    `json:"changedLines"`
}

type Repository struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Head    string `json:"head"`
	Remote  string `json:"remote"`
	Shallow bool   `json:"shallow"`
}

type Person struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Emails        []string `json:"emails,omitempty"`
	AliasIDs      []string `json:"aliasIds,omitempty"`
	IdentityBasis string   `json:"identityBasis,omitempty"`
}

type Organization struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Basis string `json:"basis"`
}

type Method struct {
	Ownership   string   `json:"ownership"`
	Activity    string   `json:"activity"`
	Affiliation string   `json:"affiliation"`
	Identity    string   `json:"identity"`
	Exclusions  []string `json:"exclusions"`
	WindowDays  int      `json:"windowDays"`
}

type Snapshot struct {
	Date          string         `json:"date"`
	Revision      string         `json:"revision"`
	Files         []File         `json:"files"`
	ExcludedFiles int            `json:"excludedFiles"`
	Exclusions    map[string]int `json:"exclusions"`
	Warnings      []string       `json:"warnings"`
}

type File struct {
	Path          string         `json:"path"`
	Lines         int            `json:"lines"`
	Contributions []Contribution `json:"contributions"`
}

type Contribution struct {
	PersonID       string     `json:"personId"`
	OrganizationID string     `json:"organizationId"`
	Lines          int        `json:"lines"`
	ChangedLines   int        `json:"changedLines"`
	Evidence       []Evidence `json:"evidence"`
}

type Evidence struct {
	SHA     string `json:"sha"`
	Path    string `json:"path,omitempty"`
	Date    string `json:"date"`
	Summary string `json:"summary"`
	Kind    string `json:"kind"`
}
