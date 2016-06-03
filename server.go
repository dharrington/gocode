package main

import (
	"bytes"
	"fmt"
	"go/types"
	"log"
	"net"
	"net/rpc"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

	"github.com/mdempsky/gocode/gbimporter"
	"github.com/mdempsky/gocode/lookup"
	"github.com/mdempsky/gocode/reporterrors"
	"github.com/mdempsky/gocode/srcimporter"
	"github.com/mdempsky/gocode/suggest"
)

func doServer() {
	addr := *g_addr
	if *g_sock == "unix" {
		addr = getSocketPath()
	}

	lis, err := net.Listen(*g_sock, addr)
	if err != nil {
		log.Fatal(err)
	}

	sigs := make(chan os.Signal)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		exitServer()
	}()

	if err = rpc.Register(&Server{}); err != nil {
		log.Fatal(err)
	}
	rpc.Accept(lis)
}

func exitServer() {
	if *g_sock == "unix" {
		_ = os.Remove(getSocketPath())
	}
	os.Exit(0)
}

type Server struct {
}

type AutoCompleteRequest struct {
	Filename string
	Data     []byte
	Cursor   int
	Context  gbimporter.PackedContext
}

type AutoCompleteReply struct {
	Candidates []suggest.Candidate
	Len        int
}

func newImporter(ctx *gbimporter.PackedContext, filename string) types.ImporterFrom {
	if *g_importsrc {
		return srcimporter.New(ctx, filename)
	} else {
		return gbimporter.New(ctx, filename)
	}
}
func AutoComplete(req *AutoCompleteRequest, res *AutoCompleteReply) error {
	defer func() {
		if err := recover(); err != nil {
			fmt.Printf("panic: %s\n\n", err)
			debug.PrintStack()

			res.Candidates = []suggest.Candidate{
				{Class: "PANIC", Name: "PANIC", Type: "PANIC"},
			}
		}
	}()
	if *g_debug {
		var buf bytes.Buffer
		log.Printf("Got autocompletion request for '%s'\n", req.Filename)
		log.Printf("Cursor at: %d\n", req.Cursor)
		buf.WriteString("-------------------------------------------------------\n")
		buf.Write(req.Data[:req.Cursor])
		buf.WriteString("#")
		buf.Write(req.Data[req.Cursor:])
		log.Print(buf.String())
		log.Println("-------------------------------------------------------")
	}
	now := time.Now()
	imp := newImporter(&req.Context, req.Filename)

	candidates, d := suggest.New(*g_debug).Suggest(imp, req.Filename, req.Data, req.Cursor)
	elapsed := time.Since(now)
	if *g_debug {
		log.Printf("Elapsed duration: %v\n", elapsed)
		log.Printf("Offset: %d\n", res.Len)
		log.Printf("Number of candidates found: %d\n", len(candidates))
		log.Printf("Candidates are:\n")
		for _, c := range candidates {
			log.Printf("  %s\n", c.String())
		}
		log.Println("=======================================================")
	}
	res.Candidates, res.Len = candidates, d
	return nil
}
func (s *Server) AutoComplete(req *AutoCompleteRequest, res *AutoCompleteReply) error {
	return AutoComplete(req, res)
}

type ReportErrorsRequest struct {
	Filename string
	Data     []byte
	Context  gbimporter.PackedContext
}

type ReportErrorsReply struct {
	Errors []reporterrors.Error
}

func ReportErrors(req *ReportErrorsRequest, res *ReportErrorsReply) error {
	defer func() {
		if err := recover(); err != nil {
			fmt.Printf("panic: %s\n\n", err)
			debug.PrintStack()
			res.Errors = nil
		}
	}()
	imp := newImporter(&req.Context, req.Filename)
	res.Errors = reporterrors.Report(imp, req.Filename, req.Data)
	return nil
}
func (s *Server) ReportErrors(req *ReportErrorsRequest, res *ReportErrorsReply) error {
	return ReportErrors(req, res)
}

type LookupRequest struct {
	Filename string
	Data     []byte
	Cursor   int
	Context  gbimporter.PackedContext
}

type LookupInfo struct {
	Path    string
	Line    int
	Column  int
	Offset  int
	Name    string
	Doc     string
	Type    string
	CallArg int
}
type LookupReply struct {
	Cursor LookupInfo // ident at cursor
	Call   LookupInfo // call at cursor
}

func ToLookupInfo(lu lookup.Result) LookupInfo {
	var li LookupInfo
	li.Name = lu.Name
	li.Doc = lu.Doc
	li.Path = lu.Position.Filename
	li.Line = lu.Position.Line
	li.Column = lu.Position.Column
	li.Offset = lu.Position.Offset
	li.Type = lu.Type
	li.CallArg = lu.CallArg
	return li
}
func Lookup(req *LookupRequest, res *LookupReply) error {
	imp := newImporter(&req.Context, req.Filename)

	lu, call := lookup.Lookup(imp, req.Filename, req.Data, req.Cursor)
	res.Cursor = ToLookupInfo(lu)
	res.Call = ToLookupInfo(call)
	return nil
}

func (s *Server) Lookup(req *LookupRequest, res *LookupReply) error {
	return Lookup(req, res)
}

type ExitRequest struct{}
type ExitReply struct{}

func (s *Server) Exit(req *ExitRequest, res *ExitReply) error {
	go func() {
		time.Sleep(time.Second)
		exitServer()
	}()
	return nil
}
