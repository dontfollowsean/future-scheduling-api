package scheduling

import "errors"

// ErrSlotTaken means the trainer already has an appointment that overlaps
// the requested time. The store returns it; the API maps it to 409.
var ErrSlotTaken = errors.New("slot already taken")
