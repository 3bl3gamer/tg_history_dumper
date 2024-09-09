package main

import (
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
