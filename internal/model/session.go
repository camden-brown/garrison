package model

import "time"

// Session is one player's stay on one server.
//
// Left is zero while they are still connected, which is the difference between
// "played for two hours" and "has been on for two hours". A view that treats a
// zero Left as a zero-length session reports an empty server as busy.
type Session struct {
	Server  string
	Player  string
	SteamID string
	Joined  time.Time
	Left    time.Time
}

// Open reports whether the player is still connected.
func (s Session) Open() bool { return s.Left.IsZero() }

// Duration is how long the session lasted, or has lasted so far. now is passed
// rather than read so a render is a pure function of its inputs.
func (s Session) Duration(now time.Time) time.Duration {
	end := s.Left
	if s.Open() {
		end = now
	}
	if end.Before(s.Joined) {
		return 0
	}
	return end.Sub(s.Joined)
}
