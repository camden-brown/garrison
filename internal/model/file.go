package model

import "os"

// File is a config file compiled from an instance's settings. Path is relative
// to the instance's data volume, so a File is meaningful without knowing where
// that volume lives.
//
// Compile returns Files rather than writing them, which is what lets the
// settings view show a real diff against what is on disk before anything
// changes.
type File struct {
	Path string
	Mode os.FileMode
	Data []byte
}
