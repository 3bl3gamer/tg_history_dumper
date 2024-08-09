package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/ansel1/merry/v2"
)

//go:embed preview_templates/*.html
var templatesFS embed.FS

//go:embed preview_static/*
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
	FilePath     string
}

type ChatPageView struct {
	Account    map[string]interface{}
	Chat       StoredChatInfo
	Messages   []map[string]interface{}
	Prev       int
	Next       int
	Limit      int
	HasPrev    bool
	HasNext    bool
	TotalCount int
}

type File struct {
	ID          int64
	Name        string
	FullWebPath string
	Index       int64
	Size        int64
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

func (s *Server) chatPageHandler(w http.ResponseWriter, r *http.Request) {
	chatID, err := strconv.ParseInt(r.PathValue("chatID"), 10, 64)
	if err != nil {
		log.Info("invalid chat ID: %v", err)
		http.Error(w, "invalid chat ID", http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	fromStr := r.URL.Query().Get("from")
	limit := 10000
	from := 0

	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			log.Info("invalid limit: %v", err)
			http.Error(w, "invalid limit parameter", http.StatusBadRequest)
			return
		}
	}

	if fromStr != "" {
		from, err = strconv.Atoi(fromStr)
		if err != nil {
			log.Info("invalid from: %v", err)
			http.Error(w, "invalid from parameter", http.StatusBadRequest)
			return
		}
	}

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

	usersData := make(map[int64]*UserData, 1000)
	loadRelated(s.saver.usersFPath(), func(user UserData) {
		// users file might contain duplicates however no extra logic is needed to handle it
		// as eventually the map element will be overwritten with the most recent update
		usersData[user.ID] = &user
	})

	filesByIds, err := s.loadChatFiles(chatInfo)

	if err != nil {
		log.Info("couldn't load chat files: %v", err)
		http.Error(w, "couldn't load chat files", http.StatusInternalServerError)
		return
	}

	messages := make([]map[string]interface{}, 0, 1000)

	loadResult := loadRelated(chatInfo.FilePath, func(t map[string]interface{}) {
		id := int64(t["ID"].(float64))

		if t["_"] == "TL_messageService" {
			action := t["Action"].(map[string]interface{})
			// TL_messageActionChatCreate -> "ChatCreate"
			message, _ := strings.CutPrefix(action["_"].(string), "TL_messageAction")
			t["__ServiceMessage"] = message
		} else {
			if files, ok := filesByIds[id]; ok {
				t["__Files"] = files
			}

			if _, ok := t["Message"]; ok {
				t["__MessageParts"] = applyEntities(t["Message"].(string), t["Entities"].([]interface{}))
			}

			t["__FromFirstName"], t["__FromLastName"] = s.getFirstLastNames(t, usersData)

			if fwdFromID, ok := t["FwdFrom"].(map[string]interface{}); ok {
				t["__FwdFromFirstName"], t["__FwdFromLastName"] = s.getFirstLastNames(fwdFromID, usersData)
			}
		}

		messages = append(messages, t)
	})

	if loadResult != nil {
		http.Error(
			w,
			fmt.Sprintf("couldn't load chat file %s: %s", chatInfo.FilePath, loadResult),
			http.StatusInternalServerError,
		)

		return
	}

	totalCount := len(messages)
	hasPrev := from > 0
	hasNext := limit > 0 && from+limit < totalCount
	prev := from - limit
	if prev < 0 || limit == 0 {
		prev = 0
	}
	next := from + limit

	if limit > 0 {
		end := from + limit
		if end > totalCount {
			end = totalCount
		}
		messages = messages[from:end]
	} else if from > 0 {
		messages = messages[from:]
	}

	s.renderTemplate(w, "chat.html", ChatPageView{
		Account:    account,
		Chat:       chatInfo,
		Messages:   messages,
		Prev:       prev,
		Next:       next,
		Limit:      limit,
		HasPrev:    hasPrev,
		HasNext:    hasNext,
		TotalCount: totalCount,
	})
}

func (s *Server) getFirstLastNames(t map[string]interface{}, usersData map[int64]*UserData) (firstName, lastName string) {
	if fromID, ok := t["FromID"].(map[string]interface{}); ok {
		if fromUserIDStr, ok := fromID["UserID"].(string); ok {
			fromUserID, err := strconv.ParseInt(fromUserIDStr, 10, 64)
			if err == nil {
				if fromUser, ok := usersData[fromUserID]; ok {
					if fromUser.FirstName != nil {
						firstName = *fromUser.FirstName
					}
					if fromUser.LastName != nil {
						lastName = *fromUser.LastName
					}
				}
			} else {
				log.Info("couldn't parse FromID['UserID'] for %s : %v", fromUserIDStr, err)
			}
		}
	}
	return
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

	// entities can include other entities, but intersection seems not allowed:
	// sending message with `"entities":[{"type":"bold","offset":0,"length":6}, {"type":"italic","offset":3,"length":9}]`
	// results in error: `Bad Request: entity beginning at UTF-16 offset 3 ends after the end of the text at UTF-16 offset 12`

	for _, entAny := range entities {
		if ent, ok := entAny.(map[string]interface{}); ok {
			entOpen := ""
			entClose := ""
			isBlock := false
			switch ent["_"] {
			case "TL_messageEntityTextUrl": //type name before v0.167.0
				fallthrough
			case "TL_messageEntityTextURL":
				var url string
				if u, ok := ent["Url"]; ok { //field name before v0.167.0
					url = u.(string)
				} else {
					url = ent["URL"].(string)
				}
				href := addDefaultScheme(url, "http")
				entOpen = `<a href="` + href + `" target="_blank">`
				entClose = `</a>`
			case "TL_messageEntityUrl": //type name before v0.167.0
				fallthrough
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

// "example.com" -> "http://example.com"
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

func (s *Server) loadChats() ([]StoredChatInfo, error) {
	chats, err := s.saver.ReadSavedChatsList()
	if err != nil {
		return nil, merry.Wrap(err)
	}

	chatInfos := make([]StoredChatInfo, 0, len(chats))

	for _, chat := range chats {
		chatInfos = append(chatInfos, StoredChatInfo{
			ID:           chat.ID,
			Name:         chat.FSTitle,
			FirstLetters: extractFirstTwoLetters(chat.FSTitle, ""),
			FilePath:     chat.FPath,
		})
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

func (s *Server) loadChatFiles(chat StoredChatInfo) (map[int64][]File, error) {
	filesById := make(map[int64][]File)

	files, err := s.saver.ReadSavedChatFilesList(chat.ID)
	if err != nil {
		return nil, merry.Wrap(err)
	}

	for _, file := range files {
		stat, err := os.Stat(file.FPath)
		if err != nil {
			return nil, merry.Wrap(err)
		}
		relPath, _ := filepath.Rel(s.saver.Dirpath, file.FPath)

		filesById[file.MessageID] = append(filesById[file.MessageID], File{
			Name:        file.FName,
			FullWebPath: "/" + relPath,
			Index:       file.IndexInMessage,
			Size:        stat.Size(),
		})
	}

	for _, files := range filesById {
		sort.Slice(files, func(i, j int) bool { return files[i].Index < files[j].Index })
	}

	return filesById, nil
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
	templates, err := templates.ParseFS(templatesFS, "preview_templates/layout.html", "preview_templates/"+tmpl)
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
	s.mux.ServeHTTP(w, r)
}

func servePreviewHttp(addr string, config *Config, saver *JSONFilesHistorySaver) error {
	server := &Server{
		config: config,
		saver:  saver,
	}

	mux := http.NewServeMux()
	server.mux = mux
	mux.Handle("/", http.RedirectHandler("/chats/", http.StatusFound))
	mux.HandleFunc("/chats/", server.chatsPageHandler)
	mux.HandleFunc("/chats/{chatID}", server.chatPageHandler)

	filesDir := http.Dir(config.OutDirPath + "/files")
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(filesDir)))

	staticFS, _ := fs.Sub(staticFS, "preview_static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	log.Info("Starting server on http://%s", addr) //"http://" makes the address openable with Ctrl+Click in some terminal emulators (like GNOME Terminal)
	if err := http.ListenAndServe(addr, server); err != nil {
		return err
	}
	return nil
}
