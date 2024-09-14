package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Embed files/directories
//
//go:embed views/*
var viewDir embed.FS

//go:embed static/*
var staticDir embed.FS

//go:embed version.txt
var version string

var mediaExtensions = []string{".mp4", ".mp3", ".ogg", ".webm", ".m4a"}

func GetVersion() string {
	return version
}

func listHandler(tmpl *template.Template, path string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(path, r.URL.Path)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			log.Printf("%s", err.Error())
			http.NotFound(w, r)
			return
		} else if err != nil {
			log.Printf("%s", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if info.IsDir() {
			files, err := os.ReadDir(path)
			if err != nil {
				log.Printf("%s", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			var fileInfos []os.DirEntry
			for _, file := range files {
				fileInfos = append(fileInfos, file)
			}

			data := ListView{
				Breadcrumbs: GenerateBreadcrumbs(r.URL.Path),
				Path:        r.URL.Path,
				Files:       fileInfos,
				Version:     GetVersion(),
			}

			if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
				log.Printf("%s", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		} else {
			http.ServeFile(w, r, path)
		}
	}
}

type ListView struct {
	Breadcrumbs []Breadcrumb
	Path        string
	Files       []os.DirEntry
	Version     string
	IsMediaFile func(string) bool
}

type Breadcrumb struct {
	Name   string
	URL    string
	IsLast bool
}

func GenerateBreadcrumbs(path string) []Breadcrumb {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	breadcrumbs := []Breadcrumb{}

	for i, part := range parts {
		if part == "" {
			continue
		}
		breadcrumbs = append(breadcrumbs, Breadcrumb{
			Name:   part,
			URL:    strings.Join(parts[:i+1], "/"),
			IsLast: i == len(parts)-1,
		})
	}
	return breadcrumbs
}

func playerHandler(tmpl *template.Template) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		filePath := strings.TrimPrefix(r.URL.Path, "/player/")
		data := struct {
			Path     string
			MimeType string
			Version  string
		}{
			Path:     filePath,
			MimeType: GetMimeTypeFromFilename(filePath),
			Version:  GetVersion(),
		}

		if err := tmpl.ExecuteTemplate(w, "player.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func main() {
	port := flag.Int("port", 7001, "Port to serve on")
	directory := flag.String("directory", ".", "Directory to serve files from")
	flag.Parse()

	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"isMediaFile": isMediaFile,
		"isLast":      func(i, size int) bool { return i == size-1 },
		"split":       strings.Split,
	}).ParseFS(viewDir, "views/*.html", "views/partials/*"))

	http.Handle("/static/", http.FileServer(http.FS(staticDir)))
	http.HandleFunc("/", listHandler(templates, *directory))
	http.HandleFunc("/player/", playerHandler(templates))
	log.Printf("Starting Consus media/file server  %s on port %d...", *directory, *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func GetMimeTypeFromFilename(name string) string {
	for _, ext := range mediaExtensions {
		if strings.HasSuffix(name, ext) {
			if ext == ".m4a" {
				return "audio/aac"
			}
			return fmt.Sprintf("audio/%s", strings.TrimPrefix(ext, "."))
		}
	}
	return "application/octet-stream"
}

func isMediaFile(name string) bool {
	for _, ext := range mediaExtensions {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}
