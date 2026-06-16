package applier

import (
	"context"

	"github.com/yalimeiri/job-agent/internal/models"
)

// Applier submits an application for a matched job.
type Applier interface {
	// Apply fills and submits the application form for job using profileData
	// (field_key -> value) and the generated cover letter.
	Apply(ctx context.Context, job *models.Job, profileData map[string]string, coverLetter string) (*models.ApplicationResult, error)
	// Close releases any held resources (browser instances).
	Close() error
}
