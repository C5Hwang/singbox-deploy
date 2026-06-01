// Package ui implements the Bubble Tea terminal interface: an adaptive status
// panel plus grouped menu, and multi-step flows with progress bars.
package ui

import "fmt"

// Progress describes one multi-step flow's position.
type Progress struct {
	Current int
	Total   int
	Label   string
}

// Percent returns completion in the range [0,1]. A non-positive total is 0.
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return 0
	}
	return float64(p.Current) / float64(p.Total)
}

// Title renders the "N/M Label" header shown above the progress bar.
func (p Progress) Title() string {
	return fmt.Sprintf("%d/%d %s", p.Current, p.Total, p.Label)
}
