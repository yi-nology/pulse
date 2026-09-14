// 仿真飞书服务器：实现 pulse 实际调用的全部端点（envelope {code,msg,data}），
// 内存存储，供 E2E 测试 bind/sync/publish/双机对抗。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type record struct {
	RecordID string         `json:"record_id"`
	Fields   map[string]any `json:"fields"`
	LMT      int64          `json:"last_modified_time"`
}
type table struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Fields  []map[string]any   `json:"fields"`
	Records map[string]*record `json:"records"`
	Views   []string           `json:"views"`
}
type base struct {
	Name    string          `json:"name"`
	Tables  map[string]*tab `json:"tables"`
	Docs    []string        `json:"docs"`
}
type tab table // alias 避免与方法遮蔽
type doc struct {
	ID     string            `json:"id"`
	Title  string            `json:"title"`
	Blocks []json.RawMessage `json:"blocks"`
}

type server struct {
	mu       sync.Mutex
	nApp     int
	nTbl     int
	nRec     int
	nDoc     int
	bases    map[string]*base
	docs     map[string]*doc
	lastLMT  int64
}

func (s *server) nextLMT() int64 {
	now := time.Now().Unix()
	if now <= s.lastLMT {
		now = s.lastLMT + 1
	}
	s.lastLMT = now
	return now
}

func env(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data})
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/open-apis/auth/v3/tenant_access_token/internal" && r.Method == "POST":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"test-token","expire":7200}`))
	case path == "/open-apis/bitable/v1/apps" && r.Method == "POST":
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.nApp++
		tok := fmt.Sprintf("app%d", s.nApp)
		s.bases[tok] = &base{Name: body.Name, Tables: map[string]*tab{}}
		env(w, map[string]any{"app": map[string]any{"app_token": tok, "name": body.Name}})
	case strings.HasPrefix(path, "/open-apis/bitable/v1/apps/") && strings.HasSuffix(path, "/tables") && r.Method == "POST":
		tok := segment(path, 4)
		var body struct {
			Table struct {
				Name   string           `json:"name"`
				Fields []map[string]any `json:"fields"`
			} `json:"table"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b := s.bases[tok]
		if b == nil {
			http.NotFound(w, r)
			return
		}
		s.nTbl++
		t := &tab{ID: fmt.Sprintf("tbl%d", s.nTbl), Name: body.Table.Name, Fields: body.Table.Fields, Records: map[string]*record{}}
		b.Tables[t.ID] = t
		env(w, map[string]any{"table_id": t.ID})
	case strings.HasPrefix(path, "/open-apis/bitable/v1/apps/") && strings.HasSuffix(path, "/views") && r.Method == "POST":
		tok, tbl := segment(path, 4), segment(path, 6)
		var body struct {
			ViewName string `json:"view_name"`
			ViewType string `json:"view_type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		t := s.bases[tok].Tables[tbl]
		t.Views = append(t.Views, body.ViewName+":"+body.ViewType)
		env(w, map[string]any{"view_id": "view1"})
	case strings.HasPrefix(path, "/open-apis/bitable/v1/apps/") && strings.HasSuffix(path, "/records/search") && r.Method == "POST":
		tok, tbl := segment(path, 4), segment(path, 6)
		t := s.bases[tok].Tables[tbl]
		items := make([]record, 0, len(t.Records))
		for _, rec := range t.Records {
			items = append(items, *rec)
		}
		env(w, map[string]any{"items": items, "has_more": false})
	case strings.HasPrefix(path, "/open-apis/bitable/v1/apps/") && strings.HasSuffix(path, "/records") && r.Method == "POST":
		tok, tbl := segment(path, 4), segment(path, 6)
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		t := s.bases[tok].Tables[tbl]
		s.nRec++
		rec := &record{RecordID: fmt.Sprintf("rec%d", s.nRec), Fields: body.Fields, LMT: s.nextLMT()}
		t.Records[rec.RecordID] = rec
		env(w, map[string]any{"record": map[string]any{"record_id": rec.RecordID}})
	case strings.HasPrefix(path, "/open-apis/bitable/v1/apps/") && r.Method == "PUT":
		tok, tbl, rid := segment(path, 4), segment(path, 6), segment(path, 8)
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec := s.bases[tok].Tables[tbl].Records[rid]
		if rec == nil {
			http.NotFound(w, r)
			return
		}
		for k, v := range body.Fields {
			rec.Fields[k] = v
		}
		rec.LMT = s.nextLMT()
		env(w, map[string]any{"record": map[string]any{"record_id": rid}})
	case path == "/open-apis/docx/v1/documents" && r.Method == "POST":
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.nDoc++
		id := fmt.Sprintf("doc%d", s.nDoc)
		s.docs[id] = &doc{ID: id, Title: body.Title}
		env(w, map[string]any{"document": map[string]any{"document_id": id}})
	case strings.HasPrefix(path, "/open-apis/docx/v1/documents/") && strings.HasSuffix(path, "/children") && r.Method == "POST":
		docID := segment(path, 4)
		var body struct {
			Index   int               `json:"index"`
			Children []json.RawMessage `json:"children"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		d := s.docs[docID]
		if d == nil {
			http.NotFound(w, r)
			return
		}
		d.Blocks = append(d.Blocks, body.Children...)
		env(w, map[string]any{})
	case path == "/_debug/dump" && r.Method == "GET":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bases": s.bases, "docs": s.docs})
	default:
		log.Printf("fake-feishu: UNMATCHED %s %s", r.Method, path)
		http.NotFound(w, r)
	}
}

// segment 取 /分隔路径 的第 n 段（0=空串）。
func segment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n < len(parts) {
		return parts[n]
	}
	return ""
}

func main() {
	s := &server{bases: map[string]*base{}, docs: map[string]*doc{}}
	addr := "127.0.0.1:19090"
	log.Printf("fake-feishu listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, s))
}
