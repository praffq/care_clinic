package protocol

import (
	"maps"
	"regexp"
	"slices"
)

// Frontend build variables. The frontend-rebuild kind accepts only these
// REACT_* keys, each with a value pattern; values reach Vite through the
// build's environment and %REACT_APP_TITLE% is substituted into index.html,
// so text values exclude markup and quote characters.
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
	feUUIDRE      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

var frontendBuildEnv = map[string]*regexp.Regexp{
	"REACT_APP_TITLE":                               feTitleRE,
	"REACT_ALLOWED_LOCALES":                         feLocalesRE,
	"REACT_DEFAULT_ENCOUNTER_TYPE":                  feEncounterRE,
	"REACT_ALLOWED_ENCOUNTER_CLASSES":               feClassesRE,
	"REACT_ENABLE_MINIMAL_PATIENT_REGISTRATION":     feBoolRE,
	"REACT_PATIENT_REG_MIN_GEO_ORG_LEVELS_REQUIRED": feDigitRE,
	"REACT_DEFAULT_PAYMENT_METHOD":                  fePaymentRE,
	"REACT_DEFAULT_PAYMENT_TERMS":                   feTextRE,
	"REACT_INVENTORY_DEFAULT_TAX_INCLUSIVE":         feBoolRE,
	"REACT_ENABLE_AUTO_INVOICE_AFTER_DISPENSE":      feBoolRE,
	"REACT_CARE_API_URL":                            feURLRE,
	"REACT_DISABLE_PATIENT_LOGIN":                   feBoolRE,
	"REACT_RECAPTCHA_SITE_KEY":                      feSiteKeyRE,
	"REACT_MFE_REGISTERED_COMPONENTS":               feCompsRE,
	"REACT_ENABLED_APPS":                            feAppsRE,
	"REACT_PATIENT_REGISTRATION_DEFAULT_GEO_ORG":    feUUIDRE,
}

// FrontendBuildKeys returns the REACT_* keys a frontend rebuild accepts.
func FrontendBuildKeys() []string { return slices.Sorted(maps.Keys(frontendBuildEnv)) }

// ValidFrontendBuildValue reports whether key is an allowed frontend build
// variable and value matches its pattern.
func ValidFrontendBuildValue(key, value string) bool {
	re, ok := frontendBuildEnv[key]
	return ok && len(value) <= MaxEnvValue && re.MatchString(value)
}
