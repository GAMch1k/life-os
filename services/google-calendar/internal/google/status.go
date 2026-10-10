package google

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
)

// PushSummary is how writing the owner's edits to Google goes.
type PushSummary = store.PushSummaryRow

// PushSummary counts the edits waiting to be written to Google.
func (s *Service) PushSummary(ctx context.Context, connection store.GoogleConnection) (PushSummary, error) {
	return s.queries.PushSummary(ctx, connection.ID)
}

// Exports are the exports of the given Life OS events of the current
// connection.
func (s *Service) Exports(ctx context.Context, ids []pgtype.UUID) ([]store.GoogleExport, error) {
	return s.queries.ListGoogleExports(ctx, ids)
}
