package protocol

import (
	"maps"
	"regexp"
	"slices"
)

// Frontend build variables. The frontend-rebuild kind accepts the known
// REACT_* keys below, each with a strict value pattern, and any other
// REACT_* key with a conservative generic value. Values reach Vite through
// the build's environment and %REACT_APP_TITLE% is substituted into
// index.html, so values exclude markup, quote and shell characters.
var (
	feBoolRE      = regexp.MustCompile(`^(?:true|false)?$`)
	feTitleRE     = regexp.MustCompile(`^[\p{L}\p{M}\p{N} .,'()_:/!?#+@-]{0,80}$`)
	feTextRE      = regexp.MustCompile(`^[\p{L}\p{M}\p{N} .,'()_:/!?#+@%-]{0,200}$`)
	feEncounterRE = regexp.MustCompile(`^(?:amb|imp|emer|obsenc|hh|vr)?$`)
	feClassesRE   = regexp.MustCompile(`^(?:(?:amb|imp|emer|obsenc|hh|vr)(?:,(?:amb|imp|emer|obsenc|hh|vr))*)?$`)
	fePaymentRE   = regexp.MustCompile(`^(?:cash|ccca|debc|chck|ddpo|cdac|cchk)?$`)
	feLocalesRE   = regexp.MustCompile(`^(?:[a-z]{2}(?:,[a-z]{2})*)?$`)
	feDigitRE     = regexp.MustCompile(`^[0-9]?$`)
	feURLRE       = regexp.MustCompile(`^(?:https?://[A-Za-z0-9.-]+(?::[0-9]{1,5})?)?(?:/[A-Za-z0-9._~/-]*)?$`)
	feCompsRE     = regexp.MustCompile(`^(?:\*|[A-Za-z0-9_]+(?:,[A-Za-z0-9_]+)*)?$`)
	feAppsRE      = regexp.MustCompile(`^[A-Za-z0-9_./@:,-]{0,1000}$`)
	feSiteKeyRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{0,100}$`)
	feUUIDRE      = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	feNumberRE    = regexp.MustCompile(`^[0-9]{0,6}$`)
	feCountryRE   = regexp.MustCompile(`^(?:[A-Z]{2})?$`)
	feCodeRE      = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
	feToastRE     = regexp.MustCompile(`^(?:(?:top|bottom)-(?:left|right|center))?$`)

	feKeyRE     = regexp.MustCompile(`^REACT_[A-Z0-9_]{1,60}$`)
	feGenericRE = regexp.MustCompile("^[^\"'<>\\\\$`\\x00-\\x1f\\x7f]{0,200}$")
)

var frontendBuildEnv = map[string]*regexp.Regexp{
	"REACT_APP_TITLE":                                feTitleRE,
	"REACT_ALLOWED_LOCALES":                          feLocalesRE,
	"REACT_DEFAULT_ENCOUNTER_TYPE":                   feEncounterRE,
	"REACT_ALLOWED_ENCOUNTER_CLASSES":                feClassesRE,
	"REACT_ENABLE_MINIMAL_PATIENT_REGISTRATION":      feBoolRE,
	"REACT_PATIENT_REG_MIN_GEO_ORG_LEVELS_REQUIRED":  feDigitRE,
	"REACT_DEFAULT_PAYMENT_METHOD":                   fePaymentRE,
	"REACT_DEFAULT_PAYMENT_TERMS":                    feTextRE,
	"REACT_INVENTORY_DEFAULT_TAX_INCLUSIVE":          feBoolRE,
	"REACT_ENABLE_AUTO_INVOICE_AFTER_DISPENSE":       feBoolRE,
	"REACT_CARE_API_URL":                             feURLRE,
	"REACT_DISABLE_PATIENT_LOGIN":                    feBoolRE,
	"REACT_RECAPTCHA_SITE_KEY":                       feSiteKeyRE,
	"REACT_MFE_REGISTERED_COMPONENTS":                feCompsRE,
	"REACT_ENABLED_APPS":                             feAppsRE,
	"REACT_PATIENT_REGISTRATION_DEFAULT_GEO_ORG":     feUUIDRE,
	"REACT_DEFAULT_COUNTRY":                          feCountryRE,
	"REACT_DEFAULT_COUNTRY_NAME":                     feTextRE,
	"REACT_CUSTOM_DESCRIPTION":                       feTextRE,
	"REACT_DEFAULT_DISCHARGE_DISPOSITION":            feCodeRE,
	"REACT_TOAST_POSITION":                           feToastRE,
	"REACT_AUTO_REFRESH_BY_DEFAULT":                  feBoolRE,
	"REACT_AUTO_REFRESH_INTERVAL":                    feNumberRE,
	"REACT_APP_MAX_IMAGE_UPLOAD_SIZE_MB":             feNumberRE,
	"REACT_INVENTORY_EXPIRY_MONTH_OFFSET":            feNumberRE,
	"REACT_MAX_FORM_DIALOG_FAVORITES":                feNumberRE,
	"REACT_ENCOUNTER_DEFAULT_DATE_FILTER":            feNumberRE,
	"REACT_APPOINTMENTS_DEFAULT_DATE_FILTER":         feNumberRE,
	"REACT_ENABLE_QUESTIONNAIRE_DRAFT":               feBoolRE,
	"REACT_ENABLE_TOKEN_GENERATION_IN_PATIENT_HOME":  feBoolRE,
	"REACT_OPEN_SCHEDULE_AFTER_PATIENT_REGISTRATION": feBoolRE,
	"REACT_PATIENT_GLOBAL_EDIT_ACCESS_ENABLED":       feBoolRE,
	"REACT_PAYMENT_LOCATION_REQUIRED":                feBoolRE,
	"REACT_APPOINTMENTS_USE_AVAILABILITY_STATS_API":  feBoolRE,
}

// FrontendBuildKeys returns the known REACT_* keys (with strict patterns).
func FrontendBuildKeys() []string { return slices.Sorted(maps.Keys(frontendBuildEnv)) }

// ValidFrontendBuildKey reports whether key may be a frontend build variable:
// ^REACT_[A-Z0-9_]{1,60}$.
func ValidFrontendBuildKey(key string) bool { return feKeyRE.MatchString(key) }

// ValidFrontendBuildValue reports whether key is an allowed frontend build
// variable and value matches its pattern: a known key's own, otherwise at
// most 200 characters without quotes, <, >, backslash, $, backticks or
// control characters.
func ValidFrontendBuildValue(key, value string) bool {
	if len(value) > MaxEnvValue {
		return false
	}
	if re, ok := frontendBuildEnv[key]; ok {
		return re.MatchString(value)
	}
	return feKeyRE.MatchString(key) && feGenericRE.MatchString(value)
}
