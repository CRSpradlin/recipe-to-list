package main

import (
	"github.com/charmbracelet/log"
	_ "github.com/mattn/go-sqlite3"

	"database/sql"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"time"

	// "fmt"
	"errors"
	"strings"
)

const SQLITE_FILE_LOC = "./.data/"
const SQLITE_FILE_NAME = "db.sqlite"
const SQLITE_DB_SCHEMA = `
	create table if not exists recipes (
		id integer not null primary key,
		name text not null,
		ingredients text not null
	);
	create table if not exists groceries (
		id integer not null primary key,
		name text not null,
		retrieved boolean not null,
		recipe text,
		weekday text,
		dtm date not null
	);
`

const RECIPE_INGREDIENTS_DEL = "|"

type Recipe struct {
	ID          *int64
	Name        string
	Ingredients []string
}

type RootView struct {
	Recipes []Recipe
	Version int64
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
	db.QueryRow("select count(*) from recipes").Scan(&recipeCount)
	log.Info("Database Initialized", "Recipe Count", recipeCount)

	// var newRecipe = Recipe{nil, "test recipe", []string{"apple", "pie"}}
	// newRecipe, recipeCreateErr := updateRecipe(newRecipe)
	// if recipeCreateErr != nil {
	// 	panic(recipeCreateErr)
	// }

	// log.Info("New Recipe Added", "ID", *(newRecipe.ID), "Name", newRecipe.Name, "Ingredients", newRecipe.Ingredients)

	log.Info("Listening from web server...")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.HandleFunc("/", handleRootGetRequest)
	http.HandleFunc("/recipe", handleRecipePostRequest)
	http.HandleFunc("/grocery-list", func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handleGroceryListPostRequest(rw, req)
		} else {
			handleGroceryListGetRequest(rw, req)
		}
	})

	webServerError := http.ListenAndServe("0.0.0.0:3000", nil)

	if webServerError != nil {
		panic(errors.Join(webServerError, errors.New("error listening and serving web server")))
	}

}

func initSqliteDB() (*sql.DB, error) {
	_, err := os.Stat(SQLITE_FILE_LOC + SQLITE_FILE_NAME)

	if errors.Is(err, os.ErrNotExist) {
		dirErr := os.MkdirAll(SQLITE_FILE_LOC, os.ModePerm)
		if dirErr != nil {
			return nil, errors.Join(dirErr, errors.New("could not create directory for database file"))
		}

		file, createFileErr := os.Create(SQLITE_FILE_LOC + SQLITE_FILE_NAME)

		if createFileErr != nil {
			return nil, errors.Join(createFileErr, errors.New("could not create the necessary database file, please check for proper permissions"))
		}

		file.Close()
	}

	initDB, initDBErr := sql.Open("sqlite3", SQLITE_FILE_LOC+SQLITE_FILE_NAME)
	if initDBErr != nil {
		return nil, errors.Join(initDBErr, errors.New("could not open a connection to the database file"))
	}

	_, dbSchemaInitErr := initDB.Exec(SQLITE_DB_SCHEMA)

	if dbSchemaInitErr != nil {
		return nil, errors.Join(dbSchemaInitErr, errors.New("could not run the DB Schema initialization SQL script. Please ensure it is properly formatted"))
	}

	return initDB, nil
}

func getAllRecipes() ([]Recipe, error) {
	recipes := []Recipe{}

	rows, queryRowsError := db.Query("select id, name, ingredients from recipes")
	if queryRowsError != nil {
		return nil, errors.Join(queryRowsError, errors.New("failed to query all recipes"))
	}

	for rows.Next() {
		var id int64
		var name string
		var ingredientsStr string

		scanErr := rows.Scan(&id, &name, &ingredientsStr)
		if scanErr != nil {
			return nil, errors.Join(scanErr, errors.New("unable to scan for next row within get all recipes db query"))
		}
		ingredients := strings.Split(ingredientsStr, RECIPE_INGREDIENTS_DEL)

		newRecipe := Recipe{
			ID:          &id,
			Name:        name,
			Ingredients: ingredients,
		}
		log.Info("Recipe Found", "ID", newRecipe.ID, "Name", newRecipe.Name, "Ingredients", strings.Join(newRecipe.Ingredients, ","))
		recipes = append(recipes, newRecipe)
	}

	rows.Close()
	return recipes, nil
}

func updateRecipe(recipe Recipe) (Recipe, error) {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return recipe, errors.Join(dbBeginErr, errors.New("could not start database tx for recipe create/update"))
	}

	if recipe.ID == nil {
		stmt, stmtPrepareError := tx.Prepare("insert into recipes(name,ingredients) values(?,?)")
		if stmtPrepareError != nil {
			return recipe, errors.Join(stmtPrepareError, errors.New("could not prepare statement for recipe create"))
		}
		defer stmt.Close()

		result, stmtExecErr := stmt.Exec(recipe.Name, strings.Join(recipe.Ingredients, "|"))
		if stmtExecErr != nil {
			return recipe, errors.Join(stmtExecErr, errors.New("could not execute the create recipe query statement"))
		}

		createdId, sqlResultErr := result.LastInsertId()
		if sqlResultErr != nil {
			return recipe, errors.Join(sqlResultErr, errors.New("could not parse create recipe sql result and inserted id value"))
		}

		recipe.ID = &createdId

	} else {
		stmt, stmtPrepareError := tx.Prepare("update recipes set ingredients=? where id=?")
		if stmtPrepareError != nil {
			return recipe, errors.Join(stmtPrepareError, errors.New("could not prepare statement for recipe update"))
		}

		_, stmtExecErr := stmt.Exec(strings.Join(recipe.Ingredients, "|"), recipe.ID)
		if stmtExecErr != nil {
			return recipe, errors.Join(stmtExecErr, errors.New("could not execute the recipe update query statement"))
		}
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return recipe, errors.Join(txCommitErr, errors.New("could not commit the recipe update/create statement"))
	}

	return recipe, nil
}

func handleRootGetRequest(rw http.ResponseWriter, req *http.Request) {
	allrecipes, getAllRecipesError := getAllRecipes()
	if getAllRecipesError != nil {
		panic(getAllRecipesError)
	}

	log.Info("Rendering Root Path", "Recipe Count", len(allrecipes))

	// Prevent caching of the HTML page
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	rw.Header().Set("Pragma", "no-cache")
	rw.Header().Set("Expires", "0")

	tmplData := RootView{Recipes: allrecipes, Version: time.Now().Unix()}
	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.Execute(rw, tmplData)
}

func handleRecipePostRequest(rw http.ResponseWriter, req *http.Request) {
	newRecipeNameStr := req.PostFormValue("name")
	newRecipeIngredientsStr := req.PostFormValue("ingredients")

	newRecipe := Recipe{
		Name:        newRecipeNameStr,
		Ingredients: strings.Split(newRecipeIngredientsStr, RECIPE_INGREDIENTS_DEL),
	}

	recipe, createRecipeErr := updateRecipe(newRecipe)

	if createRecipeErr != nil {
		panic(createRecipeErr)
	}

	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.ExecuteTemplate(rw, "recipe", recipe)
}

func handleGroceryListPostRequest(rw http.ResponseWriter, req *http.Request) {
	req.ParseForm()
	recipeIDs := req.Form["recipe_ids[]"]
	weekdays := req.Form["weekdays[]"]

	log.Info("Creating grocery list", "RecipeCount", len(recipeIDs))

	// Process each recipe ID with its corresponding weekday
	for i, recipeIDStr := range recipeIDs {
		recipeID, err := strconv.ParseInt(recipeIDStr, 10, 64)
		if err != nil {
			log.Error("Failed to parse recipe ID", "RecipeID", recipeIDStr, "Error", err)
			continue
		}

		weekday := ""
		if i < len(weekdays) {
			weekday = weekdays[i]
		}

		log.Info("Processing recipe for grocery list", "RecipeID", recipeID, "Weekday", weekday)

		// TODO: Insert into groceries table or process as needed
		// For now, just logging the data
	}

    rw.Header().Set("HX-Redirect", "/grocery-list")
    rw.WriteHeader(http.StatusOK)
}

func handleGroceryListGetRequest(rw http.ResponseWriter, req *http.Request) {
	log.Info("Rendering Grocery List Page")

	// Prevent caching of the HTML page
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	rw.Header().Set("Pragma", "no-cache")
	rw.Header().Set("Expires", "0")

	tmpl := template.Must(template.ParseFiles("grocery-list.html"))
	tmpl.Execute(rw, nil)
}
