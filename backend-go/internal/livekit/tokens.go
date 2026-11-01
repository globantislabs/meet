// Package livekit wraps the LiveKit server SDK for the control plane:
// short-lived participant access tokens and the room-service client used by
// the API (end room, remove participant) and the scheduler.
//
// SECURITY: the API key/secret are server-side only. Browsers receive
// short-lived JOIN tokens with minimal grants (blueprint §12).
package livekit

import (
        "fmt"
        "time"

        "github.com/livekit/protocol/auth"
)

// TokenGrants is the minimal permission set for one participant.
type TokenGrants struct {
        RoomName     string
        Identity     string // server-issued participant ID (stable, unique)
        DisplayName  string
        CanPublish   bool
        CanSubscribe bool
        TTL          time.Duration
}

// IssueJoinToken mints a short-lived LiveKit join token for one participant.
// Grant discipline: publish one microphone track, subscribe allowed tracks,
// no data publishing (the app has no data channel features today).
func IssueJoinToken(apiKey, apiSecret string, g TokenGrants) (string, error) {
        at := auth.NewAccessToken(apiKey, apiSecret)
        at.SetIdentity(g.Identity)
        if g.DisplayName != "" {
                at.SetName(g.DisplayName)
        }
        at.SetValidFor(g.TTL)
        at.SetVideoGrant(&auth.VideoGrant{
                RoomJoin:     true,
                Room:         g.RoomName,
                CanPublish:   boolPtr(g.CanPublish),
                CanSubscribe: boolPtr(g.CanSubscribe),
                CanPublishData: boolPtr(false),
        })
        token, err := at.ToJWT()
        if err != nil {
                return "", fmt.Errorf("livekit: issue join token: %w", err)
        }
        return token, nil
}

// IssueWorkerToken mints the server-side media worker's join token. Workers
// publish translated tracks and subscribe source tracks; identity must be
// unique per room per worker.
func IssueWorkerToken(apiKey, apiSecret, roomName, identity string, ttl time.Duration) (string, error) {
        return IssueJoinToken(apiKey, apiSecret, TokenGrants{
                RoomName:     roomName,
                Identity:     identity,
                DisplayName:  "Media Worker",
                CanPublish:   true,
                CanSubscribe: true,
                TTL:          ttl,
        })
}

func boolPtr(b bool) *bool { return &b }
