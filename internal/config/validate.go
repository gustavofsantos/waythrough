package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
		if err := entry.RootMarkers.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
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

func (markers RootMarkers) validate() error {
	const (
		markerCountMax  = 64
		markerLengthMax = 255
	)
	if len(markers) > markerCountMax {
		return fmt.Errorf("root_markers has %d groups; maximum is %d",
			len(markers), markerCountMax)
	}

	markerCount := 0
	for groupIndex, group := range markers {
		if len(group) == 0 {
			return fmt.Errorf("root_markers group %d is an empty group", groupIndex)
		}
		markerCount += len(group)
		if markerCount > markerCountMax {
			return fmt.Errorf("root_markers has %d markers; maximum is %d",
				markerCount, markerCountMax)
		}

		for markerIndex, marker := range group {
			id := fmt.Sprintf("root_markers group %d marker %d", groupIndex, markerIndex)
			if marker == "" {
				return fmt.Errorf("%s is an empty marker", id)
			}
			if len(marker) > markerLengthMax {
				return fmt.Errorf("%s length is %d; maximum length is %d",
					id, len(marker), markerLengthMax)
			}
			if filepath.IsAbs(marker) {
				return fmt.Errorf("%s is absolute", id)
			}
			for component := range strings.FieldsFuncSeq(marker, func(r rune) bool {
				return r == '/' || r == '\\'
			}) {
				if component == ".." {
					return fmt.Errorf("%s contains parent traversal", id)
				}
			}
		}
	}
	return nil
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
