package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
)

type UserData struct {
	ID                    int32
	Username              string
	FirstName, LastName   string
	PhoneNumber, LangCode string
	IsBot                 bool
	UpdatedAt             time.Time
}

func (u *UserData) Equals(other *mtproto.TL_user) bool {
	return other != nil &&
		u.Username == other.Username && u.PhoneNumber == other.Phone &&
		u.FirstName == other.FirstName && u.LastName == other.LastName &&
		u.LangCode == other.LangCode
}

type SaveFileCallbackFunc func(*TGFileInfo, string) error

type HistorySaver interface {
	GetLastMessageID(*Dialog) (int32, error)
	SaveSenders([]mtproto.TL) error
	SaveMessages(*Dialog, []mtproto.TL) error
	SetFileRequestCallback(SaveFileCallbackFunc)
}

type JSONFilesHistorySaver struct {
	Dirpath         string
	usersData       map[int32]*UserData
	requestFileFunc SaveFileCallbackFunc
}

func (s JSONFilesHistorySaver) dialogFSName(dialog *Dialog) string {
	title := strings.Replace(dialog.Title, "/", "_", -1)
	title = strings.Replace(title, ":", "_", -1) //TODO: is it enough?
	return strconv.FormatInt(int64(dialog.ID), 10) + "__" + title
}

func (s JSONFilesHistorySaver) dialogFPath(dialog *Dialog) string {
	return s.Dirpath + "/" + s.dialogFSName(dialog)
}

func (s JSONFilesHistorySaver) usersFPath() string {
	return s.Dirpath + "/users"
}

func (s JSONFilesHistorySaver) filePath(dialog *Dialog, msgID int32, fname string) string {
	fpath := s.Dirpath + "/files/" + s.dialogFSName(dialog) + "/" + strconv.Itoa(int(msgID)) + "_Media"
	if fname != "" {
		fpath += "_" + fname
	}
	return fpath
}

func (s JSONFilesHistorySaver) GetLastMessageID(dialog *Dialog) (int32, error) {
	file, err := os.Open(s.dialogFPath(dialog))
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

func (s JSONFilesHistorySaver) loadUsers() error {
	file, err := os.Open(s.usersFPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	for {
		user := &UserData{}
		err := decoder.Decode(user)
		if err == io.EOF {
			break
		}
		if err != nil {
			return merry.Wrap(err)
		}
		s.usersData[user.ID] = user
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveSenders(users []mtproto.TL) error {
	if s.usersData == nil {
		s.usersData = make(map[int32]*UserData)
		if err := s.loadUsers(); err != nil {
			return merry.Wrap(err)
		}
	}

	for _, userTL := range users {
		tgUser, ok := userTL.(mtproto.TL_user)
		if !ok {
			return merry.Errorf(mtproto.UnexpectedTL("user", userTL))
		}

		user, ok := s.usersData[tgUser.ID]
		if !ok || !user.Equals(&tgUser) {
			newUser := &UserData{
				ID:          tgUser.ID,
				FirstName:   tgUser.FirstName,
				LastName:    tgUser.LastName,
				Username:    tgUser.Username,
				PhoneNumber: tgUser.Phone,
				LangCode:    tgUser.LangCode,
				IsBot:       tgUser.Bot,
				UpdatedAt:   time.Now(),
			}

			if err := os.MkdirAll(s.Dirpath, 0700); err != nil {
				return merry.Wrap(err)
			}
			file, err := os.OpenFile(s.usersFPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return merry.Wrap(err)
			}
			defer file.Close()
			if err := json.NewEncoder(file).Encode(newUser); err != nil {
				return merry.Wrap(err)
			}

			s.usersData[tgUser.ID] = newUser
		}
	}
	return nil
}

func (s JSONFilesHistorySaver) SaveMessages(dialog *Dialog, messages []mtproto.TL) error {
	if err := os.MkdirAll(s.Dirpath, 0700); err != nil {
		return merry.Wrap(err)
	}

	file, err := os.OpenFile(s.dialogFPath(dialog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return merry.Wrap(err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		msgMap := tgObjToMap(msg)
		if s.requestFileFunc != nil {
			fileInfo := tgGetMessageMediaFileInfo(msg)
			if fileInfo != nil {
				fpath := s.filePath(dialog, msgMap["ID"].(int32), fileInfo.FName)
				if err := s.requestFileFunc(fileInfo, fpath); err != nil {
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

func (s *JSONFilesHistorySaver) SetFileRequestCallback(callback SaveFileCallbackFunc) {
	s.requestFileFunc = callback
}
