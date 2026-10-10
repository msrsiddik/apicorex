package manifest

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Setting is one value a plugin lets an operator set from the dashboard.
//
// Key is the environment variable the plugin already reads, so moving a
// setting onto the dashboard changes nothing about its name, and the variable
// still wins when it is set: the environment stays the way back.
type Setting struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	// Enum is the allowed values when Type is "enum".
	Enum []string `json:"enum,omitempty"`
	// Secret settings are sealed in Core's store, never shown again once
	// saved, and handed only to the plugin itself when it presents its own
	// key.
	Secret bool `json:"secret,omitempty"`
	// SetOnce marks a value that data depends on — an encryption key, a
	// signing key. Replacing or clearing one once it is set needs an explicit
	// confirmation, since what was encrypted or signed with the old value
	// stops working.
	SetOnce bool `json:"set_once,omitempty"`
}

// Setting types.
const (
	SettingString   = "string"
	SettingInt      = "int"
	SettingBool     = "bool"
	SettingDuration = "duration"
	SettingTime     = "time"
	SettingURL      = "url"
	SettingEnum     = "enum"
)

var settingKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// maxSettingLen bounds a value. Settings are configuration, not documents;
// anything longer is a mistake or something that belongs elsewhere.
const (
	maxSettingLen = 4096
	maxSecretLen  = 16384
)

// ValidSettingKey reports whether k is a key a plugin may declare: an
// environment-variable name.
func ValidSettingKey(k string) bool { return settingKeyRE.MatchString(k) }

// Validate checks a value against the declared type. The empty string is
// always allowed: it means "unset", which a plugin reads as its default.
func (s Setting) Validate(v string) error {
	if v == "" {
		return nil
	}
	limit := maxSettingLen
	if s.Secret {
		// A secret can be a whole config file, base64 — rclone's, with its
		// Drive token and crypt password, runs to a few kilobytes.
		limit = maxSecretLen
	}
	if len(v) > limit {
		return fmt.Errorf("%s: longer than %d characters", s.Key, limit)
	}
	switch s.Type {
	case SettingString, "":
		return nil
	case SettingInt:
		if _, err := strconv.Atoi(v); err != nil {
			return fmt.Errorf("%s: not a whole number", s.Key)
		}
	case SettingBool:
		if v != "true" && v != "false" {
			return fmt.Errorf("%s: must be true or false", s.Key)
		}
	case SettingDuration:
		if d, err := time.ParseDuration(v); err != nil || d < 0 {
			return fmt.Errorf("%s: not a duration such as 30s, 5m or 1h", s.Key)
		}
	case SettingTime:
		if _, err := time.Parse("15:04", v); err != nil {
			return fmt.Errorf("%s: not a time of day as HH:MM", s.Key)
		}
	case SettingURL:
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s: not an absolute URL", s.Key)
		}
	case SettingEnum:
		for _, e := range s.Enum {
			if v == e {
				return nil
			}
		}
		return fmt.Errorf("%s: must be one of %s", s.Key, strings.Join(s.Enum, ", "))
	default:
		return fmt.Errorf("%s: the plugin declared an unknown type %q", s.Key, s.Type)
	}
	return nil
}

// SettingByKey finds a declared setting.
func (m Manifest) SettingByKey(key string) (Setting, bool) {
	for _, s := range m.Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}
