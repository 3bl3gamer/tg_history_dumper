package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Server struct {
	config    *Config
	saver     *JSONFilesHistorySaver
	mux       *http.ServeMux
	templates *template.Template
}

type StoredChatInfo struct {
	ID           int64
	Name         string
	FirstLetters string
	FileName     string
}

type ChatPageView struct {
	Account  map[string]interface{}
	Chat     StoredChatInfo
	Messages []map[string]interface{}
}

type File struct {
	ID   int64
	Name string
	Size int64
}

func (s *Server) chatsPageHandler(w http.ResponseWriter, r *http.Request) {
	chatInfos, err := s.loadChats()
	if err != nil {
		log.Info("couldn't load chats: %v", err)
		http.Error(w, "couldn't load chats", http.StatusInternalServerError)
		return
	}

	s.renderTemplate(w, "chats.html", chatInfos)
}

func (s *Server) chatPageHandler(w http.ResponseWriter, r *http.Request, chatID int64) {
	account, err := s.loadAccountData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chatInfo, err := s.loadChatByID(chatID)
	if err != nil {
		log.Info("couldn't load chat: %v", err)
		http.Error(w, "couldn't load chat", http.StatusInternalServerError)
		return
	}

	filesByIds, err := s.loadChatFiles(chatInfo)

	if err != nil {
		log.Info("couldn't load chat files: %v", err)
		http.Error(w, "couldn't load chat files", http.StatusInternalServerError)
		return
	}

	messages := make([]map[string]interface{}, 0, 1000)
	chatPath := s.saver.Dirpath + "/" + chatInfo.FileName

	loadResult := loadRelated(chatPath, func(t map[string]interface{}) {
		t["__ChatFileName"] = chatInfo.FileName

		id := int64(t["ID"].(float64))
		if file, ok := filesByIds[id]; ok {
			t["__File"] = file
			t["__FileFullPath"] = strconv.FormatInt(file.ID, 10) + "_" + file.Name
		}
		messages = append(messages, t)
	})

	if loadResult != nil {
		http.Error(
			w,
			fmt.Sprintf("couldn't load chat file %s: %s", chatPath, loadResult),
			http.StatusInternalServerError,
		)

		return
	}

	s.renderTemplate(w, "chat.html", ChatPageView{Account: account, Chat: chatInfo, Messages: messages})
}

func (s *Server) loadAccountData() (map[string]interface{}, error) {
	account := make(map[string]interface{})
	n := 0
	accountFPath := s.saver.accountFPath()

	loadResult := loadRelated(accountFPath, func(t map[string]interface{}) {
		account = t
		n++
	})

	if loadResult != nil {
		return nil, fmt.Errorf("couldn't load accounts file %s: %s", accountFPath, loadResult)
	}

	if n > 1 || n == 0 {
		return nil, fmt.Errorf("expected only 1 line in %s, found: %d", accountFPath, n)
	}

	_, ok := account["ID"]
	if !ok {
		return nil, fmt.Errorf("malformed json: 'ID' attr is missing in %s", accountFPath)
	}

	firstName, _ := account["FirstName"].(string)
	lastName, _ := account["LastName"].(string)

	account["FirstLetters"] = extractFirstTwoLetters(firstName, lastName)

	return account, nil
}

func extractFirstTwoLetters(firstName string, lastName string) string {
	if firstName != "" && lastName != "" {
		return fmt.Sprintf("%c%c", unicode.ToUpper(rune(firstName[0])), unicode.ToUpper(rune(lastName[0])))
	}

	name := ""

	if firstName != "" {
		name = firstName
	} else {
		name = lastName
	}

	words := strings.Fields(name)
	if len(words) >= 2 {
		return fmt.Sprintf("%c%c", unicode.ToUpper(rune(words[0][0])), unicode.ToUpper(rune(words[1][0])))
	} else if len(words) == 1 {
		return fmt.Sprintf("%c", unicode.ToUpper(rune(words[0][0])))
	}
	return ""
}

func (s *Server) loadChats() ([]StoredChatInfo, error) {
	files, err := ioutil.ReadDir(s.saver.Dirpath)
	if err != nil {
		return nil, fmt.Errorf("couldn't open history directory %s: %w", s.saver.Dirpath, err)
	}

	pattern := regexp.MustCompile(`^(\d+)_?(.*)$`)

	var chatInfos []StoredChatInfo

	for _, file := range files {
		if !file.IsDir() {
			matches := pattern.FindStringSubmatch(file.Name())
			if matches != nil {
				id, err := strconv.ParseInt(matches[1], 10, 64)
				if err != nil {
					log.Info("Error converting ID: %v", err)
					continue
				}
				name := matches[2]
				chatInfo := StoredChatInfo{
					ID:           id,
					Name:         name,
					FirstLetters: extractFirstTwoLetters(name, ""),
					FileName:     file.Name(),
				}
				chatInfos = append(chatInfos, chatInfo)
			}
		}
	}

	return chatInfos, nil
}

func (s *Server) loadChatByID(chatID int64) (StoredChatInfo, error) {
	chatInfos, err := s.loadChats()
	if err != nil {
		return StoredChatInfo{}, err
	}

	for _, chat := range chatInfos {
		if chat.ID == chatID {
			return chat, nil
		}
	}

	return StoredChatInfo{}, fmt.Errorf("chat with ID %d not found", chatID)
}

func (s *Server) loadChatFiles(chat StoredChatInfo) (map[int64]File, error) {
	fileNamesById := make(map[int64]File)

	dir := s.saver.Dirpath + "/files/" + chat.FileName
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fileNamesById, nil
		}

		return nil, fmt.Errorf("couldn't open history files directory %s: %w", dir, err)
	}

	pattern := regexp.MustCompile(`^(\d+)_?(.*)$`)

	for _, file := range files {
		if !file.IsDir() {
			matches := pattern.FindStringSubmatch(file.Name())
			if matches != nil {
				id, err := strconv.ParseInt(matches[1], 10, 64)
				if err != nil {
					log.Info("Error converting ID: %v", err)
					continue
				}

				fileNamesById[id] = File{
					ID:   id,
					Name: matches[2],
					Size: file.Size(),
				}
			}
		}
	}

	return fileNamesById, nil
}

func (s *Server) renderTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	templates := template.New("").Funcs(template.FuncMap{
		"formatDate": func(date interface{}) string {
			return time.Unix(int64(date.(float64)), 0).Format("02.01.2006 15:04:05")
		},
		"nl2br": func(text string) template.HTML {
			return template.HTML(strings.Replace(text, "\n", "<br>", -1))
		},
		"firstLetters": extractFirstTwoLetters,
		"humanizeSize": func(b int64) string {
			const unit = 1000
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}

			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
		},
	})

	// Parse the layout and the specific template
	templates, err := templates.ParseFS(templatesFS, "templates/layout.html", "templates/"+tmpl)
	if err != nil {
		log.Info("Error parsing templates: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = templates.ExecuteTemplate(w, "layout.html", data)
	if err != nil {
		log.Info("Error rendering template %s: %v", tmpl, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/chats/") {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) == 3 {
			chatID, err := strconv.ParseInt(parts[2], 10, 64)
			if err == nil {
				s.chatPageHandler(w, r, chatID)
				return
			}
		}
	}

	s.mux.ServeHTTP(w, r)
}

func serveHttp(addr string, config *Config, saver *JSONFilesHistorySaver) error {
	server := &Server{
		config: config,
		saver:  saver,
	}

	mux := http.NewServeMux()
	server.mux = mux
	mux.Handle("/", http.RedirectHandler("/chats/", http.StatusFound))
	mux.HandleFunc("/chats/", server.chatsPageHandler)

	filesDir := http.Dir("./history/files")
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(filesDir)))

	staticFS, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	log.Info("Starting server on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		return err
	}
	return nil
}
