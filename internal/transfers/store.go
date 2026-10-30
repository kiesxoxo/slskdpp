package transfers

import (
	"database/sql"
	"strings"
	"time"
)

const downloadCols = `id, username, filename, size, dest, state, bytes, attempts, error, tried_users, next_attempt, created, updated, completed`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func loadDownloads(db *sql.DB) ([]*Download, error) {
	rows, err := db.Query(`SELECT ` + downloadCols + ` FROM downloads ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Download
	for rows.Next() {
		var d Download
		var state, tried string
		var next, created, updated, completed int64
		if err := rows.Scan(&d.ID, &d.Username, &d.Filename, &d.Size, &d.Dest, &state, &d.Bytes,
			&d.Attempts, &d.Error, &tried, &next, &created, &updated, &completed); err != nil {
			return nil, err
		}
		d.State = State(state)
		if tried != "" {
			d.Tried = strings.Split(tried, "\n")
		}
		d.NextAttempt, d.Created, d.Updated, d.Completed = fromMS(next), fromMS(created), fromMS(updated), fromMS(completed)
		out = append(out, &d)
	}
	return out, rows.Err()
}

func insertDownload(db *sql.DB, d *Download) error {
	res, err := db.Exec(`INSERT INTO downloads (username, filename, size, dest, state, bytes, attempts, error, tried_users, next_attempt, created, updated, completed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Username, d.Filename, d.Size, d.Dest, string(d.State), d.Bytes, d.Attempts, d.Error,
		strings.Join(d.Tried, "\n"), ms(d.NextAttempt), ms(d.Created), ms(d.Updated), ms(d.Completed))
	if err != nil {
		return err
	}
	d.ID, err = res.LastInsertId()
	return err
}

func updateDownload(db *sql.DB, d *Download) error {
	_, err := db.Exec(`UPDATE downloads SET username = ?, filename = ?, size = ?, dest = ?, state = ?, bytes = ?,
		attempts = ?, error = ?, tried_users = ?, next_attempt = ?, updated = ?, completed = ? WHERE id = ?`,
		d.Username, d.Filename, d.Size, d.Dest, string(d.State), d.Bytes, d.Attempts, d.Error,
		strings.Join(d.Tried, "\n"), ms(d.NextAttempt), ms(d.Updated), ms(d.Completed), d.ID)
	return err
}

func deleteDownload(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM downloads WHERE id = ?`, id)
	return err
}
