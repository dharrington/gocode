package main

import (
	"flag"
	"fmt"
	"go/build"
	"io/ioutil"
	"log"
	"net/rpc"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mdempsky/gocode/gbimporter"
	"github.com/mdempsky/gocode/suggest"
)

func clientConnect() *rpc.Client {
	addr := *g_addr
	if *g_sock == "unix" {
		addr = getSocketPath()
	}

	// client
	client, err := rpc.Dial(*g_sock, addr)
	if err != nil {
		if *g_sock == "unix" && fileExists(addr) {
			os.Remove(addr)
		}

		err = tryStartServer()
		if err != nil {
			log.Fatal(err)
		}
		client, err = tryToConnect(*g_sock, addr)
		if err != nil {
			log.Fatal(err)
		}
		return client
	}
	return client
}
func doClient() {
	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "autocomplete":
			cmdAutoComplete()
		case "reporterrors":
			cmdReportErrors()
		case "lookup":
			cmdLookup()
		case "close", "exit":
			c := clientConnect()
			defer c.Close()
			cmdExit(c)
		default:
			fmt.Printf("gocode: unknown subcommand: %q\nRun 'gocode -help' for usage.\n", flag.Arg(0))
		}
	}
}

func tryStartServer() error {
	path := get_executable_filename()
	args := []string{os.Args[0], "-s", "-sock", *g_sock, "-addr", *g_addr}
	cwd, _ := os.Getwd()

	var err error
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}

	procattr := os.ProcAttr{Dir: cwd, Env: os.Environ(), Files: []*os.File{stdin, stdout, stderr}}
	p, err := os.StartProcess(path, args, &procattr)
	if err != nil {
		return err
	}

	return p.Release()
}

func tryToConnect(network, address string) (*rpc.Client, error) {
	start := time.Now()
	for {
		client, err := rpc.Dial(network, address)
		if err != nil && time.Since(start) < time.Second {
			continue
		}
		return client, err
	}
}

func cmdAutoComplete() {
	var req AutoCompleteRequest
	req.Filename, req.Data, req.Cursor = prepareFilenameDataCursor()
	req.Filter = *g_filtersuggestions
	req.Context = gbimporter.PackContext(&build.Default)

	var res AutoCompleteReply
	var err error
	if *g_oneshot {
		err = AutoComplete(&req, &res)
	} else {
		c := clientConnect()
		defer c.Close()
		err = c.Call("Server.AutoComplete", &req, &res)
	}
	if err != nil {
		panic(err)
	}

	fmt := suggest.Formatters[*g_format]
	if fmt == nil {
		fmt = suggest.NiceFormat
	}
	fmt(os.Stdout, res.Candidates, res.Len)
}

func cmdReportErrors() {
	var req ReportErrorsRequest
	req.Filename, req.Data = prepareFilenameData()
	req.Context = gbimporter.PackContext(&build.Default)

	var res ReportErrorsReply
	var err error
	if *g_oneshot {
		err = ReportErrors(&req, &res)
	} else {
		c := clientConnect()
		defer c.Close()
		err = c.Call("Server.ReportErrors", &req, &res)
	}
	if err != nil {
		panic(err)
	}
	for _, e := range res.Errors {
		fmt.Printf("Error: %d %d %s\n", e.Line, e.Col, e.Msg)
	}
}

func cmdLookup() {
	var req LookupRequest
	req.Filename, req.Data, req.Cursor = prepareFilenameDataCursor()
	req.Context = gbimporter.PackContext(&build.Default)

	var res LookupReply
	var err error
	if *g_oneshot {
		err = Lookup(&req, &res)
	} else {
		c := clientConnect()
		defer c.Close()
		err = c.Call("Server.Lookup", &req, &res)
	}
	if err != nil {
		panic(err)
	}
	// Print out information about identifier at the cursor and the call if
	// the cursor is within call parenthesis. One or both may be invalid.
	// Ex:
	// ident: /path/to/file.go:99:1
	//  name: CoolFunc
	//  type: func(int, string) string
	//  callarg: 1
	//  doc: // Comment about this identifier.
	//  // ...
	// call: /path/to/file.go:10:1
	//  name: NeatFunc
	//  type: func(int,int)
	print := func(kind string, li LookupInfo) {
		if li.Path == "" {
			return
		}
		doc := strings.Replace(li.Doc, "\n", "<BR>", -1)
		fmt.Printf("%s:\n pos: %s:%v:%v\n name: %s\n type: %s\n", kind, li.Path, li.Line, li.Column, li.Name, li.Type)
		if li.CallArg != -1 {
			fmt.Printf(" callarg: %d\n", li.CallArg)
		}
		fmt.Printf(" doc: %s\n", doc)
	}
	print("ident", res.Cursor)
	print("call", res.Call)
}

func cmdExit(c *rpc.Client) {
	var req ExitRequest
	var res ExitReply
	if err := c.Call("Server.Exit", &req, &res); err != nil {
		panic(err)
	}
}

func prepareFilenameDataCursor() (string, []byte, int) {
	var file []byte
	var err error

	if *g_input != "" {
		file, err = ioutil.ReadFile(*g_input)
	} else {
		file, err = ioutil.ReadAll(os.Stdin)
	}

	if err != nil {
		panic(err.Error())
	}

	filename := *g_input
	offset := ""
	switch flag.NArg() {
	case 2:
		offset = flag.Arg(1)
	case 3:
		filename = flag.Arg(1) // Override default filename
		offset = flag.Arg(2)
	}

	if filename != "" {
		filename, _ = filepath.Abs(filename)
	}

	cursor := -1
	if offset != "" {
		if offset[0] == 'c' || offset[0] == 'C' {
			cursor, _ = strconv.Atoi(offset[1:])
			cursor = runeToByteOffset(file, cursor)
		} else {
			cursor, _ = strconv.Atoi(offset)
		}
	}

	return filename, file, cursor
}

func prepareFilenameData() (string, []byte) {
	var file []byte
	var err error

	if *g_input != "" {
		file, err = ioutil.ReadFile(*g_input)
	} else {
		file, err = ioutil.ReadAll(os.Stdin)
	}

	if err != nil {
		panic(err.Error())
	}

	filename := *g_input
	switch flag.NArg() {
	case 2:
		filename = flag.Arg(1) // Override default filename
	}

	if filename != "" {
		filename, _ = filepath.Abs(filename)
	}

	return filename, file
}
