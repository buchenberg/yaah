package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Database struct {
	conn *sql.DB
}

func Open(path string) (*Database, error) {
	if path == "" {
		path = "default.db"
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return &Database{conn: conn}, nil
}

func (d *Database) Close() error {
	return d.conn.Close()
}

func (d *Database) Migrate() error {
	_, err := d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id    TEXT PRIMARY KEY,
			name  TEXT NOT NULL,
			email TEXT NOT NULL
		)
	`)
	return err
}

func (d *Database) InsertUser(id, name, email string) error {
	_, err := d.conn.Exec(
		"INSERT INTO users (id, name, email) VALUES (?, ?, ?)",
		id, name, email,
	)
	return err
}
