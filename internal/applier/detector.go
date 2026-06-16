// Package applier submits job applications. The engine is data-driven: it reads
// platform definitions, field mappings and common questions from the store and
// drives a headless browser, so supporting a new ATS is an INSERT, not code.
package applier

import (
	"fmt"
	"regexp"

	"github.com/yalimeiri/job-agent/internal/models"
)

// PlatformClass categorises how a detected platform should be handled.
type PlatformClass int

const (
	// ClassUnknown means no platform matched the apply URL.
	ClassUnknown PlatformClass = iota
	// ClassNotifyOnly means a platform matched but must not be automated
	// (LinkedIn, or any platform with enabled=false).
	ClassNotifyOnly
	// ClassAutomate means the platform can be auto-applied.
	ClassAutomate
)

func (c PlatformClass) String() string {
	switch c {
	case ClassNotifyOnly:
		return "notify_only"
	case ClassAutomate:
		return "automate"
	default:
		return "unknown"
	}
}

// Classify decides how to handle a detected platform. A nil platform is
// unknown; LinkedIn and disabled platforms are notify-only; everything else is
// automatable.
func Classify(p *models.Platform) PlatformClass {
	switch {
	case p == nil:
		return ClassUnknown
	case p.Name == "linkedin" || !p.Enabled:
		return ClassNotifyOnly
	default:
		return ClassAutomate
	}
}

// MatchPlatform returns the first platform whose url_pattern regex matches the
// apply URL, or nil if none match. It is a pure helper (no DB), used for
// testing and when platforms are already loaded.
func MatchPlatform(platforms []models.Platform, applyURL string) (*models.Platform, error) {
	for i := range platforms {
		re, err := regexp.Compile(platforms[i].URLPattern)
		if err != nil {
			return nil, fmt.Errorf("bad url_pattern for %s: %w", platforms[i].Name, err)
		}
		if re.MatchString(applyURL) {
			return &platforms[i], nil
		}
	}
	return nil, nil
}
