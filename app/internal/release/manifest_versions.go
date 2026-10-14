package release

import (
	"fmt"

	"github.com/Masterminds/semver/v3"
)

func validateUpdateVersions(m Manifest) error {
	for name, value := range map[string]string{
		"desktop": m.Desktop.Version, "engine": m.Engine.Version, "CARE": m.Care.Version,
	} {
		v, err := semver.StrictNewVersion(value)
		if err != nil {
			return fmt.Errorf("%s version: %w", name, err)
		}
		if name == "CARE" && (v.Major() != 3 || v.Minor() != 1) {
			return fmt.Errorf("only CARE 3.1 ships")
		}
	}
	for _, value := range []string{m.Desktop.RequiresEngine, m.Engine.RequiresDesktop, m.Care.RequiresDesktop, m.Care.RequiresEngine} {
		if value == "" {
			return fmt.Errorf("missing version requirement")
		}
		if _, err := semver.NewConstraint(value); err != nil {
			return fmt.Errorf("invalid requirement: %w", err)
		}
	}
	return nil
}
