package main

import (
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gorilla/sessions"
	_ "github.com/mattn/go-sqlite3"

	"bytes"
	"database/sql"
	"encoding/gob"
	"encoding/json"
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
		dtm date not null,
		recurring boolean not null
	);
	create table if not exists users (
		id integer not null primary key,
		username text not null unique,
		display_name text not null
	);
	create table if not exists credentials (
		id blob not null primary key,
		user_id integer not null,
		public_key blob not null,
		attestation_type text,
		aaguid blob,
		sign_count integer not null,
		clone_warning boolean not null default 0,
		backup_eligible boolean not null default 0,
		backup_state boolean not null default 0,
		transport text,
		foreign key (user_id) references users(id) on delete cascade
	);
`

const RECIPE_INGREDIENTS_DEL = "|"

type User struct {
	ID          int64
	Username    string
	DisplayName string
	credentials []webauthn.Credential
}

// WebAuthn User interface implementation
func (u *User) WebAuthnID() []byte {
	return []byte(strconv.FormatInt(u.ID, 10))
}

func (u *User) WebAuthnName() string {
	return u.Username
}

func (u *User) WebAuthnDisplayName() string {
	return u.DisplayName
}

func (u *User) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

func (u *User) WebAuthnIcon() string {
	return ""
}

type Recipe struct {
	ID          *int64
	Name        string
	Ingredients []string
}

type GroceryItem struct {
	ID        *int64
	Name      string
	Retrieved bool
	Recipe    string
	Weekday   string
	Dtm       time.Time
	Recurring bool
}

type UserView struct {
	Version int64
}

type RootView struct {
	Recipes []Recipe
	Version int64
}

type GroceryListView struct {
	GroceryItems []GroceryItem
	Version      int64
}

var db *sql.DB
var webAuthn *webauthn.WebAuthn
var store *sessions.CookieStore

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

	// Initialize session store with a secure random key
	// In production, use a key from environment variable
	store = sessions.NewCookieStore([]byte("super-secret-key-change-this-in-production-32bytes"))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteLaxMode,
	}

	// Initialize WebAuthn
	var webAuthnErr error
	webAuthn, webAuthnErr = webauthn.New(&webauthn.Config{
		RPDisplayName: "Brittany's Recipe App",
		RPID:          os.Getenv("BGP_RPID"),
		RPOrigins:     []string{"http://localhost:8080", fmt.Sprintf("https://%s", os.Getenv("BGP_RPID"))},
	})
	if webAuthnErr != nil {
		panic(errors.Join(webAuthnErr, errors.New("failed to initialize WebAuthn")))
	}

	// var newRecipe = Recipe{nil, "test recipe", []string{"apple", "pie"}}
	// newRecipe, recipeCreateErr := updateRecipe(newRecipe)
	// if recipeCreateErr != nil {
	// 	panic(recipeCreateErr)
	// }

	// log.Info("New Recipe Added", "ID", *(newRecipe.ID), "Name", newRecipe.Name, "Ingredients", newRecipe.Ingredients)

	log.Info("Listening from web server...")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

	// Auth endpoints (no auth required)
	http.HandleFunc("/login", handleLoginPage)
	http.HandleFunc("/register", handleRegisterPage)
	http.HandleFunc("/register/begin", handleRegisterBegin)
	http.HandleFunc("/register/finish", handleRegisterFinish)
	http.HandleFunc("/login/begin", handleLoginBegin)
	http.HandleFunc("/login/finish", handleLoginFinish)
	http.HandleFunc("/logout", handleLogout)

	// Protected endpoints
	http.HandleFunc("/", requireAuth(handleRootGetRequest))
	http.HandleFunc("/recipe", requireAuth(handleRecipePostRequest))
	http.HandleFunc("/recipe/", requireAuth(func(rw http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodDelete:
			handleRecipeDeleteRequest(rw, req)
		case http.MethodPut, http.MethodPatch:
			handleRecipeUpdateRequest(rw, req)
		default:
			http.NotFound(rw, req)
		}
	}))
	http.HandleFunc("/grocery-item", requireAuth(handleGroceryItemPostRequest))
	http.HandleFunc("/grocery-item/", requireAuth(func(rw http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodDelete:
			handleGroceryItemDeleteRequest(rw, req)
		case http.MethodPatch:
			handleGroceryItemPatchRequest(rw, req)
		default:
			http.NotFound(rw, req)
		}
	}))
	http.HandleFunc("/grocery-list", requireAuth(func(rw http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			handleGroceryListPostRequest(rw, req)
		case http.MethodDelete:
			handleGroceryListDeleteRequest(rw, req)
		default:
			handleGroceryListGetRequest(rw, req)
		}
	}))

	log.Info("Server starting on http://localhost:8080")
	webServerError := http.ListenAndServe("0.0.0.0:8080", nil)

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

// Authentication middleware
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, req *http.Request) {
		session, _ := store.Get(req, "auth-session")
		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
			http.Redirect(rw, req, "/login", http.StatusSeeOther)
			return
		}
		next(rw, req)
	}
}

// User database functions
func getUserByUsername(username string) (*User, error) {
	user := &User{}
	err := db.QueryRow("select id, username, display_name from users where username=?", username).
		Scan(&user.ID, &user.Username, &user.DisplayName)
	if err != nil {
		return nil, err
	}

	// Load credentials
	rows, err := db.Query(`
		select id, public_key, attestation_type, aaguid, sign_count, clone_warning, backup_eligible, backup_state, transport 
		from credentials where user_id=?`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var cred webauthn.Credential
		var id, publicKey, aaguid []byte
		var attestationType, transport string
		var signCount uint32
		var cloneWarning, backupEligible, backupState bool

		err := rows.Scan(&id, &publicKey, &attestationType, &aaguid, &signCount, &cloneWarning, &backupEligible, &backupState, &transport)
		if err != nil {
			return nil, err
		}

		cred.ID = id
		cred.PublicKey = publicKey
		cred.AttestationType = attestationType
		cred.Authenticator.AAGUID = aaguid
		cred.Authenticator.SignCount = signCount
		cred.Authenticator.CloneWarning = cloneWarning
		cred.Flags.BackupEligible = backupEligible
		cred.Flags.BackupState = backupState

		// Parse transport
		if transport != "" {
			var transports []protocol.AuthenticatorTransport
			parts := strings.Split(transport, ",")
			for _, t := range parts {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
			cred.Transport = transports
		}

		user.credentials = append(user.credentials, cred)
	}

	return user, nil
}

func getUserByID(id int64) (*User, error) {
	user := &User{}
	err := db.QueryRow("select id, username, display_name from users where id=?", id).
		Scan(&user.ID, &user.Username, &user.DisplayName)
	if err != nil {
		return nil, err
	}

	// Load credentials
	rows, err := db.Query(`
		select id, public_key, attestation_type, aaguid, sign_count, clone_warning, backup_eligible, backup_state, transport 
		from credentials where user_id=?`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var cred webauthn.Credential
		var id, publicKey, aaguid []byte
		var attestationType, transport string
		var signCount uint32
		var cloneWarning, backupEligible, backupState bool

		err := rows.Scan(&id, &publicKey, &attestationType, &aaguid, &signCount, &cloneWarning, &backupEligible, &backupState, &transport)
		if err != nil {
			return nil, err
		}

		cred.ID = id
		cred.PublicKey = publicKey
		cred.AttestationType = attestationType
		cred.Authenticator.AAGUID = aaguid
		cred.Authenticator.SignCount = signCount
		cred.Authenticator.CloneWarning = cloneWarning
		cred.Flags.BackupEligible = backupEligible
		cred.Flags.BackupState = backupState

		// Parse transport
		if transport != "" {
			var transports []protocol.AuthenticatorTransport
			parts := strings.Split(transport, ",")
			for _, t := range parts {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
			cred.Transport = transports
		}

		user.credentials = append(user.credentials, cred)
	}

	return user, nil
}

func createUser(username, displayName string) (*User, error) {
	result, err := db.Exec("insert into users(username, display_name) values(?,?)", username, displayName)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &User{
		ID:          id,
		Username:    username,
		DisplayName: displayName,
		credentials: []webauthn.Credential{},
	}, nil
}

func userCount() (int, error) {
	var count int
	err := db.QueryRow("select count(*) from users").Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func registrationEnableCheck(rw http.ResponseWriter, req *http.Request, uiRedirect bool) bool {
	registeredUserCount, err := userCount()
	if err != nil {
		http.Error(rw, "Registered user validation check failed", http.StatusInternalServerError)
		return false
	}

	if (registeredUserCount > 2) {
		if uiRedirect {
			http.Redirect(rw, req, "/login", http.StatusSeeOther)
			return false
		} else {
			http.NotFound(rw, req)
			return false
		}
	}
	return true
}

func saveCredential(userID int64, cred *webauthn.Credential) error {
	transport := ""
	if len(cred.Transport) > 0 {
		var parts []string
		for _, t := range cred.Transport {
			parts = append(parts, string(t))
		}
		transport = strings.Join(parts, ",")
	}

	_, err := db.Exec(`
		insert into credentials(id, user_id, public_key, attestation_type, aaguid, sign_count, clone_warning, backup_eligible, backup_state, transport)
		values(?,?,?,?,?,?,?,?,?,?)`,
		cred.ID, userID, cred.PublicKey, cred.AttestationType, cred.Authenticator.AAGUID,
		cred.Authenticator.SignCount, cred.Authenticator.CloneWarning, cred.Flags.BackupEligible, cred.Flags.BackupState, transport)
	return err
}

func updateCredential(userID int64, cred *webauthn.Credential) error {
	_, err := db.Exec(`
		update credentials set sign_count=?, clone_warning=?, backup_eligible=?, backup_state=? where id=? and user_id=?`,
		cred.Authenticator.SignCount, cred.Authenticator.CloneWarning, cred.Flags.BackupEligible, cred.Flags.BackupState, cred.ID, userID)
	return err
}

// Auth handlers
func handleLoginPage(rw http.ResponseWriter, req *http.Request) {
	// Prevent caching
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	rw.Header().Set("Pragma", "no-cache")
	rw.Header().Set("Expires", "0")

	tmplData := UserView{ Version: time.Now().Unix() }
	tmpl := template.Must(template.ParseFiles("login.html"))
	tmpl.Execute(rw, tmplData)
}

func handleRegisterPage(rw http.ResponseWriter, req *http.Request) {
	// Is registration enabled?
	if !registrationEnableCheck(rw, req, true) {
		return
	}

	// Prevent caching
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	rw.Header().Set("Pragma", "no-cache")
	rw.Header().Set("Expires", "0")

	tmplData := UserView{ Version: time.Now().Unix() }
	tmpl := template.Must(template.ParseFiles("register.html"))
	tmpl.Execute(rw, tmplData)
}

func handleRegisterBegin(rw http.ResponseWriter, req *http.Request) {
	// Is registration enabled?
	if !registrationEnableCheck(rw, req, false) {
		return
	}
	
	if req.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var requestData struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}

	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		log.Error("Failed to decode register begin request", "Error", err)
		http.Error(rw, "Invalid request", http.StatusBadRequest)
		return
	}

	// Check if user already exists
	existingUser, _ := getUserByUsername(requestData.Username)
	if existingUser != nil {
		http.Error(rw, "Username already exists", http.StatusConflict)
		return
	}

	// Create new user
	user, err := createUser(requestData.Username, requestData.DisplayName)
	if err != nil {
		log.Error("Failed to create user", "Error", err)
		http.Error(rw, "Failed to create user", http.StatusInternalServerError)
		return
	}

	// Generate registration options
	options, sessionData, err := webAuthn.BeginRegistration(user)
	if err != nil {
		log.Error("Failed to begin registration", "Error", err)
		http.Error(rw, "Failed to begin registration", http.StatusInternalServerError)
		return
	}

	// Store session data (encode to bytes for session storage)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(sessionData); err != nil {
		log.Error("Failed to encode session data", "Error", err)
		http.Error(rw, "Failed to encode session data", http.StatusInternalServerError)
		return
	}

	session, _ := store.Get(req, "auth-session")
	session.Values["registration"] = buf.Bytes()
	session.Values["userID"] = user.ID
	session.Save(req, rw)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(options)
}

func handleRegisterFinish(rw http.ResponseWriter, req *http.Request) {
	// Is registration enabled?
	if !registrationEnableCheck(rw, req, false) {
		return
	}
	
	if req.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, _ := store.Get(req, "auth-session")

	// Decode registration session data
	sessionBytes, ok := session.Values["registration"].([]byte)
	if !ok {
		http.Error(rw, "No registration in progress", http.StatusBadRequest)
		return
	}

	var sessionData webauthn.SessionData
	buf := bytes.NewBuffer(sessionBytes)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&sessionData); err != nil {
		log.Error("Failed to decode session data", "Error", err)
		http.Error(rw, "Invalid session data", http.StatusBadRequest)
		return
	}

	userID, ok := session.Values["userID"].(int64)
	if !ok {
		http.Error(rw, "No user ID in session", http.StatusBadRequest)
		return
	}

	user, err := getUserByID(userID)
	if err != nil {
		log.Error("Failed to get user", "Error", err)
		http.Error(rw, "User not found", http.StatusNotFound)
		return
	}

	credential, err := webAuthn.FinishRegistration(user, sessionData, req)
	if err != nil {
		log.Error("Failed to finish registration", "Error", err)
		http.Error(rw, "Failed to finish registration", http.StatusBadRequest)
		return
	}

	// Save credential
	if err := saveCredential(user.ID, credential); err != nil {
		log.Error("Failed to save credential", "Error", err)
		http.Error(rw, "Failed to save credential", http.StatusInternalServerError)
		return
	}

	// Clear registration session data
	delete(session.Values, "registration")
	delete(session.Values, "userID")

	// Mark user as authenticated
	session.Values["authenticated"] = true
	session.Values["username"] = user.Username
	session.Values["userId"] = user.ID
	session.Save(req, rw)

	log.Info("User registered successfully", "Username", user.Username)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]string{"status": "success"})
}

func handleLoginBegin(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var requestData struct {
		Username string `json:"username"`
	}

	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		log.Error("Failed to decode login begin request", "Error", err)
		http.Error(rw, "Invalid request", http.StatusBadRequest)
		return
	}

	user, err := getUserByUsername(requestData.Username)
	if err != nil {
		http.Error(rw, "User not found", http.StatusNotFound)
		return
	}

	options, sessionData, err := webAuthn.BeginLogin(user)
	if err != nil {
		log.Error("Failed to begin login", "Error", err)
		http.Error(rw, "Failed to begin login", http.StatusInternalServerError)
		return
	}

	// Store session data (encode to bytes for session storage)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(sessionData); err != nil {
		log.Error("Failed to encode session data", "Error", err)
		http.Error(rw, "Failed to encode session data", http.StatusInternalServerError)
		return
	}

	session, _ := store.Get(req, "auth-session")
	session.Values["authentication"] = buf.Bytes()
	session.Values["userID"] = user.ID
	session.Save(req, rw)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(options)
}

func handleLoginFinish(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, _ := store.Get(req, "auth-session")

	// Decode authentication session data
	sessionBytes, ok := session.Values["authentication"].([]byte)
	if !ok {
		http.Error(rw, "No authentication in progress", http.StatusBadRequest)
		return
	}

	var sessionData webauthn.SessionData
	buf := bytes.NewBuffer(sessionBytes)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&sessionData); err != nil {
		log.Error("Failed to decode session data", "Error", err)
		http.Error(rw, "Invalid session data", http.StatusBadRequest)
		return
	}

	userID, ok := session.Values["userID"].(int64)
	if !ok {
		http.Error(rw, "No user ID in session", http.StatusBadRequest)
		return
	}

	user, err := getUserByID(userID)
	if err != nil {
		log.Error("Failed to get user", "Error", err)
		http.Error(rw, "User not found", http.StatusNotFound)
		return
	}

	credential, err := webAuthn.FinishLogin(user, sessionData, req)
	if err != nil {
		log.Error("Failed to finish login", "Error", err)
		http.Error(rw, "Failed to finish login", http.StatusBadRequest)
		return
	}

	// Update credential sign count
	if credential.Authenticator.SignCount > 0 {
		if err := updateCredential(user.ID, credential); err != nil {
			log.Error("Failed to update credential", "Error", err)
		}
	}

	// Clear authentication session data
	delete(session.Values, "authentication")
	delete(session.Values, "userID")

	// Mark user as authenticated
	session.Values["authenticated"] = true
	session.Values["username"] = user.Username
	session.Values["userId"] = user.ID
	session.Save(req, rw)

	log.Info("User logged in successfully", "Username", user.Username)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]string{"status": "success"})
}

func handleLogout(rw http.ResponseWriter, req *http.Request) {
	session, _ := store.Get(req, "auth-session")
	session.Values["authenticated"] = false
	delete(session.Values, "username")
	delete(session.Values, "userId")
	session.Save(req, rw)

	http.Redirect(rw, req, "/login", http.StatusSeeOther)
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

func getAllGroceryItems() ([]GroceryItem, error) {
	items := []GroceryItem{}

	rows, queryRowsError := db.Query("select id, name, retrieved, recipe, weekday, dtm from groceries order by name asc")
	if queryRowsError != nil {
		return nil, errors.Join(queryRowsError, errors.New("failed to query all grocery items"))
	}

	for rows.Next() {
		var id int64
		var name string
		var retrieved bool
		var recipe string
		var weekday string
		var dtm time.Time

		scanErr := rows.Scan(&id, &name, &retrieved, &recipe, &weekday, &dtm)
		if scanErr != nil {
			return nil, errors.Join(scanErr, errors.New("unable to scan for next row within get all grocery items db query"))
		}

		newItem := GroceryItem{
			ID:        &id,
			Name:      name,
			Retrieved: retrieved,
			Recipe:    recipe,
			Weekday:   weekday,
			Dtm:       dtm,
		}
		log.Info("Grocery Item Found", "ID", newItem.ID, "Name", newItem.Name, "Retrieved", newItem.Retrieved, "Recipe", newItem.Recipe, "Weekday", newItem.Weekday)
		items = append(items, newItem)
	}

	rows.Close()
	return items, nil
}

func resetAllGroceryItems() error {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return errors.Join(dbBeginErr, errors.New("could not start database tx for resetting grocery items"))
	}

	// Delete all non-recurring items
	deleteStmt, deletePrepareError := tx.Prepare("delete from groceries where recurring = 0")
	if deletePrepareError != nil {
		tx.Rollback()
		return errors.Join(deletePrepareError, errors.New("could not prepare statement for deleting non-recurring items"))
	}
	defer deleteStmt.Close()

	_, deleteExecErr := deleteStmt.Exec()
	if deleteExecErr != nil {
		tx.Rollback()
		return errors.Join(deleteExecErr, errors.New("could not execute delete non-recurring items statement"))
	}

	// Reset retrieved status for all recurring items
	updateStmt, updatePrepareError := tx.Prepare("update groceries set retrieved = 0 where recurring = 1")
	if updatePrepareError != nil {
		tx.Rollback()
		return errors.Join(updatePrepareError, errors.New("could not prepare statement for resetting recurring items"))
	}
	defer updateStmt.Close()

	_, updateExecErr := updateStmt.Exec()
	if updateExecErr != nil {
		tx.Rollback()
		return errors.Join(updateExecErr, errors.New("could not execute reset recurring items statement"))
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return errors.Join(txCommitErr, errors.New("could not commit the reset grocery items transaction"))
	}

	return nil
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
		stmt, stmtPrepareError := tx.Prepare("update recipes set name=?, ingredients=? where id=?")
		if stmtPrepareError != nil {
			return recipe, errors.Join(stmtPrepareError, errors.New("could not prepare statement for recipe update"))
		}

		_, stmtExecErr := stmt.Exec(recipe.Name, strings.Join(recipe.Ingredients, "|"), recipe.ID)
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

func deleteRecipe(id int64) error {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return errors.Join(dbBeginErr, errors.New("could not start database tx for recipe delete"))
	}

	stmt, stmtPrepareError := tx.Prepare("delete from recipes where id=?")
	if stmtPrepareError != nil {
		return errors.Join(stmtPrepareError, errors.New("could not prepare statement for recipe delete"))
	}
	defer stmt.Close()

	_, stmtExecErr := stmt.Exec(id)
	if stmtExecErr != nil {
		return errors.Join(stmtExecErr, errors.New("could not execute the recipe delete query statement"))
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return errors.Join(txCommitErr, errors.New("could not commit the recipe delete statement"))
	}

	return nil
}

func updateGroceryItem(item GroceryItem) (GroceryItem, error) {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return item, errors.Join(dbBeginErr, errors.New("could not start database tx for grocery item create/update"))
	}

	if item.ID == nil {
		stmt, stmtPrepareError := tx.Prepare("insert into groceries(name,retrieved,recipe,weekday,dtm,recurring) values(?,?,?,?,?,?)")
		if stmtPrepareError != nil {
			return item, errors.Join(stmtPrepareError, errors.New("could not prepare statement for grocery item create"))
		}
		defer stmt.Close()

		result, stmtExecErr := stmt.Exec(item.Name, item.Retrieved, item.Recipe, item.Weekday, item.Dtm, item.Recurring)
		if stmtExecErr != nil {
			return item, errors.Join(stmtExecErr, errors.New("could not execute the create grocery item query statement"))
		}

		createdId, sqlResultErr := result.LastInsertId()
		if sqlResultErr != nil {
			return item, errors.Join(sqlResultErr, errors.New("could not parse create grocery item sql result and inserted id value"))
		}

		item.ID = &createdId

	} else {
		stmt, stmtPrepareError := tx.Prepare("update groceries set name=?, retrieved=?, recipe=?, weekday=?, dtm=?, recurring=? where id=?")
		if stmtPrepareError != nil {
			return item, errors.Join(stmtPrepareError, errors.New("could not prepare statement for grocery item update"))
		}

		_, stmtExecErr := stmt.Exec(item.Name, item.Retrieved, item.Recipe, item.Weekday, item.Dtm, item.Recurring, item.ID)
		if stmtExecErr != nil {
			return item, errors.Join(stmtExecErr, errors.New("could not execute the grocery item update query statement"))
		}
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return item, errors.Join(txCommitErr, errors.New("could not commit the grocery item update/create statement"))
	}

	return item, nil
}

func deleteGroceryItem(id int64) error {
	tx, dbBeginErr := db.Begin()
	if dbBeginErr != nil {
		return errors.Join(dbBeginErr, errors.New("could not start database tx for grocery item delete"))
	}

	stmt, stmtPrepareError := tx.Prepare("delete from groceries where id=?")
	if stmtPrepareError != nil {
		return errors.Join(stmtPrepareError, errors.New("could not prepare statement for grocery item delete"))
	}
	defer stmt.Close()

	_, stmtExecErr := stmt.Exec(id)
	if stmtExecErr != nil {
		return errors.Join(stmtExecErr, errors.New("could not execute the grocery item delete query statement"))
	}

	txCommitErr := tx.Commit()
	if txCommitErr != nil {
		return errors.Join(txCommitErr, errors.New("could not commit the grocery item delete statement"))
	}

	return nil
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

func handleRecipeDeleteRequest(rw http.ResponseWriter, req *http.Request) {
	// Extract ID from path (e.g., /recipe/123)
	pathParts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(pathParts) < 2 || pathParts[1] == "" {
		http.Error(rw, "Missing id in path", http.StatusBadRequest)
		return
	}

	idStr := pathParts[1]
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil {
		log.Error("Failed to parse recipe ID", "ID", idStr, "Error", parseErr)
		http.Error(rw, "Invalid id parameter", http.StatusBadRequest)
		return
	}

	deleteErr := deleteRecipe(id)
	if deleteErr != nil {
		log.Error("Failed to delete recipe", "ID", id, "Error", deleteErr)
		http.Error(rw, "Failed to delete recipe", http.StatusInternalServerError)
		return
	}

	log.Info("Recipe deleted", "ID", id)
	rw.WriteHeader(http.StatusOK)
}

func handleRecipeUpdateRequest(rw http.ResponseWriter, req *http.Request) {
	idStr := req.PostFormValue("id")
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil {
		log.Error("Failed to parse recipe ID", "ID", idStr, "Error", parseErr)
		http.Error(rw, "Invalid id parameter", http.StatusBadRequest)
		return
	}

	// Get form values
	recipeName := req.PostFormValue("name")
	ingredientsStr := req.PostFormValue("ingredients")

	if recipeName == "" {
		http.Error(rw, "Recipe name is required", http.StatusBadRequest)
		return
	}

	if ingredientsStr == "" {
		http.Error(rw, "At least one ingredient is required", http.StatusBadRequest)
		return
	}

	// Update recipe
	recipe := Recipe{
		ID:          &id,
		Name:        recipeName,
		Ingredients: strings.Split(ingredientsStr, RECIPE_INGREDIENTS_DEL),
	}

	updatedRecipe, updateErr := updateRecipe(recipe)
	if updateErr != nil {
		log.Error("Failed to update recipe", "ID", id, "Error", updateErr)
		http.Error(rw, "Failed to update recipe", http.StatusInternalServerError)
		return
	}

	log.Info("Recipe updated", "ID", id, "Name", updatedRecipe.Name)

	// Return updated recipe HTML
	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.ExecuteTemplate(rw, "recipe", updatedRecipe)
}

func handleGroceryItemPatchRequest(rw http.ResponseWriter, req *http.Request) {
	// Parse the form data
	err := req.ParseForm()
	if err != nil {
		http.Error(rw, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	// ID is required
	idStr := req.PostFormValue("id")
	if idStr == "" {
		http.Error(rw, "Item ID is required", http.StatusBadRequest)
		return
	}

	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil {
		log.Error("Failed to parse grocery item ID", "ID", idStr, "Error", parseErr)
		http.Error(rw, "Invalid ID parameter", http.StatusBadRequest)
		return
	}

	// Fetch existing grocery item from database
	var existingItem GroceryItem
	var retrieved bool
	var recurring bool
	queryErr := db.QueryRow("select name, retrieved, recipe, weekday, dtm, recurring from groceries where id=?", id).
		Scan(&existingItem.Name, &retrieved, &existingItem.Recipe, &existingItem.Weekday, &existingItem.Dtm, &recurring)
	if queryErr != nil {
		log.Error("Failed to query grocery item", "ID", id, "Error", queryErr)
		http.Error(rw, "Grocery item not found", http.StatusNotFound)
		return
	}
	existingItem.ID = &id
	existingItem.Retrieved = retrieved
	existingItem.Recurring = recurring

	// Update with form values if provided
	if name := req.PostFormValue("name"); name != "" {
		existingItem.Name = name
	}
	if retrievedStr := req.PostFormValue("retrieved"); retrievedStr != "" {
		existingItem.Retrieved = retrievedStr == "true"
	}
	if recipe := req.PostFormValue("recipe"); recipe != "" {
		existingItem.Recipe = recipe
	}
	if weekday := req.PostFormValue("weekday"); weekday != "" {
		existingItem.Weekday = weekday
	}
	if recurringStr := req.PostFormValue("recurring"); recurringStr != "" {
		existingItem.Recurring = recurringStr == "true"
	}
	// Note: dtm is typically not updated via form, but keeping existing value

	// Update the item in the database
	updatedItem, updateErr := updateGroceryItem(existingItem)
	if updateErr != nil {
		log.Error("Failed to update grocery item", "ID", id, "Error", updateErr)
		http.Error(rw, "Failed to update grocery item", http.StatusInternalServerError)
		return
	}

	log.Info("Grocery item updated", "ID", updatedItem.ID, "Name", updatedItem.Name)

	tmpl := template.Must(template.ParseFiles("grocery-list.html"))
	tmpl.ExecuteTemplate(rw, "grocery-item", updatedItem)
}

func handleGroceryItemPostRequest(rw http.ResponseWriter, req *http.Request) {
	itemName := req.PostFormValue("name")
	if itemName == "" {
		http.Error(rw, "Item name is required", http.StatusBadRequest)
		return
	}

	recurring := req.PostFormValue("recurring") == "true"

	newItem := GroceryItem{
		Name:      itemName,
		Retrieved: false,
		Recipe:    "Ad-hoc",
		Weekday:   "Soon",
		Dtm:       time.Now(),
		Recurring: recurring,
	}

	item, createErr := updateGroceryItem(newItem)
	if createErr != nil {
		log.Error("Failed to create grocery item", "Name", itemName, "Error", createErr)
		http.Error(rw, "Failed to create grocery item", http.StatusInternalServerError)
		return
	}

	log.Info("Ad-hoc grocery item created", "ID", item.ID, "Name", item.Name)

	tmpl := template.Must(template.ParseFiles("grocery-list.html"))
	tmpl.ExecuteTemplate(rw, "grocery-item", item)
}

func handleGroceryItemDeleteRequest(rw http.ResponseWriter, req *http.Request) {
	// Extract ID from path (e.g., /grocery-item/123)
	pathParts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(pathParts) < 2 || pathParts[1] == "" {
		http.Error(rw, "Missing id in path", http.StatusBadRequest)
		return
	}

	idStr := pathParts[1]
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil {
		log.Error("Failed to parse grocery item ID", "ID", idStr, "Error", parseErr)
		http.Error(rw, "Invalid id parameter", http.StatusBadRequest)
		return
	}

	deleteErr := deleteGroceryItem(id)
	if deleteErr != nil {
		log.Error("Failed to delete grocery item", "ID", id, "Error", deleteErr)
		http.Error(rw, "Failed to delete grocery item", http.StatusInternalServerError)
		return
	}

	log.Info("Grocery item deleted", "ID", id)
	rw.WriteHeader(http.StatusOK)
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

		// Get the recipe to access its ingredients
		var recipeName string
		var ingredientsStr string
		queryErr := db.QueryRow("select name, ingredients from recipes where id=?", recipeID).Scan(&recipeName, &ingredientsStr)
		if queryErr != nil {
			log.Error("Failed to query recipe", "RecipeID", recipeID, "Error", queryErr)
			continue
		}

		ingredients := strings.Split(ingredientsStr, RECIPE_INGREDIENTS_DEL)

		// Insert each ingredient into the groceries table
		for _, ingredientName := range ingredients {
			newItem := GroceryItem{
				Name:      ingredientName,
				Retrieved: false,
				Recipe:    recipeName,
				Weekday:   weekday,
				Dtm:       time.Now(),
				Recurring: false,
			}

			item, itemErr := updateGroceryItem(newItem)
			if itemErr != nil {
				log.Error("Failed to insert grocery item", "Name", ingredientName, "Recipe", recipeName, "Error", itemErr)
				continue
			}

			log.Info("Grocery item added to list", "ID", item.ID, "Name", item.Name, "Recipe", item.Recipe, "Weekday", item.Weekday)
		}
	}

	rw.Header().Set("HX-Redirect", "/grocery-list")
	rw.WriteHeader(http.StatusOK)
}

func handleGroceryListGetRequest(rw http.ResponseWriter, req *http.Request) {
	allGroceryItems, getAllGroceryItemsError := getAllGroceryItems()
	if getAllGroceryItemsError != nil {
		panic(getAllGroceryItemsError)
	}

	log.Info("Rendering Grocery List Page", "Grocery Item Count", len(allGroceryItems))

	// Prevent caching of the HTML page
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	rw.Header().Set("Pragma", "no-cache")
	rw.Header().Set("Expires", "0")

	tmplData := GroceryListView{GroceryItems: allGroceryItems, Version: time.Now().Unix()}
	tmpl := template.Must(template.ParseFiles("grocery-list.html"))
	tmpl.Execute(rw, tmplData)
}

func handleGroceryListDeleteRequest(rw http.ResponseWriter, req *http.Request) {
	resetErr := resetAllGroceryItems()
	if resetErr != nil {
		panic(resetErr)
	}

	log.Info("Grocery list reset successfully")
	rw.WriteHeader(http.StatusOK)
}
