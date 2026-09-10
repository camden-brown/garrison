package model

import "time"

// Archive is one backup on disk.
//
// It lives in model rather than in internal/services/backup because the
// snapshot carries it to the Backups view, and core imports no service —
// services and the store meet in cmd (ADR 0007). The service keeps the name
// as an alias, so the type reads as backup.Archive where backups are made and
// model.Archive where they are rendered.
type Archive struct {
	// Path is the archive on disk, which is also what a restore is given.
	Path string
	// Name is the file name: a sortable UTC timestamp plus the extension.
	Name string
	// Taken is when the archive was made, read from the name rather than
	// from the file's mtime, which a copy or a sync would rewrite.
	Taken time.Time
	Bytes int64
}
