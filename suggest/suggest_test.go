package suggest_test

import (
	"bytes"
	"flag"
	"go/importer"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/mdempsky/gocode/suggest"
)

func TestRegress(t *testing.T) {
	s := suggest.New(testing.Verbose())

	testDirs := flag.Args()
	if len(testDirs) == 0 {
		var err error
		testDirs, err = filepath.Glob("testdata/test.*")
		if err != nil {
			t.Fatal(err)
		}
	}

	failed := 0
	for _, testDir := range testDirs {
		if !testRegress(t, s, testDir) {
			failed++
		}
	}
	if failed != 0 {
		t.Errorf("%d failed / %d total", failed, len(testDirs))
	}
}

func testRegress(t *testing.T, s *suggest.Suggester, testDir string) bool {
	testDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Errorf("Abs failed: %v", err)
		return false
	}

	filename := filepath.Join(testDir, "test.go.in")
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Errorf("ReadFile failed: %v", err)
		return false
	}

	cursor := bytes.IndexByte(data, '@')
	if cursor < 0 {
		t.Errorf("Missing @")
		return false
	}
	data = append(data[:cursor], data[cursor+1:]...)

	candidates, prefixLen := s.Suggest(importer.Default(), filename, data, cursor, true)

	var out bytes.Buffer
	suggest.NiceFormat(&out, candidates, prefixLen)

	want, err := ioutil.ReadFile(filepath.Join(testDir, "out.expected"))
	if got := out.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("%s:\nGot:\n%s\nWant:\n%s\n", testDir, got, want)
		return false
	}

	return true
}
