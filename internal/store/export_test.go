package store

// SetVersionForTest forces the recorded schema version, so a test can simulate
// a database written by a newer Garrison.
func SetVersionForTest(d *DB, v int) error {
	_, err := d.sql.Exec(`UPDATE schema_version SET version = ?`, v)
	return err
}
