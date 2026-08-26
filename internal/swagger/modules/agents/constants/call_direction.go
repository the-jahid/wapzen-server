package constants

// CallDirectionOptions is the set of allowed call_direction values.
var CallDirectionOptions = []string{"inbound", "outbound", "both"}

// CallDirection is the union of CallDirectionOptions.
type CallDirection = string

// CallDirectionDefault is the default call_direction.
const CallDirectionDefault CallDirection = "outbound"
