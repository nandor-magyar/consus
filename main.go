package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

type ListView struct {
	Breadcrumbs  []Breadcrumb
	Path         string
	Files        []os.DirEntry
	Version      string
	CommentCount map[string]uint16
	IsMediaFile  func(string) bool
}

type Breadcrumb struct {
	Name   string
	URL    string
	IsLast bool
}

type CommentFilev1 struct {
	Comments []Commentv1
}

type Commentv1 struct {
	User    string
	Content string
	When    time.Time
}

func GetVersion() string {
	return version
}

func renderList(tmpl *template.Template, contentPath, commentPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		contentLocation := filepath.Join(contentPath, strings.TrimPrefix(r.URL.Path, "/files"))
		info, err := os.Stat(contentLocation)
		if os.IsNotExist(err) {
			log.Printf("%s", err.Error())
			http.NotFound(w, r)
			return
		} else if err != nil {
			log.Printf("%s", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// the single most important cond. deciding if there is a anything to render or just return a file
		if info.IsDir() {
			files, err := os.ReadDir(contentLocation)
			if err != nil {
				log.Printf("%s", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			var fileInfos []os.DirEntry
			for _, file := range files {
				fileInfos = append(fileInfos, file)
			}

			commentCount, err := getCommentCountPerItem(filepath.Join(commentPath, r.URL.Path))
			if err != nil {
				log.Printf("%s", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			data := ListView{
				Breadcrumbs:  GenerateBreadcrumbs(r.URL.Path),
				Path:         r.URL.Path,
				Files:        fileInfos,
				Version:      GetVersion(),
				CommentCount: commentCount,
			}

			if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
				log.Printf("%s", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		} else {
			http.ServeFile(w, r, contentLocation)
		}
	}
}

func getCommentCountPerItem(commentsLocation string) (map[string]uint16, error) {
	counts := map[string]uint16{}

	dir, err := os.Open(commentsLocation)
	if errors.Is(err, os.ErrNotExist) {
		return counts, os.MkdirAll(commentsLocation, 0o755)
	} else if err != nil {
		return nil, err
	}
	defer dir.Close()

	files, err := dir.Readdir(-1)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
	}

	return counts, nil
}

func GenerateBreadcrumbs(path string) []Breadcrumb {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, "/files"), "/"), "/")
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

func renderView(tmpl *template.Template, commentPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		filePath := strings.TrimPrefix(r.URL.Path, "/view/")
		fileCommentPath := filepath.Join(commentPath, filePath)

		commentBytes, err := os.ReadFile(fileCommentPath)
		if err != nil {
			if !os.IsNotExist(err) {
				http.Error(w, fmt.Sprintf("error while reading %s", err.Error()), http.StatusInternalServerError)
				return
			}
		}

		commentsFile := CommentFilev1{}
		if len(commentBytes) > 0 {
			err := json.Unmarshal(commentBytes, &commentsFile)
			if err != nil {
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
		}

		data := struct {
			Path     string
			MimeType string
			Version  string
			Comments []Commentv1
		}{
			Path:     filePath,
			MimeType: GetMimeTypeFromFilename(filePath),
			Version:  GetVersion(),
			Comments: commentsFile.Comments,
		}

		if err := tmpl.ExecuteTemplate(w, "view.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func commentSubmit(tmpl *template.Template, commentPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comment") {
			http.Error(w, "", http.StatusMethodNotAllowed)
			return
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, fmt.Errorf("could not parse form: %w", err).Error(), http.StatusBadRequest)
				return
			}

			comment := Commentv1{
				User:    r.FormValue("user"),
				Content: r.FormValue("content"),
				When:    time.Now(),
			}

			fileCommentPath := filepath.Join(commentPath, strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/comment"), "/view/"))
			commentBytes, err := os.ReadFile(fileCommentPath)
			if err != nil {
				if os.IsNotExist(err) {
					os.WriteFile(fileCommentPath, commentBytes, os.ModePerm)
				} else {
					http.Error(w, fmt.Errorf("unexpected file error: %w", err).Error(), http.StatusInternalServerError)
					return
				}
			} else {
				commentsFile := CommentFilev1{}
				if len(commentBytes) > 0 {
					err := json.Unmarshal(commentBytes, &commentsFile)
					if err != nil {
						http.Error(w, fmt.Errorf("could not load comment data: %w", err).Error(), http.StatusInternalServerError)
						return
					}
				}
				commentsFile.Comments = append([]Commentv1{comment}, commentsFile.Comments...)
				commentBytes, err = json.Marshal(commentsFile)
				if err != nil {
					http.Error(w, fmt.Errorf("could not persist comment data: %w", err).Error(), http.StatusInternalServerError)
					return
				}

				os.WriteFile(fileCommentPath, commentBytes, os.ModePerm)
				r.URL.Path = strings.TrimSuffix(r.URL.Path, "/comment")
				renderView(tmpl, commentPath)(w, r)
			}
		}
	}
}

// we need more TYPES and comments as feature
// server

// comments

//

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := flag.Int("port", 7001, "Port to serve on")
	directory := flag.String("directory", ".", "Directory to serve files from")
	comments := flag.String("comments", ".comments", "A shadow directory to store comments of files")
	flag.Parse()

	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"isMediaFile": isMediaFile,
		"isLast":      func(i, size int) bool { return i == size-1 },
		"split":       strings.Split,
	}).ParseFS(viewDir, "views/*.html", "views/partials/*"))

	mux := http.NewServeMux()

	mux.Handle("/", http.RedirectHandler("/files/", http.StatusTemporaryRedirect))
	mux.Handle("/static/", http.FileServer(http.FS(staticDir)))
	mux.HandleFunc("/files/", renderList(templates, *directory, *comments))
	mux.HandleFunc("GET /view/", renderView(templates, *comments))
	mux.HandleFunc("POST /view/", commentSubmit(templates, *comments))
	log.Printf("Starting Consus media/file server  %s on port %d...", *directory, *port)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal("could not start listening: ", err)
	}

	svr := http.Server{
		Handler: mux,
	}

	defer svr.Shutdown(ctx)

	err = svr.Serve(listener)
	if err != nil {
		log.Fatal("serve error ", err)
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
