package main

import (
	"github.com/charmbracelet/log"
	_ "github.com/mattn/go-sqlite3"

	"database/sql"
	"html/template"
	"net/http"
	"os"

	// "fmt"
	"errors"
	// "strings"
)

const SQLITE_FILE_LOC = "./.data/"
const SQLITE_FILE_NAME = "db.sqlite"
const SQLITE_DB_SCHEMA = `
	create table if not exists recipe (
		id integer not null primary key,
		ingredients text not null
	);
`

var db *sql.DB

func main() {

	var dbErr error
	db, dbErr = initSqliteDB()
	if dbErr != nil {
		panic(dbErr)
	}

	var recipeCount int
	db.QueryRow("select count(*) from recipe").Scan(&recipeCount)
	log.Info("Database Initialized", "Recipe Count", recipeCount)

	log.Info("Preparing web server...")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.HandleFunc("/", handleRootRequest)

	webServerError := http.ListenAndServe("0.0.0.0:3000", nil)

	if webServerError != nil {
		panic(errors.Join(webServerError, errors.New("Error listening and serving web server")))
	}

}

func initSqliteDB() (*sql.DB, error) {
	_, err := os.Stat(SQLITE_FILE_LOC + SQLITE_FILE_NAME)

	if errors.Is(err, os.ErrNotExist) {
		dirErr := os.MkdirAll(SQLITE_FILE_LOC, os.ModePerm)
		if dirErr != nil {
			return nil, errors.Join(dirErr, errors.New("Could not create directory for database file."))
		}

		file, createFileErr := os.Create(SQLITE_FILE_LOC + SQLITE_FILE_NAME)

		if createFileErr != nil {
			return nil, errors.Join(createFileErr, errors.New("Could not create the necessary database file, please check for proper permissions."))
		}

		file.Close()
	}

	initDB, initDBErr := sql.Open("sqlite3", SQLITE_FILE_LOC+SQLITE_FILE_NAME)
	if initDBErr != nil {
		return nil, errors.Join(initDBErr, errors.New("Could not open a connection to the database file."))
	}
	defer initDB.Close()

	_, dbSchemaInitErr := initDB.Exec(SQLITE_DB_SCHEMA)

	if dbSchemaInitErr != nil {
		return nil, errors.Join(dbSchemaInitErr, errors.New("Could not run the DB Schema initialization SQL script. Please ensure it is properly formatted."))
	}

	return initDB, nil
}

func handleRootRequest(rw http.ResponseWriter, req *http.Request) {
	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.Execute(rw, nil)
}
