package ownership

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

const maxCalendarPoints = 242

func normalizeOptions(options Options) (Options, error) {
	if options.WindowDays == 0 {
		options.WindowDays = 90
	}
	if options.Workers == 0 {
		options.Workers = min(12, max(1, runtime.NumCPU()-2))
	}
	if options.Snapshots < 0 || options.Snapshots > 240 {
		return options, fmt.Errorf("explicit snapshots must be between 1 and 240, or zero for calendar sampling")
	}
	if options.WindowDays < 1 || options.WindowDays > 36500 {
		return options, fmt.Errorf("window days must be between 1 and 36500")
	}
	if options.Workers < 1 || options.Workers > 32 {
		return options, fmt.Errorf("workers must be between 1 and 32")
	}
	options.Interval = strings.ToLower(strings.TrimSpace(options.Interval))
	if options.Interval == "" {
		options.Interval = "auto"
	}
	switch options.Interval {
	case "auto", "day", "week", "month", "quarter", "year":
	default:
		return options, fmt.Errorf("interval must be auto, day, week, month, quarter or year")
	}
	if options.Since != "" {
		if _, err := parseDate(options.Since, false); err != nil {
			return options, err
		}
	}
	if options.Until != "" {
		if _, err := parseDate(options.Until, true); err != nil {
			return options, err
		}
	}
	return options, nil
}

func (e *engine) selectCutoffs(chain []string, options Options) ([]cutoff, Timeline, bool, error) {
	timeline := Timeline{Interval: options.Interval, Calendar: options.Snapshots == 0}
	if len(chain) == 0 {
		return nil, timeline, false, nil
	}
	start := e.commits[chain[len(chain)-1]].committed
	end := e.commits[chain[0]].committed
	skewed := false
	for i := 1; i < len(chain); i++ {
		date := e.commits[chain[i]].committed
		if date.After(e.commits[chain[i-1]].committed) {
			skewed = true
		}
		if date.Before(start) {
			start = date
		}
	}
	var err error
	if options.Since != "" {
		start, err = parseDate(options.Since, false)
		if err != nil {
			return nil, timeline, skewed, err
		}
	}
	if options.Until != "" {
		end, err = parseDate(options.Until, true)
		if err != nil {
			return nil, timeline, skewed, err
		}
	}
	if start.After(end) {
		return nil, timeline, skewed, fmt.Errorf("since must be at or before until")
	}
	timeline.Since, timeline.Until = start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)
	dates := []time.Time{}
	if options.Snapshots > 0 {
		timeline.Interval = "uniform"
		for i := 0; i < options.Snapshots; i++ {
			date := end
			if options.Snapshots > 1 && i < options.Snapshots-1 {
				date = start.Add(time.Duration(float64(end.Sub(start)) * float64(i) / float64(options.Snapshots-1)))
			}
			if len(dates) == 0 || !dates[len(dates)-1].Equal(date) {
				dates = append(dates, date)
			}
		}
	} else {
		interval := options.Interval
		if interval == "auto" {
			switch {
			case !end.After(start.Add(90 * 24 * time.Hour)):
				interval = "day"
			case !end.After(start.AddDate(2, 0, 0)):
				interval = "week"
			default:
				interval = "month"
			}
		}
		for {
			dates, err = calendarDates(start, end, interval)
			if err == nil || options.Interval != "auto" {
				break
			}
			switch interval {
			case "month":
				interval = "quarter"
			case "quarter":
				interval = "year"
			default:
				return nil, timeline, skewed, err
			}
		}
		if err != nil {
			return nil, timeline, skewed, err
		}
		timeline.Interval = interval
	}
	result := []cutoff{}
	for _, date := range dates {
		var selected string
		for _, sha := range chain {
			if !e.commits[sha].committed.After(date) {
				selected = sha
				break
			}
		}
		// Preserve requested bounds even before history: an empty revision yields
		// an explicit empty snapshot, never the first future commit.
		result = append(result, cutoff{date: date, sha: selected})
	}
	timeline.Points = len(result)
	return result, timeline, skewed, nil
}

func calendarDates(start, end time.Time, interval string) ([]time.Time, error) {
	start, end = start.UTC(), end.UTC()
	if start.Equal(end) {
		return []time.Time{start}, nil
	}
	boundary := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	advance := func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	switch interval {
	case "day":
	case "week":
		boundary = boundary.AddDate(0, 0, -(int(boundary.Weekday())+6)%7)
		advance = func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	case "month":
		boundary = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
		advance = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
	case "quarter":
		month := time.Month((int(start.Month())-1)/3*3 + 1)
		boundary = time.Date(start.Year(), month, 1, 0, 0, 0, 0, time.UTC)
		advance = func(t time.Time) time.Time { return t.AddDate(0, 3, 0) }
	case "year":
		boundary = time.Date(start.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		advance = func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }
	default:
		return nil, fmt.Errorf("unsupported calendar interval %q", interval)
	}
	for !boundary.After(start) {
		boundary = advance(boundary)
	}
	dates := []time.Time{start}
	for boundary.Before(end) {
		if len(dates) >= maxCalendarPoints-1 {
			return nil, fmt.Errorf("%s interval produces more than %d calendar points; choose a coarser interval or a narrower time range", interval, maxCalendarPoints)
		}
		dates = append(dates, boundary)
		boundary = advance(boundary)
	}
	return append(dates, end), nil
}
