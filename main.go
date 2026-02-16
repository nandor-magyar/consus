package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
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

// sessions stores active session tokens mapped to user emails.
var sessions = struct {
	mu sync.Mutex
	m  map[string]string
}{m: make(map[string]string)}

func newSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func emailFromRequest(r *http.Request) string {
	c, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.m[c.Value]
}

func isAllowedEmail(email string) bool {
	allowed := os.Getenv("ALLOWED_EMAILS")
	if allowed == "" {
		return false
	}
	for e := range strings.SplitSeq(allowed, ",") {
		if strings.TrimSpace(e) == email {
			return true
		}
	}
	return false
}

func newOAuthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email"},
		Endpoint:     google.Endpoint,
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	state := newSessionToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	if redirect := r.URL.Query().Get("redirect"); redirect != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_redirect",
			Value:    redirect,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   300,
		})
	}
	http.Redirect(w, r, newOAuthConfig().AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	// Clear state cookie
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Path: "/", MaxAge: -1})

	token, err := newOAuthConfig().Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		log.Printf("oauth exchange error: %v", err)
		http.Error(w, "oauth exchange failed", http.StatusInternalServerError)
		return
	}

	client := newOAuthConfig().Client(r.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		log.Printf("userinfo fetch error: %v", err)
		http.Error(w, "could not fetch user info", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var userInfo struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		http.Error(w, "could not parse user info", http.StatusInternalServerError)
		return
	}

	if !isAllowedEmail(userInfo.Email) {
		http.Error(w, "access denied: email not in allowlist", http.StatusForbidden)
		return
	}

	sessionToken := newSessionToken()
	sessions.mu.Lock()
	sessions.m[sessionToken] = userInfo.Email
	sessions.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24 hours
	})

	redirectTo := "/files/"
	if c, err := r.Cookie("oauth_redirect"); err == nil && c.Value != "" {
		// Only allow relative paths to prevent open redirect
		if strings.HasPrefix(c.Value, "/") {
			redirectTo = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: "oauth_redirect", Path: "/", MaxAge: -1})
	}
	http.Redirect(w, r, redirectTo, http.StatusTemporaryRedirect)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		sessions.mu.Lock()
		delete(sessions.m, c.Value)
		sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/files/", http.StatusTemporaryRedirect)
}

type BaseView struct {
	Version     string
	CurrentYear string
}

type ListView struct {
	Breadcrumbs  []Breadcrumb
	Path         string
	Files        []os.DirEntry
	Version      string
	CommentCount map[string]uint16
	IsMediaFile  func(string) bool
	UserEmail    string
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
	ID      string `json:"ID,omitempty"`
	User    string
	Content string
	When    time.Time
	Deleted bool `json:"Deleted,omitempty"`
}

func newCommentID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// commentLocks provides per-file mutual exclusion for comment read-modify-write.
var commentLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

func lockCommentFile(path string) func() {
	commentLocks.mu.Lock()
	m, ok := commentLocks.locks[path]
	if !ok {
		m = &sync.Mutex{}
		commentLocks.locks[path] = m
	}
	commentLocks.mu.Unlock()
	m.Lock()
	return m.Unlock
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
				Path:         strings.TrimPrefix(r.URL.Path, "/files/"),
				Files:        fileInfos,
				Version:      GetVersion(),
				CommentCount: commentCount,
				UserEmail:    emailFromRequest(r),
			}

			if err := tmpl.ExecuteTemplate(w, "list.html", data); err != nil {
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
		data, err := os.ReadFile(filepath.Join(commentsLocation, f.Name()))
		if err != nil {
			continue
		}
		var cf CommentFilev1
		if err := json.Unmarshal(data, &cf); err != nil {
			continue
		}
		var count uint16
		for _, c := range cf.Comments {
			if !c.Deleted {
				count++
			}
		}
		counts[f.Name()] = count
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

func renderItem(tmpl *template.Template, commentPath string) func(http.ResponseWriter, *http.Request) {
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

		var visibleComments []Commentv1
		for _, c := range commentsFile.Comments {
			if !c.Deleted {
				visibleComments = append(visibleComments, c)
			}
		}

		data := struct {
			Path            string
			MimeType        string
			Version         string
			CommentsEnabled bool
			Comments        []Commentv1
			UserEmail       string
		}{
			Path:            filePath,
			MimeType:        GetMimeTypeFromFilename(filePath),
			Version:         GetVersion(),
			CommentsEnabled: commentPath != "",
			Comments:        visibleComments,
			UserEmail:       emailFromRequest(r),
		}

		if err := tmpl.ExecuteTemplate(w, "view.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func commentSubmit(commentPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		email := emailFromRequest(r)
		if email == "" {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, fmt.Errorf("could not parse form: %w", err).Error(), http.StatusBadRequest)
			return
		}

		comment := Commentv1{
			ID:      newCommentID(),
			User:    email,
			Content: r.FormValue("content"),
			When:    time.Now(),
		}

		filePath := strings.TrimPrefix(r.URL.Path, "/comment/")
		fileCommentPath := filepath.Join(commentPath, filePath)

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(fileCommentPath), 0o755); err != nil {
			http.Error(w, fmt.Errorf("could not create comment directory: %w", err).Error(), http.StatusInternalServerError)
			return
		}

		unlock := lockCommentFile(fileCommentPath)
		defer unlock()

		// Load existing comments (if any)
		commentsFile := CommentFilev1{}
		commentBytes, err := os.ReadFile(fileCommentPath)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, fmt.Errorf("unexpected file error: %w", err).Error(), http.StatusInternalServerError)
			return
		}
		if len(commentBytes) > 0 {
			if err := json.Unmarshal(commentBytes, &commentsFile); err != nil {
				http.Error(w, fmt.Errorf("could not load comment data: %w", err).Error(), http.StatusInternalServerError)
				return
			}
		}

		// Prepend new comment and persist
		commentsFile.Comments = append([]Commentv1{comment}, commentsFile.Comments...)
		commentBytes, err = json.Marshal(commentsFile)
		if err != nil {
			http.Error(w, fmt.Errorf("could not persist comment data: %w", err).Error(), http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile(fileCommentPath, commentBytes, 0o644); err != nil {
			http.Error(w, fmt.Errorf("could not write comment file: %w", err).Error(), http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/view/%s", filePath), http.StatusSeeOther)
	}
}

func commentDelete(commentPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		email := emailFromRequest(r)
		if email == "" {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}

		commentID := r.URL.Query().Get("id")
		if commentID == "" {
			http.Error(w, "missing comment id", http.StatusBadRequest)
			return
		}

		filePath := strings.TrimPrefix(r.URL.Path, "/comment/")
		fileCommentPath := filepath.Join(commentPath, filePath)

		unlock := lockCommentFile(fileCommentPath)
		defer unlock()

		commentsFile := CommentFilev1{}
		commentBytes, err := os.ReadFile(fileCommentPath)
		if err != nil {
			http.Error(w, "comment file not found", http.StatusNotFound)
			return
		}
		if err := json.Unmarshal(commentBytes, &commentsFile); err != nil {
			http.Error(w, "could not parse comment data", http.StatusInternalServerError)
			return
		}

		found := false
		for i := range commentsFile.Comments {
			if commentsFile.Comments[i].ID == commentID {
				if commentsFile.Comments[i].User != email {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				if time.Since(commentsFile.Comments[i].When) >= 5*time.Minute {
					http.Error(w, "delete window expired", http.StatusForbidden)
					return
				}
				commentsFile.Comments[i].Deleted = true
				found = true
				break
			}
		}

		if !found {
			http.Error(w, "comment not found", http.StatusNotFound)
			return
		}

		commentBytes, err = json.Marshal(commentsFile)
		if err != nil {
			http.Error(w, "could not persist comment data", http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile(fileCommentPath, commentBytes, 0o644); err != nil {
			http.Error(w, "could not write comment file", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

type ServerConfig struct {
	Port     int
	data     string
	Comments string
}

func migrateComments(commentPath string) error {
	return filepath.WalkDir(commentPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var cf CommentFilev1
		if err := json.Unmarshal(data, &cf); err != nil {
			log.Printf("migrate: skipping %s: %v", path, err)
			return nil
		}

		modified := false
		for i := range cf.Comments {
			if cf.Comments[i].ID == "" {
				cf.Comments[i].ID = newCommentID()
				modified = true
			}
		}

		if !modified {
			return nil
		}

		out, err := json.Marshal(cf)
		if err != nil {
			return err
		}
		log.Printf("migrate: assigned IDs to comments in %s", path)
		return os.WriteFile(path, out, 0o644)
	})
}

func NewMainServer(ctx context.Context, config ServerConfig) error {
	if config.Comments != "" {
		if err := migrateComments(config.Comments); err != nil {
			log.Printf("warning: comment migration failed: %v", err)
		}
	}

	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"isMediaFile": isMediaFile,
		"isLast":      func(i, size int) bool { return i == size-1 },
		"split":       strings.Split,
		"year":        time.Now().Year,
		"canDelete":   func(t time.Time) bool { return time.Since(t) < 5*time.Minute },
	}).ParseFS(viewDir, "views/*.html", "views/partials/*"))

	mux := http.NewServeMux()

	mux.Handle("/", http.RedirectHandler("/files/", http.StatusTemporaryRedirect))
	mux.Handle("/static/", http.FileServer(http.FS(staticDir)))

	mux.HandleFunc("GET /login", handleLogin)
	mux.HandleFunc("GET /callback", handleCallback)
	mux.HandleFunc("GET /logout", handleLogout)

	// would be nice to separate file and rendering this early
	mux.HandleFunc("/files/", renderList(templates, config.data, config.Comments))

	mux.HandleFunc("GET /view/", renderItem(templates, config.Comments))

	// doubt: maybe having it on a different route has no benefits now
	mux.HandleFunc("POST /comment/", commentSubmit(config.Comments))
	mux.HandleFunc("DELETE /comment/", commentDelete(config.Comments))
	log.Printf("Starting Consus media/file server on port %d...", config.Port)
	log.Printf("DataPath: %s", config.data)
	log.Printf("CommentsPath: %s", config.Comments)
	log.Printf("OAuth: ClientID=%s ClientSecret=%s RedirectURL=%s AllowedEmails=%s",
		redact(os.Getenv("GOOGLE_CLIENT_ID")), redact(os.Getenv("GOOGLE_CLIENT_SECRET")),
		os.Getenv("GOOGLE_REDIRECT_URL"),
		os.Getenv("ALLOWED_EMAILS"),
	)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", config.Port))
	if err != nil {
		log.Fatal("could not start listening: ", err)
	}

	svr := http.Server{
		Handler: mux,
	}

	defer svr.Shutdown(ctx)

	return svr.Serve(listener)
}

type mainServer struct {
}

// comments

//

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := flag.Int("port", 7001, "Port to serve on (overridden by PORT env var)")
	data := flag.String("data", ".", "Directory to serve files from")
	comments := flag.String("comments", ".comments", "A shadow directory to store comments of files")
	flag.Parse()

	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := fmt.Sscanf(envPort, "%d", port); p != 1 || err != nil {
			log.Fatalf("invalid PORT env var: %q", envPort)
		}
	}

	err := NewMainServer(ctx, ServerConfig{
		Port:     *port,
		data:     *data,
		Comments: *comments,
	})
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

func redact(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

func isMediaFile(name string) bool {
	for _, ext := range mediaExtensions {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}
