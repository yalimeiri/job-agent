// Package store defines the persistence interface used by the agent and its
// PostgreSQL implementation. All operations take a context as the first
// parameter so they participate in cancellation and timeouts.
package store

import (
	"context"

	"github.com/yalimeiri/job-agent/internal/models"
)

// Store handles all database operations.
type Store interface {
	// Jobs
	SaveJob(ctx context.Context, job *models.Job) (isNew bool, err error)
	UpdateJobStatus(ctx context.Context, id string, status models.JobStatus) error
	UpdateMatch(ctx context.Context, id string, score float64, reason, coverLetter string) error
	ListByStatus(ctx context.Context, status models.JobStatus) ([]*models.Job, error)
	HasSeen(ctx context.Context, externalID, source string) (bool, error)

	// Platform detection
	DetectPlatform(ctx context.Context, applyURL string) (*models.Platform, error)
	SetJobPlatform(ctx context.Context, jobID string, platformID int) error
	GetFieldMappings(ctx context.Context, platformID, stepNumber int) ([]models.FieldMapping, error)
	GetPlatformSteps(ctx context.Context, platformID int) ([]models.PlatformStep, error)
	GetCommonQuestions(ctx context.Context, platformID *int) ([]models.CommonQuestion, error)

	// Profile
	GetProfile(ctx context.Context) (map[string]string, error) // field_key -> field_value
	UpsertProfileField(ctx context.Context, key, value, fieldType string) error

	// Logging
	LogApplicationAction(ctx context.Context, entry models.ApplicationLogEntry) error
	UpdateSelectorHealth(ctx context.Context, mappingID int, success bool) error

	// Stats
	GetStats(ctx context.Context) (map[models.JobStatus]int, error)

	Close() error
}
