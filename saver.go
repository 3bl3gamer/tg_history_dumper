package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry/v2"
)

type MediaFileSource byte

const (
	MessageMediaFile MediaFileSource = iota
	StoryMediaFile
)

type UserData struct {
	ID          int64
	Username    *string
	FirstName   *string
	LastName    *string
	PhoneNumber *string
	IsBot       bool
	IsFake      bool
	IsScam      bool
	IsVerified  bool
	IsPremium   bool
	IsDeleted   bool
	UpdatedAt   time.Time
}

func NewUserDataFromTG(tgUser mtproto.TL_user) *UserData {
	return &UserData{
		ID:          tgUser.ID,
		FirstName:   tgUser.FirstName,
		LastName:    tgUser.LastName,
		Username:    tgUser.Username,
		PhoneNumber: tgUser.Phone,
		IsBot:       tgUser.Bot,
		IsFake:      tgUser.Fake,
		IsScam:      tgUser.Scam,
		IsVerified:  tgUser.Verified,
		IsPremium:   tgUser.Premium,
		IsDeleted:   tgUser.Deleted,
		UpdatedAt:   time.Now(),
	}
}

func (u *UserData) IsUpdatedBy(other *mtproto.TL_user) bool {
	if other.Min {
		return false //min constructor does not update old data (https://core.telegram.org/api/min)
	}
	return !equalsOpt(u.FirstName, other.FirstName) ||
		!equalsOpt(u.LastName, other.LastName) ||
		!equalsOpt(u.Username, other.Username) ||
		!equalsOpt(u.PhoneNumber, other.Phone) ||
		u.IsBot != other.Bot ||
		u.IsFake != other.Fake ||
		u.IsScam != other.Scam ||
		u.IsVerified != other.Verified ||
		u.IsPremium != other.Premium ||
		u.IsDeleted != other.Deleted
}

type ChatData struct {
	ID        int64
	Username  *string
	Title     string
	IsChannel bool
	UpdatedAt time.Time
}

func (c *ChatData) IsUpdatedBy(other *ChatData, otherIsMin bool) bool {
	if otherIsMin {
		return false
	}
	return !equalsOpt(c.Username, other.Username) ||
		c.IsChannel != other.IsChannel ||
		c.Title != other.Title
}

type SaveFileCallbackFunc func(*Chat, *TGFileInfo, int32, MediaFileSource) error

func equalsOpt[T comparable](old, new *T) bool {
	return new == old || (old != nil && new != nil && *new == *old)
}

func fnameIDPrefix(id int64) string {
	return strconv.FormatInt(id, 10) + "_"
}

func escapeNameForFS(name string) string {
	chars := `/:` //TODO: is it enough?
	if runtime.GOOS == "windows" {
		chars += `\<>:"|*?`
	}
	for _, c := range chars {
		name = strings.Replace(name, string(c), "_", -1)
	}
	return name
}

func findFPathForID(dirpath string, id int64, defaultName string) (string, error) {
	fnamePrefix := fnameIDPrefix(id)
	correctFPath := dirpath + "/" + fnamePrefix + escapeNameForFS(defaultName)

	entries, err := os.ReadDir(dirpath)
	if os.IsNotExist(err) {
		return correctFPath, nil
	}
	if err != nil {
		return "", merry.Wrap(err)
	}
	var matchedFNames []string
	for _, entry := range entries {
		fname := entry.Name()
		if strings.HasPrefix(fname, fnamePrefix) {
			matchedFNames = append(matchedFNames, fname)
		}
	}

	if len(matchedFNames) == 0 {
		return correctFPath, nil
	}

	curFPath := dirpath + "/" + matchedFNames[0]
	if len(matchedFNames) > 1 {
		return "", merry.Errorf(
			"found multiple files with prefix %s in %s/, there must be only one: %s",
			fnamePrefix, dirpath, strings.Join(matchedFNames, ", "))
	}

	if curFPath != correctFPath {
		log.Info("renaming %s -> %s", curFPath, correctFPath)
		if err := os.Rename(curFPath, correctFPath); err != nil {
			return "", merry.Wrap(err)
		}
	}
	return correctFPath, nil
}

type HistorySaver interface {
	GetLastMessageID(*Chat) (int32, error)
	GetLastStoryID(*Chat) (int32, error)
	SaveRelatedUsers([]mtproto.TL) error
	SaveRelatedChats([]mtproto.TL) error
	SaveMessages(*Chat, []mtproto.TL) error
	SaveStories(*Chat, []mtproto.TL) error
	SetFileRequestCallback(SaveFileCallbackFunc)
	SaveAccount(mtproto.TL_user) error
	SaveContacts([]mtproto.TL) error
	SaveAuths([]mtproto.TL_authorization) error
}

type JSONFilesHistorySaver struct {
	Dirpath         string
	usersData       map[int64]*UserData
	chatsData       map[int64]*ChatData
	requestFileFunc SaveFileCallbackFunc
}

func (s JSONFilesHistorySaver) chatMessagesFPath(chat *Chat) (string, error) {
	return findFPathForID(s.Dirpath, int64(chat.ID), chat.Title)
}

func (s JSONFilesHistorySaver) usersFPath() string {
	return s.Dirpath + "/users"
}

func (s JSONFilesHistorySaver) chatsFPath() string {
	return s.Dirpath + "/chats"
}

func (s JSONFilesHistorySaver) contactsFPath() string {
	return s.Dirpath + "/contacts"
}

func (s JSONFilesHistorySaver) authsFPath() string {
	return s.Dirpath + "/auths"
}

func (s JSONFilesHistorySaver) accountFPath() string {
	return s.Dirpath + "/account"
}

func (s JSONFilesHistorySaver) chatStoriesFPath(chat *Chat) (string, error) {
	return findFPathForID(s.Dirpath+"/stories", int64(chat.ID), chat.Title)
}

func (s JSONFilesHistorySaver) MessageFileFPath(chat *Chat, msgID int32, fname string, mediaSource MediaFileSource) (string, error) {
	baseDirPath := s.Dirpath + "/files"
	if mediaSource == StoryMediaFile {
		baseDirPath += "/stories"
	}
	dirPath, err := findFPathForID(baseDirPath, int64(chat.ID), chat.Title)
	if err != nil {
		return "", merry.Wrap(err)
	}
	suffix := "Media"
	if fname != "" {
		suffix += "_" + fname
	}
	return dirPath + "/" + fnameIDPrefix(int64(msgID)) + escapeNameForFS(suffix), nil
}

func (s JSONFilesHistorySaver) makeDir(dirpath string) error {
	return merry.Wrap(os.MkdirAll(dirpath, 0700))
}

func (s JSONFilesHistorySaver) openForAppend(fpath string) (*os.File, error) {
	if err := s.makeDir(filepath.Dir(fpath)); err != nil {
		return nil, merry.Wrap(err)
	}
	file, err := os.OpenFile(fpath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, merry.Wrap(err)
	}
	return file, nil
}

func (s JSONFilesHistorySaver) openAndTruncate(fpath string) (*os.File, error) {
	if err := s.makeDir(filepath.Dir(fpath)); err != nil {
		return nil, merry.Wrap(err)
	}
	file, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, merry.Wrap(err)
	}
	return file, nil
}

func (s JSONFilesHistorySaver) getLastLineID(fpath string) (int32, error) {
	file, err := os.Open(fpath)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, merry.Wrap(err)
	}
	defer file.Close()

	endPos, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, merry.Wrap(err)
	}
	if endPos < 2 {
		return 0, nil
	}
	curPos := endPos - 2
	buf := []byte{0}
	for ; curPos > 0; curPos-- {
		_, err := file.ReadAt(buf, curPos)
		if err != nil {
			return 0, merry.Wrap(err)
		}
		if buf[0] == '\n' {
			break
		}
	}
	buf = make([]byte, endPos-curPos)
	_, err = file.ReadAt(buf, curPos)
	if err != nil {
		return 0, merry.Wrap(err)
	}

	msg := make(map[string]interface{})
	if err := json.Unmarshal(buf, &msg); err != nil {
		return 0, merry.Wrap(err)
	}
	idInterf, ok := msg["ID"]
	if !ok {
		return 0, merry.Errorf("malformed json: 'ID' attr is missing: %s", string(buf))
	}
	id, ok := idInterf.(float64)
	if !ok {
		return 0, merry.Errorf("malformed ID: %#v", idInterf)
	}
	return int32(id), nil
}

func (s JSONFilesHistorySaver) GetLastMessageID(chat *Chat) (int32, error) {
	chatFPath, err := s.chatMessagesFPath(chat)
	if err != nil {
		return 0, merry.Wrap(err)
	}
	return s.getLastLineID(chatFPath)
}

func (s JSONFilesHistorySaver) GetLastStoryID(chat *Chat) (int32, error) {
	chatFPath, err := s.chatStoriesFPath(chat)
	if err != nil {
		return 0, merry.Wrap(err)
	}
	return s.getLastLineID(chatFPath)
}

func loadRelated[T any](fpath string, f func(T)) error {
	file, err := os.Open(fpath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	for {
		var obj T
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return merry.Wrap(err)
		}
		f(obj)
	}
	return nil
}

func (s JSONFilesHistorySaver) loadUsers() error {
	return loadRelated(s.usersFPath(), func(user UserData) {
		s.usersData[user.ID] = &user
	})
}

func (s JSONFilesHistorySaver) loadChats() error {
	return loadRelated(s.chatsFPath(), func(chat ChatData) {
		s.chatsData[chat.ID] = &chat
	})
}

func (s JSONFilesHistorySaver) SaveRelatedUsers(users []mtproto.TL) error {
	if s.usersData == nil {
		s.usersData = make(map[int64]*UserData)
		if err := s.loadUsers(); err != nil {
			return merry.Wrap(err)
		}
	}

	var encoder *json.Encoder
	for _, userTL := range users {
		tgUser, ok := userTL.(mtproto.TL_user)
		if !ok {
			return merry.Errorf(mtproto.UnexpectedTL("user", userTL))
		}

		user, exists := s.usersData[tgUser.ID]
		if !exists || user.IsUpdatedBy(&tgUser) {
			newUser := NewUserDataFromTG(tgUser)

			if encoder == nil {
				file, err := s.openForAppend(s.usersFPath())
				if err != nil {
					return merry.Wrap(err)
				}
				defer file.Close()
				encoder = json.NewEncoder(file)
			}
			if err := encoder.Encode(newUser); err != nil {
				return merry.Wrap(err)
			}

			s.usersData[tgUser.ID] = newUser
		}
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveRelatedChats(chats []mtproto.TL) error {
	if s.chatsData == nil {
		s.chatsData = make(map[int64]*ChatData)
		if err := s.loadChats(); err != nil {
			return merry.Wrap(err)
		}
	}

	var encoder *json.Encoder
	for _, chatTL := range chats {
		var newChat *ChatData
		chatIsMin := false
		switch c := chatTL.(type) {
		case mtproto.TL_chat:
			newChat = &ChatData{ID: c.ID, Title: c.Title}
		case mtproto.TL_chatForbidden:
			newChat = &ChatData{ID: c.ID, Title: c.Title}
		case mtproto.TL_channel:
			chatIsMin = c.Min
			newChat = &ChatData{ID: c.ID, Title: c.Title, Username: c.Username, IsChannel: !c.Megagroup}
		case mtproto.TL_channelForbidden:
			newChat = &ChatData{ID: c.ID, Title: c.Title, IsChannel: !c.Megagroup}
		default:
			return merry.Wrap(mtproto.WrongRespError(chatTL))
		}

		chat, exists := s.chatsData[newChat.ID]
		if !exists || chat.IsUpdatedBy(newChat, chatIsMin) {
			newChat.UpdatedAt = time.Now()

			if encoder == nil {
				file, err := s.openForAppend(s.chatsFPath())
				if err != nil {
					return merry.Wrap(err)
				}
				defer file.Close()
				encoder = json.NewEncoder(file)
			}
			if err := encoder.Encode(newChat); err != nil {
				return merry.Wrap(err)
			}

			s.chatsData[newChat.ID] = newChat
		}
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveContacts(contacts []mtproto.TL) error {
	file, err := s.openAndTruncate(s.contactsFPath())
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)

	var contactsMap []interface{}
	for i := 0; i < len(contacts); i++ {
		contactMap := tgObjToMap(contacts[i])
		contactMap["_TL_LAYER"] = mtproto.TL_Layer
		contactsMap = append(contactsMap, contactMap)
	}

	if err := encoder.Encode(contactsMap); err != nil {
		return merry.Wrap(err)
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveAuths(auths []mtproto.TL_authorization) error {
	file, err := s.openAndTruncate(s.authsFPath())
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)

	var authsMap []interface{}
	for i := 0; i < len(auths); i++ {
		authMap := tgObjToMap(auths[i])
		authMap["_TL_LAYER"] = mtproto.TL_Layer
		authsMap = append(authsMap, authMap)
	}

	if err := encoder.Encode(authsMap); err != nil {
		return merry.Wrap(err)
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveAccount(me mtproto.TL_user) error {
	file, err := s.openAndTruncate(s.accountFPath())
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)

	accMap := tgObjToMap(me)
	accMap["_TL_LAYER"] = mtproto.TL_Layer

	if err := encoder.Encode(accMap); err != nil {
		return merry.Wrap(err)
	}
	return nil
}

func (s JSONFilesHistorySaver) appendRecordsWithRelatedMedia(
	fpath string, messages []mtproto.TL,
	chat *Chat, mediaSource MediaFileSource, fileInfoFunc func(item mtproto.TL) (*TGFileInfo, error),
) error {
	file, err := s.openForAppend(fpath)
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		msgMap := tgObjToMap(msg)
		msgMap["_TL_LAYER"] = mtproto.TL_Layer
		if s.requestFileFunc != nil {
			fileInfo, err := fileInfoFunc(msg)
			if err != nil {
				return merry.Wrap(err)
			}
			if fileInfo != nil {
				if err := s.requestFileFunc(chat, fileInfo, msgMap["ID"].(int32), mediaSource); err != nil {
					return merry.Wrap(err)
				}
			}
		}
		if err := encoder.Encode(msgMap); err != nil {
			return merry.Wrap(err)
		}
	}

	if err := file.Close(); err != nil {
		return merry.Wrap(err)
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveMessages(chat *Chat, messages []mtproto.TL) error {
	messagesFPath, err := s.chatMessagesFPath(chat)
	if err != nil {
		return merry.Wrap(err)
	}
	err = s.appendRecordsWithRelatedMedia(messagesFPath, messages, chat, MessageMediaFile, tgFindMessageMediaFileInfo)
	return merry.Wrap(err)
}

func (s JSONFilesHistorySaver) SaveStories(chat *Chat, stories []mtproto.TL) error {
	storiesFPath, err := s.chatStoriesFPath(chat)
	if err != nil {
		return merry.Wrap(err)
	}
	err = s.appendRecordsWithRelatedMedia(storiesFPath, stories, chat, StoryMediaFile, tgFindStoryMediaFileInfo)
	return merry.Wrap(err)
}

func (s *JSONFilesHistorySaver) SetFileRequestCallback(callback SaveFileCallbackFunc) {
	s.requestFileFunc = callback
}
