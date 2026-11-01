// Room-service client wrapper: the API uses it to end rooms and remove
// participants; the scheduler uses it for health checks. Errors are wrapped
// with context but never include credentials.
package livekit

import (
	"context"
	"fmt"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Client wraps the LiveKit roomservice client.
type Client struct {
	host     string
	apiKey   string
	apiSecret string
}

// NewClient builds a roomservice client wrapper.
func NewClient(host, apiKey, apiSecret string) *Client {
	return &Client{host: host, apiKey: apiKey, apiSecret: apiSecret}
}

// DeleteRoom removes the LiveKit room and disconnects every participant.
// Called when the owner ends a room (the rows keep the audit trail).
func (c *Client) DeleteRoom(ctx context.Context, roomName string) error {
	rs := lksdk.NewRoomServiceClient(c.host, c.apiKey, c.apiSecret)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := rs.DeleteRoom(cctx, &lksdk.DeleteRoomRequest{Room: roomName}); err != nil {
		return fmt.Errorf("livekit: delete room: %w", err)
	}
	return nil
}

// RemoveParticipant disconnects one participant (host moderation).
func (c *Client) RemoveParticipant(ctx context.Context, roomName, identity string) error {
	rs := lksdk.NewRoomServiceClient(c.host, c.apiKey, c.apiSecret)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := rs.RemoveParticipant(cctx, &lksdk.RoomParticipantIdentity{
		Room: roomName, Identity: identity,
	}); err != nil {
		return fmt.Errorf("livekit: remove participant: %w", err)
	}
	return nil
}

// ListRoomNames returns the names of currently active LiveKit rooms
// (scheduler/ops helper).
func (c *Client) ListRoomNames(ctx context.Context) ([]string, error) {
	rs := lksdk.NewRoomServiceClient(c.host, c.apiKey, c.apiSecret)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := rs.ListRooms(cctx, &lksdk.ListRoomsRequest{})
	if err != nil {
		return nil, fmt.Errorf("livekit: list rooms: %w", err)
	}
	names := make([]string, 0, len(resp.Rooms))
	for _, r := range resp.Rooms {
		names = append(names, r.Name)
	}
	return names, nil
}
