package manifest

import "testing"

func TestSettingValidate(t *testing.T) {
	cases := []struct {
		s    Setting
		v    string
		okay bool
	}{
		{Setting{Key: "A", Type: SettingInt}, "25", true},
		{Setting{Key: "A", Type: SettingInt}, "lots", false},
		{Setting{Key: "A", Type: SettingBool}, "true", true},
		{Setting{Key: "A", Type: SettingBool}, "yes", false},
		{Setting{Key: "A", Type: SettingDuration}, "90m", true},
		{Setting{Key: "A", Type: SettingDuration}, "-1m", false},
		{Setting{Key: "A", Type: SettingTime}, "02:30", true},
		{Setting{Key: "A", Type: SettingTime}, "2:30am", false},
		{Setting{Key: "A", Type: SettingURL}, "https://pay.example.com/ipn", true},
		{Setting{Key: "A", Type: SettingURL}, "pay.example.com", false},
		{Setting{Key: "A", Type: SettingEnum, Enum: []string{"a", "b"}}, "b", true},
		{Setting{Key: "A", Type: SettingEnum, Enum: []string{"a", "b"}}, "c", false},
		{Setting{Key: "A", Type: "colour"}, "red", false},
		// Empty is "unset" whatever the type: the plugin falls to its default.
		{Setting{Key: "A", Type: SettingInt}, "", true},
	}
	for _, c := range cases {
		if err := c.s.Validate(c.v); (err == nil) != c.okay {
			t.Errorf("%s %q: err %v, want ok=%v", c.s.Type, c.v, err, c.okay)
		}
	}
}

func TestValidSettingKey(t *testing.T) {
	for k, want := range map[string]bool{"PDF_MAX_CONCURRENT": true, "pdf": false, "1X": false, "A-B": false} {
		if ValidSettingKey(k) != want {
			t.Errorf("%q: want %v", k, want)
		}
	}
}
