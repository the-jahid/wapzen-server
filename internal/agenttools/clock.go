package agenttools

import (
	"fmt"
	"strings"
	"time"

	// The deployment image is not guaranteed to ship a zoneinfo database, and
	// without one every LoadLocation below fails and each agent silently runs
	// on UTC. Embedding it costs ~450KB and removes the dependency.
	_ "time/tzdata"
)

// countryZones maps a WhatsApp number's country calling code to the time zone
// most of that country keeps. It is only consulted when the agent has no time
// zone of its own, and only for codes where one zone is a fair answer: +1, +7
// and +61 span several and are left to fall back to UTC rather than guessed.
var countryZones = map[string]string{
	"20": "Africa/Cairo", "212": "Africa/Casablanca", "233": "Africa/Accra", "234": "Africa/Lagos",
	"254": "Africa/Nairobi", "27": "Africa/Johannesburg",
	"30": "Europe/Athens", "31": "Europe/Amsterdam", "32": "Europe/Brussels", "33": "Europe/Paris",
	"34": "Europe/Madrid", "351": "Europe/Lisbon", "353": "Europe/Dublin", "36": "Europe/Budapest",
	"39": "Europe/Rome", "40": "Europe/Bucharest", "41": "Europe/Zurich", "43": "Europe/Vienna",
	"44": "Europe/London", "45": "Europe/Copenhagen", "46": "Europe/Stockholm", "47": "Europe/Oslo",
	"48": "Europe/Warsaw", "49": "Europe/Berlin", "90": "Europe/Istanbul",
	"51": "America/Lima", "52": "America/Mexico_City", "54": "America/Argentina/Buenos_Aires",
	"55": "America/Sao_Paulo", "56": "America/Santiago", "57": "America/Bogota", "58": "America/Caracas",
	"60": "Asia/Kuala_Lumpur", "62": "Asia/Jakarta", "63": "Asia/Manila", "65": "Asia/Singapore",
	"66": "Asia/Bangkok", "81": "Asia/Tokyo", "82": "Asia/Seoul", "84": "Asia/Ho_Chi_Minh",
	"852": "Asia/Hong_Kong", "86": "Asia/Shanghai", "880": "Asia/Dhaka", "91": "Asia/Kolkata",
	"92": "Asia/Karachi", "94": "Asia/Colombo", "966": "Asia/Riyadh", "965": "Asia/Kuwait",
	"968": "Asia/Muscat", "971": "Asia/Dubai", "972": "Asia/Jerusalem", "973": "Asia/Bahrain",
	"974": "Asia/Qatar", "977": "Asia/Kathmandu",
}

// Location picks the time zone an agent's conversations are dated in: the
// agent's own setting when it names a real zone, otherwise the zone of the
// country its WhatsApp number belongs to, otherwise UTC. phone may carry a
// leading + or be bare digits, as a WhatsApp JID's user part is.
func Location(timezone, phone string) *time.Location {
	if name := strings.TrimSpace(timezone); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	// Calling codes are prefix-free, so the first match from the longest
	// candidate down is the only one.
	for size := 3; size >= 1; size-- {
		if len(digits) <= size {
			continue
		}
		if name, ok := countryZones[digits[:size]]; ok {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}

// DateContext is the line an agent's prompt carries so it knows what day it
// is. Without it a model has no idea, and the first thing a tool that takes a
// date gets is the model's guess at what "tonight" or "tomorrow" means.
func DateContext(now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	return fmt.Sprintf(
		"Current date and time: %s (%s, UTC%s). Work out relative dates such as today, tonight, tomorrow or this weekend from this date, "+
			"and give dates to tools in exactly the format their parameters ask for.",
		local.Format("Monday 2 January 2006, 15:04"), loc.String(), local.Format("-07:00"),
	)
}
