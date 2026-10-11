package explorer

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

func TestCompactSeriesPreservesEveryStateAndActivityExpiry(t *testing.T) {
	a := ownership.SeriesContribution{PersonID: "a", OrganizationID: "old", Lines: 10, ChangedLines: 3}
	b := ownership.SeriesContribution{PersonID: "a", OrganizationID: "new", Lines: 2, ChangedLines: 1}
	bExpired := b
	bExpired.ChangedLines = 0
	data := &ownership.Dataset{Series: []ownership.SeriesPoint{
		{Date: "2020-01-01T00:00:00Z", Revision: "first", Kind: "revision", Lines: 10, Contributions: []ownership.SeriesContribution{a}},
		{Date: "2020-01-01T00:00:00Z", Revision: "same-time", Kind: "revision", Lines: 12, Contributions: []ownership.SeriesContribution{a, b}},
		{Date: "2020-01-02T00:00:00Z", Revision: "unchanged", Kind: "revision", Lines: 12, Contributions: []ownership.SeriesContribution{a, b}},
		{Date: "2020-01-03T00:00:00Z", Revision: "deleted", Kind: "revision", Lines: 2, Contributions: []ownership.SeriesContribution{b}},
		{Date: "2020-01-04T00:00:00Z", Revision: "deleted", Kind: "expiry", Lines: 2, Contributions: []ownership.SeriesContribution{bExpired}},
		{Date: "2020-01-05T00:00:00Z", Revision: "reintroduced", Kind: "revision", Lines: 12, Contributions: []ownership.SeriesContribution{a, bExpired}},
	}}
	before, _ := json.Marshal(data)
	encoded, err := encodeDataset(data, true)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		SeriesEncoding string                  `json:"seriesEncoding"`
		Series         []ownership.SeriesPoint `json:"series"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SeriesEncoding != "changes-v1" || len(wire.Series) != len(data.Series) {
		t.Fatalf("wrong encoding or missing points: %+v", wire)
	}
	if len(wire.Series[2].Contributions) != 0 {
		t.Fatal("unchanged attribution repeated")
	}
	if len(wire.Series[3].Contributions) != 1 || wire.Series[3].Contributions[0].Lines != 0 || wire.Series[3].Contributions[0].ChangedLines != 0 {
		t.Fatal("deleted actor missing explicit removal")
	}
	state := map[string]ownership.SeriesContribution{}
	for n, point := range wire.Series {
		expected := data.Series[n]
		if point.Date != expected.Date || point.Revision != expected.Revision || point.Kind != expected.Kind || point.Lines != expected.Lines {
			t.Fatalf("point %d metadata changed", n)
		}
		for _, value := range point.Contributions {
			key := value.PersonID + "\x00" + value.OrganizationID
			if value.Lines == 0 && value.ChangedLines == 0 {
				delete(state, key)
			} else {
				state[key] = value
			}
		}
		want := map[string]ownership.SeriesContribution{}
		for _, value := range expected.Contributions {
			want[value.PersonID+"\x00"+value.OrganizationID] = value
		}
		if !reflect.DeepEqual(state, want) {
			t.Fatalf("point %d reconstructed %+v, want %+v", n, state, want)
		}
	}
	after, _ := json.Marshal(data)
	if string(before) != string(after) {
		t.Fatal("HTTP encoding mutated the dense export")
	}
}

func TestEncodeAlreadyCompactDatasetPreservesUnchangedPairs(t *testing.T) {
	data := &ownership.Dataset{SeriesEncoding: "changes-v1", Series: []ownership.SeriesPoint{
		{Date: "2020-01-01", Contributions: []ownership.SeriesContribution{{PersonID: "a", Lines: 5}, {PersonID: "b", Lines: 7}}},
		{Date: "2020-01-02", Contributions: []ownership.SeriesContribution{{PersonID: "a", Lines: 6}}},
		{Date: "2020-01-03", Contributions: []ownership.SeriesContribution{}},
	}}
	body, err := encodeDataset(data, true)
	if err != nil {
		t.Fatal(err)
	}
	var wire ownership.Dataset
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SeriesEncoding != "changes-v1" || !reflect.DeepEqual(wire.Series, data.Series) {
		t.Fatal("sparse input was compacted a second time, dropping unchanged pairs")
	}
}
