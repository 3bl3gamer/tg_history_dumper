package main

import (
	"fmt"
	"os"
	"testing"
)

func TestJSONRecordsReader(t *testing.T) {
	type Item struct {
		ID   int64
		Name string
	}

	fillFile := func(t *testing.T, fpath, content string) string {
		t.Helper()
		if fpath == "" {
			fpath = t.TempDir() + "/file"
		}
		f, err := os.Create(fpath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return fpath
	}

	updateOK := func(t *testing.T, reader *JSONRecordsReader[Item]) {
		t.Helper()
		if err := reader.UpdateOffsets(); err != nil {
			t.Error(err)
		}
	}

	readOK := func(t *testing.T, reader *JSONRecordsReader[Item], id int64) (Item, bool) {
		t.Helper()
		item, found, err := reader.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		return item, found
	}
	readItem := func(t *testing.T, reader *JSONRecordsReader[Item], id int64, name string) {
		t.Helper()
		item, found := readOK(t, reader, id)
		if !found {
			t.Errorf("item #%d must be found", id)
		}
		if item.ID != id || item.Name != name {
			t.Errorf("item %#v != %#v", item, Item{id, name})
		}
	}
	readNone := func(t *testing.T, reader *JSONRecordsReader[Item], id int64) {
		t.Helper()
		item, found := readOK(t, reader, id)
		if found {
			t.Errorf("item #%d must not be found", id)
		}
		if item.ID != 0 || item.Name != "" {
			t.Errorf("item %#v is not zero", item)
		}
	}

	t.Run("empty file", func(t *testing.T) {
		fpath := fillFile(t, "", "")
		reader := NewJSONRecordsReader[Item](fpath)
		readNone(t, reader, 0)
		updateOK(t, reader)
		readNone(t, reader, 0)
	})

	t.Run("file with some data", func(t *testing.T) {
		fpath := fillFile(t, "", `{"ID":1, "Name": "Test1"}`+"\n"+`{"ID":2, "Name": "Test2"}`+"\n")
		reader := NewJSONRecordsReader[Item](fpath)

		readNone(t, reader, 1)

		updateOK(t, reader)
		readItem(t, reader, 1, "Test1")
		readItem(t, reader, 2, "Test2")
		readNone(t, reader, -1)
		readNone(t, reader, 0)
		readNone(t, reader, 3)
	})

	t.Run("last line without newline", func(t *testing.T) {
		fpath := fillFile(t, "", `{"ID":1, "Name": "Test1"}`+"\n"+`{"ID":2, "Name": "Te`)
		reader := NewJSONRecordsReader[Item](fpath)
		updateOK(t, reader)
		readItem(t, reader, 1, "Test1")
		readNone(t, reader, 2)
	})

	t.Run("appending", func(t *testing.T) {
		t.Run("regualr", func(t *testing.T) {
			fpath := fillFile(t, "", "")
			reader := NewJSONRecordsReader[Item](fpath)
			updateOK(t, reader)
			readNone(t, reader, 1)
			readNone(t, reader, 2)

			fillFile(t, fpath, `{"ID":1, "Name": "Test1"}`+"\n")
			readNone(t, reader, 1)
			readNone(t, reader, 2)
			updateOK(t, reader)
			readItem(t, reader, 1, "Test1")
			readNone(t, reader, 2)

			fillFile(t, fpath, `{"ID":1, "Name": "Test1"}`+"\n"+`{"ID":2, "Name": "Test2"}`+"\n")
			readItem(t, reader, 1, "Test1")
			readNone(t, reader, 2)
			updateOK(t, reader)
			readItem(t, reader, 1, "Test1")
			readItem(t, reader, 2, "Test2")
		})

		t.Run("partial last line", func(t *testing.T) {
			fpath := fillFile(t, "", `{"ID":1, "Name": "Test1"}`+"\n"+`{"ID":2, "Name": "Te`)
			reader := NewJSONRecordsReader[Item](fpath)
			updateOK(t, reader)
			readItem(t, reader, 1, "Test1")
			readNone(t, reader, 2)

			fillFile(t, fpath, `{"ID":1, "Name": "Test1"}`+"\n"+`{"ID":2, "Name": "Test2"}`+"\n")
			updateOK(t, reader)
			readItem(t, reader, 1, "Test1")
			readItem(t, reader, 2, "Test2")
		})

		t.Run("no re-read", func(t *testing.T) {
			fpath := fillFile(t, "", `{"ID":1, "Name": "Test1"}`+"\n")
			reader := NewJSONRecordsReader[Item](fpath)
			updateOK(t, reader)
			fillFile(t, fpath, `{"ID":3, "Name": "Test3"}`+"\n"+`{"ID":2, "Name": "Test2"}`+"\n")
			updateOK(t, reader)

			readItem(t, reader, 2, "Test2")
			readNone(t, reader, 3)
			item, _ := readOK(t, reader, 1)
			if item != (Item{3, "Test3"}) {
				t.Errorf("expected updated data: #%v", item)
			}
		})
	})
}

func TestJSONMessageReader(t *testing.T) {
	fillFile := func(t *testing.T, fpath, content string) string {
		t.Helper()
		if fpath == "" {
			fpath = t.TempDir() + "/file"
		}
		f, err := os.Create(fpath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return fpath
	}

	readTexts := func(t *testing.T, reader *JSONMessageReader, offset, limit int, expected string) {
		t.Helper()
		msgs, hasMore, err := reader.Read(offset, limit)
		if err != nil {
			t.Fatal(err)
		}
		res := ""
		for _, m := range msgs {
			res += m["Text"].(string) + " "
		}
		if hasMore {
			res += "..."
		} else {
			res += "end"
		}
		if res != expected {
			t.Errorf(`expected "%s", received "%s"`, expected, res)
		}
	}

	offsets := func(t *testing.T, reader *JSONMessageReader, expected string) {
		t.Helper()
		cur := fmt.Sprint(reader.endOffsets)
		if cur != expected {
			t.Errorf(`expected "%s", received "%s"`, expected, cur)
		}
	}

	t.Run("empty file", func(t *testing.T) {
		fpath := fillFile(t, "", "")
		reader := NewJSONMessageReader(fpath)
		readTexts(t, reader, 0, 0, "end")
		readTexts(t, reader, 100, 100, "end")
		offsets(t, reader, "[]")
	})

	t.Run("file with some data", func(t *testing.T) {
		fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n"+`{"Text":"#2"}`+"\n")
		reader := NewJSONMessageReader(fpath)
		readTexts(t, reader, 0, 0, "#0 #1 #2 end")
		offsets(t, reader, "[14 28 42]")
		readTexts(t, reader, 0, 9, "#0 #1 #2 end")
		readTexts(t, reader, 0, 3, "#0 #1 #2 end")
		readTexts(t, reader, 0, 2, "#0 #1 ...")
		readTexts(t, reader, 1, 2, "#1 #2 end")
		readTexts(t, reader, 1, 1, "#1 ...")
		readTexts(t, reader, 1, 0, "#1 #2 end")
		readTexts(t, reader, 2, 0, "#2 end")
		readTexts(t, reader, 3, 0, "end")
	})

	t.Run("sequential read", func(t *testing.T) {
		fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n"+`{"Text":"#2"}`+"\n")
		reader := NewJSONMessageReader(fpath)
		readTexts(t, reader, 0, 1, "#0 ...")
		offsets(t, reader, "[14 28]")
		readTexts(t, reader, 1, 1, "#1 ...")
		offsets(t, reader, "[14 28 42]")
		readTexts(t, reader, 0, 9, "#0 #1 #2 end")
		offsets(t, reader, "[14 28 42]")
	})

	t.Run("last line without newline", func(t *testing.T) {
		fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n"+`{"Text":"#2`)
		reader := NewJSONMessageReader(fpath)
		readTexts(t, reader, 0, 0, "#0 #1 end")
		readTexts(t, reader, 0, 2, "#0 #1 end")
		readTexts(t, reader, 0, 1, "#0 ...")
		readTexts(t, reader, 1, 1, "#1 end")
	})

	t.Run("appending", func(t *testing.T) {
		t.Run("regular", func(t *testing.T) {
			fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n")
			reader := NewJSONMessageReader(fpath)
			readTexts(t, reader, 0, 0, "#0 #1 end")
			readTexts(t, reader, 0, 2, "#0 #1 end")

			fillFile(t, fpath, `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n"+`{"Text":"#2"}`+"\n")
			readTexts(t, reader, 0, 0, "#0 #1 #2 end")
			readTexts(t, reader, 0, 2, "#0 #1 ...")
		})

		t.Run("partial last line", func(t *testing.T) {
			fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1`)
			reader := NewJSONMessageReader(fpath)
			readTexts(t, reader, 0, 0, "#0 end")
			readTexts(t, reader, 0, 1, "#0 end")

			fillFile(t, fpath, `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n")
			readTexts(t, reader, 0, 0, "#0 #1 end")
			readTexts(t, reader, 0, 1, "#0 ...")
		})
	})

	t.Run("EstimateMessagesCount", func(t *testing.T) {
		fpath := fillFile(t, "", `{"Text":"#0"}`+"\n"+`{"Text":"#1"}`+"\n"+`{"Text":"#2"}`+"\n")
		reader := NewJSONMessageReader(fpath)

		estimate := func(expected int64) {
			res, err := reader.EstimateMessagesCount()
			if err != nil {
				t.Fatal(err)
			}
			if res != expected {
				t.Errorf(`expected %d, received %d`, expected, res)
			}
		}

		estimate(-1)
		readTexts(t, reader, 0, 1, "#0 ...")
		estimate(3)
	})
}
