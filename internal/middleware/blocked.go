package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// writeBlocked refuses a request from a blocked account. X-Account-Blocked is
// what the dashboard keys its "account blocked" screen on (a plain 403 can come
// from any handler), and X-Account-Blocked-Lang says which language to show it
// in when the account's number tells us (see languageForPhone).
func (a *Auth) writeBlocked(w http.ResponseWriter, r *http.Request, user models.User) {
	w.Header().Set("X-Account-Blocked", "true")
	if lang := a.blockedLanguage(r.Context(), user.ID); lang != "" {
		w.Header().Set("X-Account-Blocked-Lang", lang)
	}
	writeAuthJSON(w, http.StatusForbidden, "This account has been blocked")
}

func (a *Auth) blockedLanguage(ctx context.Context, userID string) string {
	if a.users == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	phone, err := a.users.FirstPhoneNumber(ctx, userID)
	if err != nil {
		log.Printf("auth: phone lookup for blocked user %s failed: %v", userID, err)
		return ""
	}
	return languageForPhone(phone)
}

// languageByCallingCode maps a country calling code to the language the
// dashboard's blocked screen has a translation for. A number matches its
// longest listed prefix; one that matches none gets no hint, and the dashboard
// falls back to the browser's language.
var languageByCallingCode = map[string]string{
	// Arabic
	"20": "ar", "212": "ar", "213": "ar", "216": "ar", "218": "ar", "249": "ar",
	"961": "ar", "962": "ar", "963": "ar", "964": "ar", "965": "ar", "966": "ar",
	"967": "ar", "968": "ar", "970": "ar", "971": "ar", "973": "ar", "974": "ar",
	// French
	"33": "fr", "221": "fr", "223": "fr", "225": "fr", "226": "fr", "227": "fr",
	"228": "fr", "229": "fr", "237": "fr", "241": "fr", "242": "fr", "243": "fr",
	// Spanish
	"34": "es", "51": "es", "52": "es", "53": "es", "54": "es", "56": "es",
	"57": "es", "58": "es", "502": "es", "503": "es", "504": "es", "505": "es",
	"506": "es", "507": "es", "591": "es", "593": "es", "595": "es", "598": "es",
	// Portuguese
	"55": "pt", "351": "pt", "244": "pt", "258": "pt",
	// Others
	"39": "it", "62": "id", "91": "hi", "880": "bn",
}

func languageForPhone(phone string) string {
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	for n := 3; n >= 1; n-- {
		if len(digits) > n {
			if lang, ok := languageByCallingCode[digits[:n]]; ok {
				return lang
			}
		}
	}
	return ""
}
