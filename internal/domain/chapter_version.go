package domain

import "time"

// ChapterVersion is an immutable chapter manuscript. Only explicit application
// changes the project's accepted chapter; creating a candidate is read-only to it.
type ChapterVersion struct {
	ID          string    `json:"id"`
	Number      int       `json:"number"`
	Chapter     int       `json:"chapter"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	ContentHash string    `json:"contentHash"`
	CreatedAt   time.Time `json:"createdAt"`
	Instruction string    `json:"instruction,omitempty"`
	Kind        string    `json:"kind"`
	Active      bool      `json:"active"`
}
