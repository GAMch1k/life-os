package api

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

// recentFailureWindow is how far back recentFailures counts.
const recentFailureWindow = 24 * time.Hour

// ListChannels reports each configured channel, in the configured order,
// with the health its deliveries show. Nothing is probed: a channel that has
// not been used yet is idle. Status's details, so the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; the Dashboard reads it without one.
func (h *Handler) ListChannels(c fiber.Ctx) error {
	if err := identity.RequireAdmin(c, identity.AdminPolicy{}); err != nil {
		return err
	}
	result := generated.ChannelList{Items: make([]generated.Channel, 0, len(h.channels))}
	if len(h.channels) == 0 {
		return c.JSON(result)
	}
	rows, err := store.New(h.db).ChannelHealth(c.Context(), store.ChannelHealthParams{
		Since:    pgtype.Timestamptz{Time: h.now().Add(-recentFailureWindow).UTC(), Valid: true},
		Channels: h.channels,
	})
	if err != nil {
		return internal(err)
	}
	byName := make(map[string]store.ChannelHealthRow, len(rows))
	for _, row := range rows {
		byName[row.Channel] = row
	}
	for _, name := range h.channels {
		result.Items = append(result.Items, channelOutput(name, byName[name]))
	}
	return c.JSON(result)
}

func channelOutput(name string, row store.ChannelHealthRow) generated.Channel {
	channel := generated.Channel{
		Name:           name,
		LastSuccessAt:  timestamp(row.LastSuccessAt),
		LastFailureAt:  timestamp(row.LastFailureAt),
		RecentFailures: int(row.RecentFailures),
		Retrying:       int(row.Retrying),
		Pending:        int(row.Pending),
	}
	if row.LastError != "" {
		lastError := row.LastError
		channel.LastError = &lastError
	}
	channel.State = channelState(channel.LastSuccessAt, channel.LastFailureAt)
	return channel
}

// channelState is failing when the latest outcome is a failure, ok when it
// is a success, and idle before the first outcome.
func channelState(lastSuccess, lastFailure *time.Time) generated.ChannelState {
	switch {
	case lastFailure != nil && (lastSuccess == nil || lastFailure.After(*lastSuccess)):
		return generated.Failing
	case lastSuccess != nil:
		return generated.Ok
	default:
		return generated.Idle
	}
}
