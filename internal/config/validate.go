package config

import (
	"errors"
	"fmt"
)

// Validate checks that every language-server entry in cfg has what
// Waythrough needs to start it and route files to it.
func Validate(cfg Config) error {
	var errs []error
	if len(cfg.LanguageServers) == 0 {
		errs = append(errs, errors.New("no language servers configured"))
	}
	seenNames := make(map[string]bool, len(cfg.LanguageServers))
	owner := make(map[string]string) // filetype extension -> owning entry id

	for i, entry := range cfg.LanguageServers {
		id := entryID(entry, i)

		if entry.Name == "" {
			errs = append(errs, fmt.Errorf("%s: missing name", id))
		} else if seenNames[entry.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name %q", id, entry.Name))
		}
		seenNames[entry.Name] = true
		if entry.Command == "" {
			errs = append(errs, fmt.Errorf("%s: missing command", id))
		}
		if len(entry.Filetypes) == 0 {
			errs = append(errs, fmt.Errorf("%s: missing filetypes", id))
		}
		if !validReadiness(entry.Readiness) {
			errs = append(errs, fmt.Errorf("%s: invalid readiness %q (want %q or %q)",
				id, entry.Readiness, ReadinessProgress, ReadinessHandshake))
		}

		for ext := range entry.Filetypes {
			if other, claimed := owner[ext]; claimed {
				errs = append(errs, fmt.Errorf(
					"%s: filetype %q is already claimed by %s", id, ext, other))
				continue
			}
			owner[ext] = id
		}
	}

	return errors.Join(errs...)
}

func entryID(entry LanguageServer, index int) string {
	if entry.Name != "" {
		return fmt.Sprintf("entry %q", entry.Name)
	}
	return fmt.Sprintf("entry #%d", index)
}

func validReadiness(r Readiness) bool {
	return r == "" || r == ReadinessProgress || r == ReadinessHandshake
}
