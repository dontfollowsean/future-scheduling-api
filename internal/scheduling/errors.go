package scheduling

import "errors"

// ErrSlotTaken means the trainer already has an appointment that overlaps
// the requested time. The store returns it; the API maps it to 409.
var ErrSlotTaken = errors.New("slot already taken")

// Codes for well-formed input that breaks a scheduling rule. They are part
// of the API contract and must not change; messages may.
const (
	CodeInvalidDuration      = "invalid_duration"
	CodeOffGrid              = "off_grid"
	CodeNotBusinessDay       = "not_business_day"
	CodeOutsideBusinessHours = "outside_business_hours"
	CodeInPast               = "in_past"
	CodeInvalidRange         = "invalid_range"
	CodeRangeTooLarge        = "range_too_large"
)

// ValidationError is one broken scheduling rule. Field names the input at
// fault, such as "starts_at", and is empty when no single field is.
type ValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}
