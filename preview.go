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
	"sync"
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
	config     *Config
	saver      *JSONFilesHistorySaver
	userReader *ChatSyncReader[UserData]
	chatReader *ChatSyncReader[ChatData]
	mux        *http.ServeMux
}

type ChatPageView struct {
	ChatID     int64
	ChatTitle  string
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
	type ChatWithTitle struct {
		SavedChatEntry
		Title string
	}

	chatEntries, err := s.saver.ReadSavedChatsList()
	if err != nil {
		log.Info("couldn't load chats: %v", err)
		http.Error(w, "couldn't load chats", http.StatusInternalServerError)
		return
	}

	if err := s.userReader.UpdateOffsets(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.chatReader.UpdateOffsets(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chats := make([]ChatWithTitle, len(chatEntries))
	for i, chatEntry := range chatEntries {
		chats[i].SavedChatEntry = chatEntry
		chats[i].Title, err = s.readChatTitle(s.userReader, s.chatReader, chatEntry.ID, chatEntry.FSTitle)
		if err != nil {
			log.Warn("chat #%d reading error: %s", chatEntry.ID, err)
		}
	}

	s.renderTemplate(w, "chats.html", chats)
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

	chatEntries, err := s.saver.ReadSavedChatsList()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var chatEntry SavedChatEntry
	for _, chat := range chatEntries {
		if chat.ID == chatID {
			chatEntry = chat
			break
		}
	}
	if chatEntry.ID == 0 {
		log.Info("couldn't load chat: %v", err)
		http.Error(w, "couldn't load chat", http.StatusInternalServerError)
		return
	}

	if err := s.userReader.UpdateOffsets(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.chatReader.UpdateOffsets(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	userReader := &ChatCachedReader[UserData]{reader: s.userReader}
	chatReader := &ChatCachedReader[ChatData]{reader: s.chatReader}

	userData, err := userReader.ReadOpt(chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chatData, err := chatReader.ReadOpt(chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chatTitle, err := s.readChatTitle(userReader, chatReader, chatID, chatEntry.FSTitle)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filesByIds, err := s.loadChatFiles(chatID)

	if err != nil {
		log.Info("couldn't load chat files: %v", err)
		http.Error(w, "couldn't load chat files", http.StatusInternalServerError)
		return
	}

	messages := make([]map[string]interface{}, 0, 1000)

	loadResult := loadRelated(chatEntry.FPath, func(t map[string]interface{}) {
		id := int64(t["ID"].(float64))

		if t["_"] == "TL_messageService" {
			action := t["Action"].(map[string]interface{})
			// TL_messageActionChatCreate -> "ChatCreate"
			t["__ServiceMessage"] = strings.TrimPrefix(action["_"].(string), "TL_messageAction")
		} else {
			if files, ok := filesByIds[id]; ok {
				t["__Files"] = files
			}

			if _, ok := t["Message"]; ok {
				t["__MessageParts"] = applyEntities(t["Message"].(string), t["Entities"].([]interface{}))
			}

			if userData != nil {
				// dialog (user <-> user)
				if t["Out"].(bool) {
					// this is ours message in a dialog, our ID will be in FromID.UserID
					t["__FromFirstName"], t["__FromLastName"], err = s.getFirstLastNames(t, userReader, chatReader)
				} else {
					// this is other's message in a dialog, there should be a UserData record
					t["__FromFirstName"], t["__FromLastName"] = derefOr(userData.FirstName, ""), derefOr(userData.LastName, "")
				}
			} else if chatData != nil && chatData.IsChannel {
				// channel
				t["__FromFirstName"], t["__FromLastName"] = chatData.Title, ""
			} else if chatData != nil && !chatData.IsChannel {
				// group chat
				t["__FromFirstName"], t["__FromLastName"], err = s.getFirstLastNames(t, userReader, chatReader)
			}

			if err != nil {
				log.Error(err, "")
			}

			// something is wrong, data is inconsistent, trying to display at least something
			if t["__FromFirstName"] == "" && t["__FromLastName"] == "" {
				t["__FromFirstName"] = chatEntry.FSTitle
			}

			if fwdFromID, ok := t["FwdFrom"].(map[string]interface{}); ok {
				t["__FwdFromFirstName"], t["__FwdFromLastName"], err = s.getFirstLastNames(fwdFromID, userReader, chatReader)
				if err != nil {
					log.Error(err, "")
				}
			}
		}

		messages = append(messages, t)
	})

	if loadResult != nil {
		http.Error(
			w,
			fmt.Sprintf("couldn't load chat file %s: %s", chatEntry.FPath, loadResult),
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
		ChatID:     chatID,
		ChatTitle:  chatTitle,
		Messages:   messages,
		Prev:       prev,
		Next:       next,
		Limit:      limit,
		HasPrev:    hasPrev,
		HasNext:    hasNext,
		TotalCount: totalCount,
	})
}

func (s *Server) getFirstLastNames(
	t map[string]interface{},
	userReader *ChatCachedReader[UserData],
	chatReader *ChatCachedReader[ChatData],
) (firstName, lastName string, err error) {
	if fromID, ok := t["FromID"].(map[string]interface{}); ok {
		if fromUserIDStr, ok := fromID["UserID"].(string); ok {
			fromUserID, err := strconv.ParseInt(fromUserIDStr, 10, 64)
			if err != nil {
				return "", "", merry.Prependf(err, "couldn't parse FromID['UserID'] for %s", fromUserIDStr)
			}
			fromUser, err := userReader.ReadOpt(fromUserID)
			if err != nil {
				return "", "", merry.Wrap(err)
			}
			if fromUser != nil {
				return derefOr(fromUser.FirstName, ""), derefOr(fromUser.LastName, ""), nil
			}
		} else if fromChannelIDStr, ok := fromID["ChannelID"].(string); ok {
			fromChannelID, err := strconv.ParseInt(fromChannelIDStr, 10, 64)
			if err != nil {
				return "", "", merry.Prependf(err, "couldn't parse FromID['ChannelID'] for %s", fromUserIDStr)
			}
			fromChannel, err := chatReader.ReadOpt(fromChannelID)
			if err != nil {
				return "", "", merry.Wrap(err)
			}
			if fromChannel != nil {
				return fromChannel.Title, "", nil
			}
		}
	}
	return "", "", nil
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

// fpathSeparatorsToURL converts separators in *relative* filepath to "/" (URL path separators).
// Usefull on Windows where filepaths use backslashes and
// can not be used in template URLs directly ("path\to\file" will become "path%5cto%5cfile")
func fpathSeparatorsToURL(fpath string) string {
	if filepath.Separator == '/' {
		return fpath
	} else {
		return strings.ReplaceAll(fpath, string(filepath.Separator), "/")
	}
}

func derefOr[T any](val *T, defaultVal T) T {
	if val == nil {
		return defaultVal
	}
	return *val
}

type ChatReadter[T any] interface {
	Read(id int64) (T, bool, error)
}

func (s *Server) readChatTitle(
	userReader ChatReadter[UserData],
	chatReader ChatReadter[ChatData],
	chatID int64, fallback string,
) (string, error) {
	userData, found, err := userReader.Read(chatID)
	if err != nil {
		return fallback, merry.Wrap(err)
	}
	if found {
		return strings.TrimSpace(derefOr(userData.FirstName, "") + " " + derefOr(userData.LastName, "")), nil
	}

	chatData, found, err := chatReader.Read(chatID)
	if err != nil {
		return fallback, merry.Wrap(err)
	}
	if found {
		return chatData.Title, nil
	}

	return fallback, nil
}

func (s *Server) loadChatFiles(chatID int64) (map[int64][]File, error) {
	filesById := make(map[int64][]File)

	files, err := s.saver.ReadSavedChatFilesList(chatID)
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
			FullWebPath: "/" + fpathSeparatorsToURL(relPath),
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

// ChatSyncReader is a thread-safe wrapper around JSONRecordsReader
// so it can be used with (theoretically) concurrent HTTP requests.
type ChatSyncReader[T UserData | ChatData] struct {
	reader *JSONRecordsReader[T]
	mutex  sync.RWMutex
}

func NewChatSyncReader[T UserData | ChatData](fpath string) *ChatSyncReader[T] {
	return &ChatSyncReader[T]{
		reader: NewJSONRecordsReader[T](fpath),
	}
}

func (r *ChatSyncReader[T]) Read(id int64) (T, bool, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.reader.Read(id)
}

func (r *ChatSyncReader[T]) UpdateOffsets() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.reader.UpdateOffsets()
}

// ChatCachedReader is a wrapper around JSONRecordsReader with read items cache.
//
// It IS NOT thread-safe, though multiple chached readers may share same [ChatSyncReader].
//
// It is expected to be created once for each HTTP request, so multiple reads of same
// UserID (for example) will be cached for single request. And so subsequent request
// will run with an empty cache and may read updated user data.
type ChatCachedReader[T UserData | ChatData] struct {
	reader *ChatSyncReader[T]
	cache  map[int64]T
}

func (r *ChatCachedReader[T]) Read(id int64) (T, bool, error) {
	if r.cache == nil {
		r.cache = make(map[int64]T)
	}

	item, ok := r.cache[id]
	if ok {
		return item, true, nil
	}

	item, ok, err := r.reader.Read(id)
	if err != nil {
		return item, false, merry.Wrap(err)
	}
	if ok {
		r.cache[id] = item
		return item, true, nil
	}
	return item, false, nil
}

func (r *ChatCachedReader[T]) ReadOpt(id int64) (*T, error) {
	item, found, err := r.Read(id)
	if err != nil {
		return nil, merry.Wrap(err)
	}
	if !found {
		return nil, nil
	}
	return &item, nil
}

func servePreviewHttp(addr string, config *Config, saver *JSONFilesHistorySaver) error {
	server := &Server{
		config:     config,
		saver:      saver,
		userReader: NewChatSyncReader[UserData](saver.usersFPath()),
		chatReader: NewChatSyncReader[ChatData](saver.chatsFPath()),
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
