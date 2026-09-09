// Package logs turns container output into typed events.
//
// One follow per running container, each line through the game's Parse, and
// the results batched to the store. The batching is the part that matters: a
// crash-looping mod produces thousands of lines a second, and a mutation per
// line would put the store — and therefore the renderer — under exactly the
// load the log flood is warning about.
package logs

import (
	"bufio"
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/streams"
)

// Parsers resolves a game id to its line parser.
//
// A function rather than an import of internal/games, so this package can be
// tested with a parser that returns whatever a test needs, and so the log
// pipeline does not depend on the plugin registry.
type Parsers func(game string) (parse func(line string) model.Event, ok bool)

// Observer receives batches of parsed events. The store implements it.
type Observer interface {
	LogEventsRead(ctx context.Context, instance string, events []model.Event)
}

// Defaults for the batching window.
//
// A tenth of a second is below the threshold where a person perceives the
// console as lagging, and it caps the store at ten mutations per second per
// server no matter how hard the container is shouting.
const (
	DefaultFlushEvery = 100 * time.Millisecond
	DefaultBatchLimit = 256
	// DefaultTail is how much history to ask for when a stream opens, so a
	// server that has been up for hours does not present an empty console.
	DefaultTail = 200
)

// Streamer follows every running container's output.
type Streamer struct {
	driver   host.Driver
	parsers  Parsers
	observer Observer
	sup      *streams.Supervisor

	FlushEvery time.Duration
	BatchLimit int
	Tail       int
}

// NewStreamer returns a streamer that is not yet running.
func NewStreamer(driver host.Driver, parsers Parsers, obs Observer) *Streamer {
	s := &Streamer{
		driver:     driver,
		parsers:    parsers,
		observer:   obs,
		FlushEvery: DefaultFlushEvery,
		BatchLimit: DefaultBatchLimit,
		Tail:       DefaultTail,
	}
	s.sup = streams.New(s.stream)
	return s
}

// Reconcile tells the streamer which containers exist now.
func (s *Streamer) Reconcile(containers []host.Container) { s.sup.Reconcile(containers) }

// Run owns every stream until ctx is cancelled. It blocks.
func (s *Streamer) Run(ctx context.Context) { s.sup.Run(ctx) }

func (s *Streamer) stream(ctx context.Context, c host.Container) {
	parse, ok := s.parsers(c.Game)
	if !ok {
		// No plugin for this game. Its output is still worth showing, so
		// every line becomes an unclassified event rather than nothing.
		parse = func(line string) model.Event {
			return model.Event{Kind: model.KindInfo, Raw: line, Text: line}
		}
	}

	rc, err := s.driver.Logs(ctx, c.ID, s.Tail)
	if err != nil {
		return
	}
	defer rc.Close()

	// Reading blocks, so it happens on its own goroutine and the batching
	// loop below selects on the channel. Closing rc when this function
	// returns is what unblocks the reader.
	lines := make(chan string, 1024)
	go readLines(rc, lines)

	s.batch(ctx, c.Instance, parse, lines)
}

// batch accumulates events and hands them over on a timer or when the batch is
// full, whichever comes first.
func (s *Streamer) batch(ctx context.Context, instance string, parse func(string) model.Event, lines <-chan string) {
	every := s.FlushEvery
	if every <= 0 {
		every = DefaultFlushEvery
	}
	limit := s.BatchLimit
	if limit <= 0 {
		limit = DefaultBatchLimit
	}

	// One ticker for the life of the stream. time.After inside the loop
	// allocates a timer per iteration that stays live until it fires, which
	// over days of log traffic is a lot of garbage for no reason.
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	pending := make([]model.Event, 0, limit)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		// Hand over ownership: the observer keeps the slice, so the next
		// batch starts a new one rather than writing through it.
		s.observer.LogEventsRead(ctx, instance, pending)
		pending = make([]model.Event, 0, limit)
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			flush()

		case line, ok := <-lines:
			if !ok {
				flush()
				return
			}
			ev := parse(line)
			if ev.Drop() {
				continue
			}
			pending = append(pending, ev)
			if len(pending) >= limit {
				flush()
			}
		}
	}
}

// readLines splits the stream and closes the channel when it ends.
//
// The channel is buffered and the send is blocking rather than dropping: the
// consumer is a batching loop that empties it ten times a second, and dropping
// here would lose the lines that arrive during a burst — which are the ones
// worth reading.
func readLines(rc interface{ Read([]byte) (int, error) }, out chan<- string) {
	defer close(out)

	scanner := bufio.NewScanner(rc)
	// A stack trace or a mod dumping a table can exceed bufio's 64 KiB
	// default, and the default is a hard error that ends the stream rather
	// than a truncation.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		out <- scanner.Text()
	}
}
