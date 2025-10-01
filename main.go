package main

import (
	//_ "github.com/mattn/go-sqlite3"
	"github.com/charmbracelet/log"

	"html/template"
	"net/http"
	// "fmt"
	// "errors"
	// "strings"
)

func main() {
	log.Info("Testing Compile and Runtime...")

	http.HandleFunc("/", handleRootRequest)

	err := http.ListenAndServe("0.0.0.0:3000", nil)

	if err != nil {
		panic("Error starting web server")
	}

}

func handleRootRequest(rw http.ResponseWriter, req *http.Request) {
	tmpl := template.Must(template.ParseFiles("index.html"))
	tmpl.Execute(rw, nil)
}
