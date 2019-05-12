package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
)

type HistorySaver interface {
	GetLastMessageID(*Dialog) (int32, error)
	SaveMessages(*Dialog, []mtproto.TL) error
}

type JSONFilesHistorySaver struct {
	Dirpath string
}

func (s JSONFilesHistorySaver) dialogFPath(dialog *Dialog) string {
	title := strings.Replace(dialog.Title, "/", "_", 0)
	title = strings.Replace(title, ":", "_", 0) //TODO: is it enough?
	return s.Dirpath + "/" + title + " #" + strconv.FormatInt(int64(dialog.ID), 10)
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
		if err := encoder.Encode(tgObjToMap(msg)); err != nil {
			return merry.Wrap(err)
		}
	}

	if err := file.Close(); err != nil {
		return merry.Wrap(err)
	}
	return nil
}
