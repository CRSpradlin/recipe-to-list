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
	"strings"
)

const SQLITE_FILE_LOC = "./.data/"
const SQLITE_FILE_NAME = "db.sqlite"
const SQLITE_DB_SCHEMA = `
	create table if not exists recipes (
		id integer not null primary key,
		ingredients text not null
	);
`

type Recipe struct {
	id          *int64
	ingredients []string
}

var db *sql.DB

func main() {

	var dbErr error
	db, dbErr = initSqliteDB()
	if dbErr != nil {
		panic(dbErr)
	}
	defer db.Close()

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

	_, dbSchemaInitErr := initDB.Exec(SQLITE_DB_SCHEMA)

	if dbSchemaInitErr != nil {
		return nil, errors.Join(dbSchemaInitErr, errors.New("Could not run the DB Schema initialization SQL script. Please ensure it is properly formatted."))
	}

	return initDB, nil
}

func handleRecipeUpdate(recipe Recipe) (Recipe, error) {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return recipe, errors.Join(dbBeginErr, errors.New("Could not start database tx for recipe create/update."))
	}

	if recipe.id == nil {
		stmt, stmtPrepareError := tx.Prepare("insert into recipes(ingredients) values(?)")
		if stmtPrepareError != nil {
			return recipe, errors.Join(stmtPrepareError, errors.New("Could not prepare statement for recipe create."))
		}
		defer stmt.Close()

		result, stmtExecErr := stmt.Exec(strings.Join(recipe.ingredients, "|"))
		if stmtExecErr != nil {
			return recipe, errors.Join(stmtExecErr, errors.New("Could not execute the create recipe query statement."))
		}

		createdId, sqlResultErr := result.LastInsertId()
		if sqlResultErr != nil {
			return recipe, errors.Join(sqlResultErr, errors.New("Could not parse create recipe sql result and inserted id value."))
		}

		recipe.id = &createdId

	} else {
		stmt, stmtPrepareError := tx.Prepare("update recipes set ingredients=? where id=?")
		if stmtPrepareError != nil {
			return recipe, errors.Join(stmtPrepareError, errors.New("Could not prepare statement for recipe update."))
		}

		_, stmtExecErr := stmt.Exec(strings.Join(recipe.ingredients, "|"), recipe.id)
		if stmtExecErr != nil {
			return recipe, errors.Join(stmtExecErr, errors.New("Could not execute the recipe update query statement."))
		}
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return recipe, errors.Join(txCommitErr, errors.New("Could not commit the recipe update/create statement."))
	}

	return recipe, nil
}

func handleRootRequest(rw http.ResponseWriter, req *http.Request) {
	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.Execute(rw, nil)
}
