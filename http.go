package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Server struct {
	config *Config
	saver  *JSONFilesHistorySaver
	mux    *http.ServeMux
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

func (s *Server) chatPageHandler(w http.ResponseWriter, _ *http.Request, chatID int64) {
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

	chatFilesDirName, filesByIds, err := s.loadChatFiles(chatInfo)

	if err != nil {
		log.Info("couldn't load chat files: %v", err)
		http.Error(w, "couldn't load chat files", http.StatusInternalServerError)
		return
	}

	messages := make([]map[string]interface{}, 0, 1000)
	chatPath := s.saver.Dirpath + "/" + chatInfo.FileName

	loadResult := loadRelated(chatPath, func(t map[string]interface{}) {
		t["__ChatFileName"] = chatFilesDirName

		id := int64(t["ID"].(float64))
		if file, ok := filesByIds[id]; ok {
			t["__File"] = file
			t["__FileFullPath"] = strconv.FormatInt(file.ID, 10) + "_" + file.Name
		}

		if _, ok := t["Message"]; ok {
			// parts := splitTextByEntities(t["Message"].(string), t["Entities"].([]interface{}))
			// parts = splitTextPartsByNewlines(parts)
			t["__MessageParts"] = applyEntities(t["Message"].(string), t["Entities"].([]interface{}))
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
		return firstLetterUpper(firstName) + firstLetterUpper(lastName)
	}

	name := firstName
	if name == "" {
		name = lastName
	}

	words := strings.Fields(name)
	if len(words) >= 2 {
		return firstLetterUpper(words[0]) + firstLetterUpper(words[1])
	} else if len(words) == 1 {
		return firstLetterUpper(words[0])
	}
	return ""
}

func firstLetterUpper(str string) string {
	for _, c := range str {
		return string(unicode.ToUpper(c))
	}
	return ""
}

func applyEntities(strText string, entities []interface{}) []interface{} {
	type HTMLInsertion struct {
		html            string
		hasClosingBlock bool
	}

	runeText := []rune(strText)
	text := utf16.Encode(runeText)
	htmlInserts := make([]HTMLInsertion, len(text)+1)

	for _, entAny := range entities {
		if ent, ok := entAny.(map[string]interface{}); ok {
			entOpen := ""
			entClose := ""
			isBlock := false
			switch ent["_"] {
			case "TL_messageEntityTextURL":
				href := addDefaultScheme(ent["URL"].(string), "http")
				entOpen = `<a href="` + href + `" target="_blank">`
				entClose = `</a>`
			case "TL_messageEntityURL":
				entOffset := int64(ent["Offset"].(float64))
				entLength := int64(ent["Length"].(float64))
				href := string(utf16.Decode(text[entOffset : entOffset+entLength]))
				href = addDefaultScheme(href, "http")
				entOpen = `<a href="` + href + `" target="_blank">`
				entClose = `</a>`
			case "TL_messageEntityBold":
				entOpen, entClose = `<b>`, `</b>`
			case "TL_messageEntityItalic":
				entOpen, entClose = `<i>`, `</i>`
			case "TL_messageEntityUnderline":
				entOpen, entClose = `<u>`, `</u>`
			case "TL_messageEntityStrike":
				entOpen, entClose = `<s>`, `</s>`
			case "TL_messageEntityCode":
				entOpen, entClose = `<code>`, `</code>`
			case "TL_messageEntityPre":
				entOpen, entClose = `<pre>`, `</pre>`
				isBlock = true
			case "TL_messageEntityBlockquote":
				entOpen, entClose = `<blockquote>`, `</blockquote>`
				isBlock = true
			}

			if entOpen != "" {
				entOffset := int64(ent["Offset"].(float64))
				entLength := int64(ent["Length"].(float64))

				htmlInserts[entOffset].html += entOpen
				htmlInserts[entOffset+entLength].html = entClose + htmlInserts[entOffset+entLength].html
				if isBlock {
					htmlInserts[entOffset+entLength].hasClosingBlock = true
				}
			}
		}
	}

	var res []interface{}
	lastEntityEnd := 0
	lastEntityWasBlock := false

	addTextLines := func(endI int) {
		lines := strings.Split(string(utf16.Decode(text[lastEntityEnd:endI])), "\n")
		if lastEntityWasBlock && lines[0] == "" {
			lines = lines[1:] //removing empty newline after block entity (such as <pre>)
		}
		for i, line := range lines {
			if i > 0 {
				res = append(res, template.HTML("<br>"))
			}
			if line != "" {
				res = append(res, line)
			}
		}
	}

	for i, ins := range htmlInserts {
		if ins.html != "" {
			if lastEntityEnd < i {
				addTextLines(i)
			}

			res = append(res, template.HTML(ins.html))

			lastEntityEnd = i
			lastEntityWasBlock = ins.hasClosingBlock
		}
	}
	addTextLines(len(text))
	return res
}

// https://datatracker.ietf.org/doc/html/rfc1738#section-5
var urlSchemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.\-]*:`)

func addDefaultScheme(url, defaultScheme string) string {
	scheme := urlSchemeRe.FindString(url)
	if scheme == "" {
		if !strings.HasPrefix(url, "//") {
			url = "//" + url
		}
		url = defaultScheme + ":" + url
	}
	return url
}

func (s *Server) processIdNameFiles(dirPath string, callback func(id int64, name string, file fs.DirEntry) bool) error {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("couldn't open directory %s: %w", dirPath, err)
	}

	pattern := regexp.MustCompile(`^(\d+)_?(.*)$`)

	for _, file := range files {
		matches := pattern.FindStringSubmatch(file.Name())
		if matches != nil {
			id, err := strconv.ParseInt(matches[1], 10, 64)
			if err != nil {
				log.Info("Error converting ID: %v", err)
				continue
			}
			name := matches[2]
			next := callback(id, name, file)
			if !next {
				return nil
			}
		}
	}
	return nil
}

func (s *Server) loadChats() ([]StoredChatInfo, error) {
	var chatInfos []StoredChatInfo

	err := s.processIdNameFiles(s.saver.Dirpath, func(id int64, name string, file fs.DirEntry) bool {
		if !file.IsDir() {
			chatInfo := StoredChatInfo{
				ID:           id,
				Name:         name,
				FirstLetters: extractFirstTwoLetters(name, ""),
				FileName:     file.Name(),
			}
			chatInfos = append(chatInfos, chatInfo)
		}
		return true
	})

	if err != nil {
		return nil, err
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

func (s *Server) loadChatFiles(chat StoredChatInfo) (string, map[int64]File, error) {
	fileNamesById := make(map[int64]File)
	chatFilesDirName := ""

	// sometimes directory name for stored files might not be in sync with chat history file
	err := s.processIdNameFiles(s.saver.Dirpath+"/files/", func(id int64, name string, file fs.DirEntry) bool {
		if file.IsDir() && id == chat.ID {
			if chatFilesDirName == "" {
				chatFilesDirName = file.Name()
			} else {
				// in case more than one directory is found it's not certain which one should be used
				log.Info("More than one stored files folders found for chat: %s (%d)", chat.Name, chat.ID)

				chatFilesDirName = ""
				return false
			}
		}
		return true
	})

	if err != nil || chatFilesDirName == "" {
		return "", fileNamesById, err
	}

	dirPath := s.saver.Dirpath + "/files/" + chatFilesDirName

	err = s.processIdNameFiles(dirPath, func(id int64, name string, file fs.DirEntry) bool {
		if !file.IsDir() {
			info, err := file.Info()
			if err != nil {
				log.Info("Error getting file %s info: %v", dirPath+"/"+file.Name(), err)
			}

			fileNamesById[id] = File{
				ID:   id,
				Name: name,
				Size: info.Size(),
			}
		}
		return true
	})

	return chatFilesDirName, fileNamesById, nil
}

func (s *Server) renderTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	templates := template.New("").Funcs(template.FuncMap{
		"formatDate": func(date interface{}) string {
			return time.Unix(int64(date.(float64)), 0).Format("02.01.2006 15:04:05")
		},
		"safe_url": func(s string) template.URL {
			return template.URL(s)
		},
		"firstLetters": extractFirstTwoLetters,
		"humanizeSize": func(b int64) string {
			const unit = 1000
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}

			prefixes := "KMGTPE"
			div, exp := int64(unit), 0
			for b > div*unit && exp < len(prefixes)-1 {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), prefixes[exp])
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

	filesDir := http.Dir(config.OutDirPath + "/files")
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(filesDir)))

	staticFS, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	log.Info("Starting server on http://%s", addr) //"http://" makes the address openable with Ctrl+Click in some terminal emulators (like GNOME Terminal)
	if err := http.ListenAndServe(addr, server); err != nil {
		return err
	}
	return nil
}
