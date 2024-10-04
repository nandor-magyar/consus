package main

import (
	"database/sql"
	"log"
)

func setupDB() *sql.DB {
	db, err := sql.Open("sqlite3", "./consus.db")
	if err != nil {
		log.Fatal(err)
	}

	dbStructSQL := `
	PRAGMA foreign_keys = ON;
	CREATE TABLE IF NOT EXISTS user (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        username TEXT NOT NULL UNIQUE,
        password TEXT NOT NULL
    );

	CREATE TABLE IF NOT EXISTS comment (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL,
        content TEXT NOT NULL,
		userid INTEGER,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(userid) REFERENCES user(id)
    );
	`

	_, err = db.Exec(dbStructSQL)
	if err != nil {
		log.Fatal(err)
	}

	return db
}

func addUser(db *sql.DB, username, password string) error {
	insertUserSQL := `INSERT INTO user (username, password) VALUES (?, ?)`
	_, err := db.Exec(insertUserSQL, username, password)
	return err
}

func getUser(db *sql.DB, username string) (string, error) {
	var password string
	queryUserSQL := `SELECT password FROM user WHERE username = ?`
	err := db.QueryRow(queryUserSQL, username).Scan(&password)
	return password, err
}

func deleteUser(db *sql.DB, username string) error {
	deleteUserSQL := `DELETE FROM user WHERE username = ?`
	_, err := db.Exec(deleteUserSQL, username)
	return err
}

func updateUserPassword(db *sql.DB, username, newPassword string) error {
	updateUserSQL := `UPDATE user SET password = ? WHERE username = ?`
	_, err := db.Exec(updateUserSQL, newPassword, username)
	return err
}

func addComment(db *sql.DB, userID, filePath, content string) error {
	insertCommentSQL := `INSERT INTO comment (path, content, userid) VALUES (?, ?, ?)`
	_, err := db.Exec(insertCommentSQL, filePath, content, userID)
	return err
}
func getComments(db *sql.DB, filePath string) ([]string, error) {
	content := []string{}
	queryCommentsSQL := `SELECT password FROM comment WHERE path = ?`
	rows, err := db.Query(queryCommentsSQL, filePath)

	return password, err
}

func deleteComment(db *sql.DB, username string) error {
	deleteUserSQL := `DELETE FROM user WHERE username = ?`
	_, err := db.Exec(deleteUserSQL, username)
	return err
}
