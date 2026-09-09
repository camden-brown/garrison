package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/host"
)

// Default stop behaviour, used until a game's plan supplies its own.
//
// From M1 the signal and grace come from model.Plan, because they are game
// facts: Valheim's image traps SIGINT to save the world, and a Zomboid server
// needs two minutes to finish writing chunk files. Until a plugin says
// otherwise, this errs long — killing a game server early costs a save, and
// waiting costs a minute.
const (
	DefaultStopSignal = "SIGTERM"
	DefaultStopGrace  = 60 * time.Second
)

// Controller performs operations on containers. It satisfies core.Control.
type Controller struct {
	Driver     host.Driver
	StopSignal string
	StopGrace  time.Duration
}

// Start brings a container up.
func (c *Controller) Start(ctx context.Context, instance, id string) error {
	if err := c.Driver.Start(ctx, id); err != nil {
		return fmt.Errorf("%s: start: %w", instance, err)
	}
	return nil
}

// Stop takes a container down with the configured signal and grace period.
func (c *Controller) Stop(ctx context.Context, instance, id string) error {
	signal := c.StopSignal
	if signal == "" {
		signal = DefaultStopSignal
	}
	grace := c.StopGrace
	if grace <= 0 {
		grace = DefaultStopGrace
	}

	if err := c.Driver.Stop(ctx, id, signal, grace); err != nil {
		return fmt.Errorf("%s: stop: %w", instance, err)
	}
	return nil
}
