package parse

import (
	"io"
	"strings"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

const sample = `{"kind":"meta","id":"s1","timestamp":100,"source":"test"}
{"kind":"site","numID":1,"frames":[{"func":"main.alloc","file":"a.go","line":10}]}
{"kind":"type","numID":1,"name":"*buf"}
{"kind":"object","numID":7,"size":64,"type":1,"site":1,"scaleNum":4,"scaleDen":1}
{"kind":"ref","from":7,"to":8}
`

func TestReadAll(t *testing.T) {
	s, err := ReadAll(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if s.Meta.ID != "s1" || s.Meta.Timestamp != 100 {
		t.Fatalf("meta %+v", s.Meta)
	}
	if len(s.Sites) != 1 || s.Sites[0].Frames[0].Func != "main.alloc" {
		t.Fatalf("sites %+v", s.Sites)
	}
	if len(s.Types) != 1 || s.Types[0].Name != "*buf" {
		t.Fatalf("types %+v", s.Types)
	}
	if len(s.Objects) != 1 || s.Objects[0].ScaleNum != 4 {
		t.Fatalf("objects %+v", s.Objects)
	}
	if len(s.Refs) != 1 || s.Refs[0].To != 8 {
		t.Fatalf("refs %+v", s.Refs)
	}
}

func TestStreamingNext(t *testing.T) {
	r := NewReader(strings.NewReader(sample))
	var kinds []string
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, rec.recordKind())
	}
	if len(kinds) != 5 {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestBadRecord(t *testing.T) {
	_, err := ReadAll(strings.NewReader("not json\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = ReadAll(strings.NewReader(`{"kind":"bogus"}` + "\n"))
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestObjectRecordFields(t *testing.T) {
	r := NewReader(strings.NewReader(`{"kind":"object","numID":3,"size":8,"type":2,"site":9,"scaleNum":2,"scaleDen":3}` + "\n"))
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	o := rec.(ObjectRecord).Object
	if o.ID != 3 || o.Size != 8 || o.Type != 2 || o.Site != 9 || o.ScaleNum != 2 || o.ScaleDen != 3 {
		t.Fatalf("%+v", o)
	}
	_ = model.Object{}
}
